package service

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"google.golang.org/protobuf/proto"

	"github.com/muhananaufal/selaras-platform-go/internal/platform/watchhint"
)

// WatchConfig bounds every Watch* stream.
//
// A stream is held open by the gateway, so it has to end on its own: a
// client that forgot to close it must not hold a connection and a polling
// loop forever. When MaxDuration passes while the resource is still pending,
// the stream ends cleanly and the client reopens it - the first message of a
// new stream is always the current state, so nothing is missed.
type WatchConfig struct {
	// Interval is the fallback poll: how long a stream waits for a hint
	// before it reads its owning service anyway. Each read is one gRPC call.
	Interval time.Duration

	// MaxDuration is how long one stream may stay open.
	MaxDuration time.Duration

	// Hints subscribes a stream to the hints of its aggregate (ADR-029):
	// wake delivers them, and stop ends the subscription. Nil means the
	// stream only polls.
	Hints func(key watchhint.Key) (wake <-chan watchhint.Reason, stop func())

	// Fetches counts every read by its reason: open, hint, resubscribe, or
	// fallback. Nil counts nothing.
	Fetches metric.Int64Counter
}

// DefaultWatch is the default bound, without hints; the gateway adds its
// hub (HintsFrom) and its counter.
//
// Ten seconds between fallback reads: hints bring a result at once, and the
// fallback only covers a hint that was lost - Redis down when the result was
// stored, or the owner stopping between its commit and the publish. Five
// minutes per stream: long enough for an ordinary job, short enough that a
// forgotten stream costs little. Both are stated in
// docs/runbook/edge-gateway.md.
var DefaultWatch = WatchConfig{Interval: 10 * time.Second, MaxDuration: 5 * time.Minute}

// HintsFrom adapts a replica's hub to WatchConfig.Hints.
func HintsFrom(hub *watchhint.Hub) func(watchhint.Key) (<-chan watchhint.Reason, func()) {
	return func(key watchhint.Key) (<-chan watchhint.Reason, func()) {
		s := hub.Subscribe(key)
		return s.C(), s.Close
	}
}

// watchResult is one read of the watched resource.
type watchResult[PT proto.Message] struct {
	// Msg is sent to the client when it differs from the last one sent.
	Msg PT

	// Done says the resource reached a final state; the stream ends after
	// sending it.
	Done bool

	// Key is the aggregate whose hints wake this stream. A zero key means
	// the stream only polls.
	Key watchhint.Key
}

// watch sends a message whenever the watched resource changes.
//
// The owning service announces its changes through a hint after committing
// them (ADR-029), and the stream reads again when one arrives. The hint only
// says "read again": the content still comes from the owner through the
// authorised gRPC path, so a stray hint costs one read and nothing else.
// Because a hint can be lost, the stream also reads after Interval without
// one.
func watch[T any, PT interface {
	*T
	proto.Message
}](
	ctx context.Context,
	cfg WatchConfig,
	stream *connect.ServerStream[T],
	fetch func(context.Context) (watchResult[PT], error),
) error {
	return watchLoop(ctx, cfg, func(msg PT) error { return stream.Send((*T)(msg)) }, fetch)
}

// watchLoop is watch without the transport, so its timing can be tested.
func watchLoop[PT proto.Message](
	ctx context.Context,
	cfg WatchConfig,
	send func(PT) error,
	fetch func(context.Context) (watchResult[PT], error),
) error {
	ctx, cancel := context.WithTimeout(ctx, cfg.MaxDuration)
	defer cancel()

	fallback := time.NewTimer(cfg.Interval)
	defer fallback.Stop()

	// A nil channel blocks forever, so a stream without hints simply waits for
	// the fallback.
	var (
		wake <-chan watchhint.Reason
		stop func()
	)
	subscribed := false
	defer func() {
		if stop != nil {
			stop()
		}
	}()

	var last PT
	sent := false
	reason := "open"
	for {
		countFetch(ctx, cfg.Fetches, reason)
		res, err := fetch(ctx)
		if err != nil {
			// The stream's own deadline passing is the normal end, not an error.
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return nil
			}
			return err
		}

		// Subscribed after the first read, because only the owner's answer
		// names the aggregate (the request carries a public slug). A hint
		// sent between that read and this line is missed; the fallback
		// covers it.
		if !subscribed && cfg.Hints != nil && res.Key != (watchhint.Key{}) {
			wake, stop = cfg.Hints(res.Key)
			subscribed = true
		}

		if !sent || !proto.Equal(res.Msg, last) {
			if err := send(res.Msg); err != nil {
				return err
			}
			last, sent = res.Msg, true
		}
		if res.Done {
			return nil
		}

		// As of Go 1.23 a Reset timer never delivers a value from its
		// previous setting [GOROOT time/sleep.go, Timer.Reset].
		fallback.Reset(cfg.Interval)
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return nil
			}
			return ctx.Err()
		case r := <-wake:
			reason = string(r)
		case <-fallback.C:
			reason = "fallback"
		}
	}
}

func countFetch(ctx context.Context, c metric.Int64Counter, reason string) {
	if c == nil {
		return
	}
	c.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", reason)))
}
