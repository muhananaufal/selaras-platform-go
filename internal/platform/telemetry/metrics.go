// Package telemetry menyediakan metrik yang bisa dibaca dari luar proses.
//
// Ia sengaja kecil. Instrumentasi menyeluruh - trace, metrik, dan log di
// seluruh unit - adalah F9-05, dan mendahuluinya di sini akan mengunci
// keputusan yang belum diambil. Yang ada di sini hanya yang dibutuhkan F3-15:
// tiga angka yang menjawab "apakah antreannya sehat".
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
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

	exporter, err := otelprom.New(otelprom.WithRegisterer(registry))
	if err != nil {
		return nil, fmt.Errorf("building the prometheus exporter: %w", err)
	}

	// Versi semconv WAJIB sama dengan yang dipakai resource.Default()
	// [otel/sdk@v1.46.0/resource/builtin.go:16], kalau tidak Merge menolak
	// dengan "conflicting Schema URL" - dan metriknya diam-diam tidak ada.
	// Ini benar-benar terjadi: v1.26.0 vs v1.43.0.
	res, err := resource.Merge(resource.Default(),
		resource.NewWithAttributes(semconv.SchemaURL,
			semconv.ServiceName(serviceName)))
	if err != nil {
		return nil, fmt.Errorf("describing this service: %w", err)
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
