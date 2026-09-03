package llmworker

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Metrics adalah tiga angka yang menjawab "apakah antreannya sehat" (F3-15).
//
// Tiga, bukan tiga puluh. Metrik yang tidak akan dilihat siapa pun saat ada
// masalah hanya menambah yang harus disaring, dan yang tersisa di sini adalah
// yang benar-benar mengubah tindakan: seberapa jauh tertinggal, berapa lama
// satu pekerjaan, dan berapa yang gagal.
type Metrics struct {
	// duration mengukur berapa lama satu pekerjaan, dari klaim sampai selesai.
	//
	// Histogram, bukan rata-rata: rata-rata menyembunyikan ekor, dan ekor itulah
	// yang membuat antrean menumpuk. Satu pekerjaan yang menunggu lima menit
	// menahan partisinya selama itu.
	duration metric.Float64Histogram

	// outcomes menghitung pekerjaan menurut hasilnya.
	//
	// Satu penghitung dengan atribut, bukan tiga penghitung terpisah: jumlah
	// yang berhasil dan yang gagal harus bisa dibandingkan tanpa menjumlahkan
	// deret yang berbeda.
	outcomes metric.Int64Counter
}

// Outcome adalah nilai atribut untuk penghitung hasil.
const (
	OutcomeCompleted = "completed"
	OutcomeFailed    = "failed"
	OutcomeDead      = "dead"
	OutcomeSkipped   = "skipped"
	OutcomeAbandoned = "abandoned"
)

// NewMetrics membuat instrumennya.
func NewMetrics(meter metric.Meter) (*Metrics, error) {
	if meter == nil {
		return nil, errors.New("nil meter")
	}

	duration, err := meter.Float64Histogram("llm_job_duration_seconds",
		metric.WithDescription("How long one LLM job took, from claim to outcome"),
		metric.WithUnit("s"))
	if err != nil {
		return nil, fmt.Errorf("building the duration histogram: %w", err)
	}

	outcomes, err := meter.Int64Counter("llm_jobs_total",
		metric.WithDescription("LLM jobs by outcome"))
	if err != nil {
		return nil, fmt.Errorf("building the outcome counter: %w", err)
	}

	return &Metrics{duration: duration, outcomes: outcomes}, nil
}

// Observe mencatat satu pekerjaan yang selesai.
func (m *Metrics) Observe(ctx context.Context, outcome string, took time.Duration) {
	if m == nil {
		// Worker boleh berjalan tanpa metrik. Ia mencatat lebih sedikit, tetapi
		// tidak berperilaku lain - dan nil check di sini lebih baik daripada
		// setiap pemanggil harus mengingatnya.
		return
	}

	attrs := metric.WithAttributes(attribute.String("outcome", outcome))
	m.outcomes.Add(ctx, 1, attrs)
	m.duration.Record(ctx, took.Seconds(), attrs)
}

// LagReporter melaporkan consumer lag secara berkala.
//
// Lag TIDAK bisa dihitung dari sisi konsumen sendiri: ia perlu tahu offset
// terakhir di broker, dan itu pertanyaan admin. Karena itu ia diambil lewat
// kadm, bukan dari klien konsumennya.
type LagReporter struct {
	admin *kadm.Client
	group string
	gauge metric.Int64ObservableGauge
}

// NewLagReporter mendaftarkan pengukuran lag pada meter.
//
// Ia observable gauge, bukan nilai yang didorong: lag berubah terus, dan
// mendorongnya berarti memilih ritme sendiri yang belum tentu sama dengan
// ritme pembacanya. Observable gauge diukur saat ditanya.
func NewLagReporter(meter metric.Meter, client *kgo.Client, group string) (*LagReporter, error) {
	switch {
	case meter == nil:
		return nil, errors.New("nil meter")
	case client == nil:
		return nil, errors.New("nil kafka client")
	case group == "":
		return nil, errors.New("lag has no meaning without a consumer group")
	}

	gauge, err := meter.Int64ObservableGauge("kafka_consumer_lag",
		metric.WithDescription("How many records this consumer group is behind, per partition"))
	if err != nil {
		return nil, fmt.Errorf("building the lag gauge: %w", err)
	}

	r := &LagReporter{admin: kadm.NewClient(client), group: group, gauge: gauge}

	if _, err := meter.RegisterCallback(r.observe, gauge); err != nil {
		return nil, fmt.Errorf("registering the lag callback: %w", err)
	}
	return r, nil
}

// observe membaca lag saat metriknya diminta.
func (r *LagReporter) observe(ctx context.Context, o metric.Observer) error {
	// Batas waktu sendiri: pembacaan metrik tidak boleh menggantung karena
	// broker yang lambat. Halaman metrik yang tidak pernah menjawab sama
	// buruknya dengan metrik yang tidak ada.
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	lags, err := r.admin.Lag(ctx, r.group)
	if err != nil {
		return fmt.Errorf("reading consumer lag: %w", err)
	}

	described, ok := lags[r.group]
	if !ok {
		// Group yang belum pernah ada bukan galat - worker yang baru dinyalakan
		// belum bergabung. Melaporkannya sebagai galat akan membuat halaman
		// metriknya gagal selama beberapa detik pertama setiap start.
		return nil
	}

	for topic, partitions := range described.Lag {
		for partition, memberLag := range partitions {
			if memberLag.Err != nil || memberLag.Lag < 0 {
				// Lag -1 berarti offsetnya tidak bisa dibaca
				// [kadm@v1.18.0/groups.go:1412]. Melaporkannya sebagai angka
				// akan menampilkan -1 di grafik seolah itu pengukuran.
				continue
			}
			o.ObserveInt64(r.gauge, memberLag.Lag, metric.WithAttributes(
				attribute.String("topic", topic),
				attribute.Int("partition", int(partition)),
			))
		}
	}
	return nil
}
