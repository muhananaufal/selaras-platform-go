// Package telemetry menyediakan metrik, trace, dan log yang saling terhubung.
//
// Tiga hal yang dijaga di sini:
//
//   - Metrik disajikan dalam format Prometheus dari setiap proses, sehingga
//     ia bisa dibaca dengan curl tanpa infrastruktur apa pun.
//   - Trace dikirim lewat OTLP HANYA bila alamat collector dikonfigurasi, dan
//     konteksnya menyeberangi broker lewat Envelope (lihat propagate.go) -
//     bukan hanya gRPC dan HTTP.
//   - Log yang ditulis dengan context membawa trace_id dan span_id (lihat
//     logging.go), sehingga satu baris log bisa dibawa ke trace-nya.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

// Meters adalah pabrik instrumen beserta handler yang menyajikannya.
type Meters struct {
	provider *sdkmetric.MeterProvider
	registry *prometheus.Registry
	meter    metric.Meter
}

// New menyiapkan meter yang diekspor dalam format Prometheus.
//
// Prometheus, bukan OTLP, dengan sengaja untuk sekarang: OTLP butuh collector
// yang berjalan, dan metrik yang tidak bisa dibaca tanpa infrastruktur tambahan
// tidak menolong siapa pun saat ada yang salah pada pukul tiga pagi. Endpoint
// yang bisa di-curl bisa dibaca siapa saja.
func New(serviceName string) (*Meters, error) {
	if serviceName == "" {
		return nil, errors.New("a service without a name produces metrics nobody can attribute")
	}

	// Registry sendiri, bukan prometheus.DefaultRegisterer: registry global
	// membawa metrik dari pustaka mana pun yang kebetulan terpasang, dan
	// tabrakan namanya baru terlihat saat proses gagal start.
	registry := prometheus.NewRegistry()

	// Metrik proses dan runtime Go ikut disajikan: CPU-detik, RSS, goroutine,
	// dan GC. Tanpa keduanya, "berapa biaya seribu permintaan" (F9-16) dan
	// "berapa request yang dibutuhkan HPA" (F9-21) hanya bisa dijawab dari
	// docker stats - yang tidak ada di klaster.
	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	exporter, err := otelprom.New(otelprom.WithRegisterer(registry))
	if err != nil {
		return nil, fmt.Errorf("building the prometheus exporter: %w", err)
	}

	res, err := describe(serviceName)
	if err != nil {
		return nil, err
	}

	provider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(exporter),
		sdkmetric.WithResource(res),
	)

	return &Meters{
		provider: provider,
		registry: registry,
		meter:    provider.Meter(serviceName),
	}, nil
}

// Meter mengembalikan meter untuk membuat instrumen.
func (m *Meters) Meter() metric.Meter { return m.meter }

// Handler menyajikan metriknya.
func (m *Meters) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{
		// Galat saat mengumpulkan metrik dilaporkan sebagai 500, bukan
		// disembunyikan di balik jawaban yang terlihat baik. Metrik yang salah
		// lebih berbahaya daripada metrik yang tidak ada: yang kedua terlihat.
		ErrorHandling: promhttp.HTTPErrorOnError,
	})
}

// Shutdown menutup penyedia metriknya.
//
// Ia mengembalikan galat alih-alih menelannya: penutupan yang gagal berarti
// pembacaan terakhir mungkin tidak sempat keluar, dan itu perlu terlihat di log
// shutdown - bukan hilang.
func (m *Meters) Shutdown(ctx context.Context) error {
	if err := m.provider.Shutdown(ctx); err != nil {
		return fmt.Errorf("shutting down the meter provider: %w", err)
	}
	return nil
}
