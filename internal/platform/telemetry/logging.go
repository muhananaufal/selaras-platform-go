package telemetry

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel/trace"
)

// traceHandler menambahkan trace_id dan span_id ke setiap catatan yang
// ditulis dengan context yang membawa span.
//
// Bidangnya WAJIB berada di tingkat teratas catatan, bukan di dalam grup
// yang kebetulan aktif: Loki dan Grafana mencari `trace_id`, bukan
// `saga.trace_id`. Karena itu handler ini menyimpan handler AKAR beserta
// urutan WithAttrs/WithGroup yang diterapkan padanya, dan saat ada span ia
// menyisipkan bidang trace ke akar lebih dulu sebelum mengulang urutannya.
type traceHandler struct {
	root    slog.Handler
	ops     []func(slog.Handler) slog.Handler
	current slog.Handler
}

// WithTraceContext membungkus handler log supaya catatannya bisa dibawa ke
// trace-nya.
//
// Hanya catatan yang ditulis lewat varian *Context - InfoContext, ErrorContext -
// yang mendapat bidangnya; varian tanpa context memang tidak punya span untuk
// dibaca. Itu alasan seluruh log di jalur permintaan memakai varian context,
// dan alasan keduanya bukan sekadar gaya.
func WithTraceContext(h slog.Handler) slog.Handler {
	if h == nil {
		return nil
	}
	if _, already := h.(*traceHandler); already {
		return h
	}
	return &traceHandler{root: h, current: h}
}

func (h *traceHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.current.Enabled(ctx, level)
}

func (h *traceHandler) Handle(ctx context.Context, r slog.Record) error {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return h.current.Handle(ctx, r)
	}

	traced := h.root.WithAttrs([]slog.Attr{
		slog.String("trace_id", sc.TraceID().String()),
		slog.String("span_id", sc.SpanID().String()),
	})
	for _, op := range h.ops {
		traced = op(traced)
	}
	return traced.Handle(ctx, r)
}

// WithAttrs dan WithGroup WAJIB mencatat operasinya, bukan hanya meneruskan.
// Tanpa itu, logger turunan - log.With("service", ...) - kehilangan bidang
// trace-nya diam-diam, dan itu persis logger yang dipakai hampir semua tempat.
func (h *traceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return h.derive(func(inner slog.Handler) slog.Handler { return inner.WithAttrs(attrs) })
}

func (h *traceHandler) WithGroup(name string) slog.Handler {
	return h.derive(func(inner slog.Handler) slog.Handler { return inner.WithGroup(name) })
}

func (h *traceHandler) derive(op func(slog.Handler) slog.Handler) slog.Handler {
	ops := make([]func(slog.Handler) slog.Handler, 0, len(h.ops)+1)
	ops = append(ops, h.ops...)
	ops = append(ops, op)
	return &traceHandler{root: h.root, ops: ops, current: op(h.current)}
}
