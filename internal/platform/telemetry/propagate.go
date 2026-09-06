package telemetry

import (
	"context"

	"github.com/twmb/franz-go/pkg/kgo"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	eventsv1 "github.com/muhananaufal/selaras-platform-go/gen/events/v1"
)

// traceParentKey adalah nama bidang W3C yang dibawa Envelope.
const traceParentKey = "traceparent"

// scopeName menandai span yang dibuat paket ini, bukan oleh instrumentasi
// pustaka.
const scopeName = "github.com/muhananaufal/selaras-platform-go/internal/platform/telemetry"

// InjectEnvelope menyalin konteks trace aktif ke dalam envelope.
//
// Ini satu-satunya jembatan trace yang menyeberangi broker. gRPC dan HTTP
// membawa traceparent di header masing-masing, tetapi event ditulis ke tabel
// outbox dan diterbitkan belakangan oleh relay yang tidak tahu apa-apa soal
// permintaan yang melahirkannya. Yang bisa dibawa hanyalah yang ada di
// dalam payload-nya sendiri.
//
// Tanpa span aktif, envelope dibiarkan apa adanya: event yang lahir dari
// pekerjaan terjadwal memang tidak punya induk, dan mengarangnya akan
// menghasilkan trace satu span yang menyesatkan.
func InjectEnvelope(ctx context.Context, env *eventsv1.Envelope) {
	if env == nil {
		return
	}
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return
	}

	carrier := propagation.MapCarrier{}
	propagation.TraceContext{}.Inject(ctx, carrier)

	parent := carrier.Get(traceParentKey)
	if parent == "" {
		return
	}
	traceID := sc.TraceID().String()
	env.TraceParent = &parent
	env.TraceId = &traceID
}

// ContextFromEnvelope mengembalikan ctx yang induk span-nya adalah trace di
// dalam envelope, atau ctx apa adanya bila envelope tidak membawa trace.
func ContextFromEnvelope(ctx context.Context, env *eventsv1.Envelope) context.Context {
	parent := env.GetTraceParent()
	if parent == "" {
		return ctx
	}
	return propagation.TraceContext{}.Extract(ctx,
		propagation.MapCarrier{traceParentKey: parent})
}

// StartConsumerSpan membuka span untuk pemrosesan satu event dari broker.
//
// Span-nya menjadi ANAK dari permintaan yang menulis event itu, bukan trace
// baru yang ditautkan. Semantik pesan OTel menyarankan tautan untuk
// pemrosesan asinkron, tetapi yang ingin dijawab di sini adalah "apa yang
// terjadi pada permintaan pengguna ini" - dan jawabannya harus terbaca
// sebagai satu trace dari edge sampai worker (F9-07), bukan dua trace yang
// harus dicocokkan orang secara manual.
func StartConsumerSpan(
	ctx context.Context, env *eventsv1.Envelope, rec *kgo.Record,
) (context.Context, trace.Span) {
	ctx = ContextFromEnvelope(ctx, env)

	return otel.Tracer(scopeName).Start(ctx, rec.Topic+" process",
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(
			attribute.String("messaging.system", "kafka"),
			attribute.String("messaging.operation.type", "process"),
			attribute.String("messaging.destination.name", rec.Topic),
			attribute.Int("messaging.destination.partition.id", int(rec.Partition)),
			attribute.Int64("messaging.kafka.offset", rec.Offset),
			attribute.String("selaras.event.type", headerOf(rec, "event_type")),
			attribute.String("selaras.event.id", env.GetEventId()),
		))
}

// headerOf membaca satu header record; kosong bila tidak ada.
func headerOf(rec *kgo.Record, key string) string {
	for _, h := range rec.Headers {
		if h.Key == key {
			return string(h.Value)
		}
	}
	return ""
}

// StartSpan membuka span internal biasa - untuk pekerjaan yang tidak
// diinstrumentasi pustaka, seperti satu panggilan ke penyedia LLM.
func StartSpan(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	return otel.Tracer(scopeName).Start(ctx, name,
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(attrs...))
}

// End menutup span dan mencatat galatnya, bila ada.
//
// Ia ada supaya setiap tempat yang menutup span melakukannya dengan cara yang
// sama. Span yang berakhir dengan galat tetapi berstatus OK adalah span yang
// tidak akan pernah ditemukan saat seseorang mencari yang gagal.
func End(span trace.Span, err error) {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	span.End()
}
