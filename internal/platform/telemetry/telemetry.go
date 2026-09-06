package telemetry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
)

// EndpointVariable adalah variabel yang menyalakan pengiriman trace.
//
// Namanya milik spesifikasi OTel, bukan milik proyek ini, dan itu disengaja:
// exporter membacanya sendiri, sehingga tidak ada penerjemahan dari nama
// proyek ke nama pustaka yang bisa salah.
const EndpointVariable = "OTEL_EXPORTER_OTLP_ENDPOINT"

// Telemetry adalah seluruh instrumentasi satu proses: metrik, trace, dan
// propagasi konteks (F9-05).
type Telemetry struct {
	meters *Meters
	traces *sdktrace.TracerProvider
}

// Start menyiapkan telemetri dan memasangnya sebagai penyedia global.
//
// Global, dengan sengaja. Instrumentasi pustaka - gRPC, Gin - mengambil
// penyedia dari otel.GetTracerProvider() bila tidak diberi apa-apa, dan
// meneruskan penyedia secara eksplisit ke setiap tempat berarti satu tempat
// yang terlewat menghasilkan span yang diam-diam dibuang.
//
// Trace HANYA dikirim bila OTEL_EXPORTER_OTLP_ENDPOINT terisi. Tanpa itu,
// proses tetap menyala dengan tracer tanpa-operasi, dan keadaan itu
// dinyatakan di log - bukan disembunyikan di balik grafik yang kosong.
// Propagator W3C tetap dipasang dalam kedua keadaan, sehingga traceparent
// yang datang dari luar tetap diteruskan sekalipun proses ini sendiri tidak
// merekam.
func Start(ctx context.Context, serviceName string, log *slog.Logger) (*Telemetry, error) {
	if log == nil {
		return nil, errors.New("nil logger")
	}

	meters, err := New(serviceName)
	if err != nil {
		return nil, err
	}
	otel.SetMeterProvider(meters.provider)

	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{}))

	// Galat exporter - collector mati, antrean penuh - dilaporkan lewat log
	// proses, bukan ke stderr mentah tempat tidak ada yang membacanya.
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		log.Error("telemetry export failed", "error", err)
	}))

	t := &Telemetry{meters: meters}

	endpoint := os.Getenv(EndpointVariable)
	if endpoint == "" {
		log.Info("tracing is off; spans are not exported", "variable", EndpointVariable)
		return t, nil
	}

	// Alamat, TLS, dan header dibaca exporter dari OTEL_EXPORTER_OTLP_*
	// sendiri. Tidak ada yang dibaca dua kali di sini supaya tidak ada dua
	// sumber kebenaran untuk satu alamat.
	exporter, err := otlptracegrpc.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("building the trace exporter: %w", err)
	}

	res, err := describe(serviceName)
	if err != nil {
		return nil, err
	}

	// Sampler dibaca dari OTEL_TRACES_SAMPLER; bawaannya parentbased_always_on
	// [otel/sdk@v1.46.0/trace/sampler_env.go]. Tidak ditimpa di sini: rasio
	// sampling adalah keputusan per lingkungan, bukan per kode.
	t.traces = sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(t.traces)

	log.Info("tracing is on", "endpoint", endpoint)
	return t, nil
}

// Meter mengembalikan meter untuk membuat instrumen.
func (t *Telemetry) Meter() metric.Meter { return t.meters.Meter() }

// Handler menyajikan metriknya dalam format Prometheus.
func (t *Telemetry) Handler() http.Handler { return t.meters.Handler() }

// Shutdown mengosongkan antrean span lalu menutup kedua penyedia.
//
// Trace ditutup lebih dulu: span yang belum terkirim hilang bila prosesnya
// keluar, sementara metrik dibaca oleh yang menariknya dan tidak punya
// antrean yang perlu dikosongkan.
func (t *Telemetry) Shutdown(ctx context.Context) error {
	var errs []error
	if t.traces != nil {
		if err := t.traces.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("shutting down the tracer provider: %w", err))
		}
	}
	if err := t.meters.Shutdown(ctx); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// describe menyusun resource yang menempel pada setiap span dan metrik.
//
// Versi semconv WAJIB sama dengan yang dipakai resource.Default()
// [otel/sdk@v1.46.0/resource/builtin.go:16], kalau tidak Merge menolak
// dengan "conflicting Schema URL" - dan telemetrinya diam-diam tidak ada.
// Ini benar-benar terjadi: v1.26.0 vs v1.43.0.
func describe(serviceName string) (*resource.Resource, error) {
	res, err := resource.Merge(resource.Default(),
		resource.NewWithAttributes(semconv.SchemaURL,
			semconv.ServiceName(serviceName)))
	if err != nil {
		return nil, fmt.Errorf("describing this service: %w", err)
	}
	return res, nil
}
