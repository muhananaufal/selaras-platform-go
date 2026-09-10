package telemetry

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel/trace"
)

// traceHandler adds trace_id and span_id to every record written with a
// context that carries a span.
//
// The fields MUST sit at the top level of the record, not inside whichever
// group happens to be active: Loki and Grafana look for `trace_id`, not
// `saga.trace_id`. That is why this handler keeps the ROOT handler together
// with the sequence of WithAttrs/WithGroup applied to it, and when a span
// is present it inserts the trace fields at the root first before replaying
// that sequence.
type traceHandler struct {
	root    slog.Handler
	ops     []func(slog.Handler) slog.Handler
	current slog.Handler
}

// WithTraceContext wraps a log handler so its records can be followed to their
// trace.
//
// Only records written through the *Context variants - InfoContext, ErrorContext
// - get the fields; the variants without a context have no span to read. That is
// why every log on the request path uses the context variant, and why the two
// are not merely a matter of style.
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

// WithAttrs and WithGroup MUST record the operation, not just pass it through.
// Otherwise a derived logger - log.With("service", ...) - silently loses its
// trace fields, and that is precisely the logger used almost everywhere.
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
