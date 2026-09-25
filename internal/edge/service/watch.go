package service

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
)

// WatchConfig bounds every Watch* stream.
//
// A stream is held open by the gateway, so it has to end on its own: a
// client that forgot to close it must not hold a connection and a polling
// loop forever. When MaxDuration passes while the resource is still pending,
// the stream ends cleanly and the client reopens it - the first message of a
// new stream is always the current state, so nothing is missed.
type WatchConfig struct {
	// Interval is how often the upstream service is asked. Each tick is one
	// gRPC read on the owning service.
	Interval time.Duration

	// MaxDuration is how long one stream may stay open.
	MaxDuration time.Duration
}

// DefaultWatch is the default bound.
//
// Two seconds between reads: an LLM reply takes several seconds to tens of
// seconds, so a faster tick buys little latency for a lot of reads. Five
// minutes per stream: long enough for an ordinary job, short enough that a
// forgotten stream costs little. Both are stated in docs/runbook/edge-gateway.md.
var DefaultWatch = WatchConfig{Interval: 2 * time.Second, MaxDuration: 5 * time.Minute}

// watch polls fetch and sends a message whenever the result changes.
//
// This is a server-side poll behind a stream, deliberately: it is stateless,
// so it works across any number of gateway replicas with no fan-out between
// them. Pushing from the llm.results topic instead is Wave 2 of the plan, to
// be done when the number of open streams makes the reads cost something.
//
// fetch returns the message to send and whether the resource has reached a
// final state; after a final state is sent, the stream ends.
func watch[T any, PT interface {
	*T
	proto.Message
}](
	ctx context.Context,
	cfg WatchConfig,
	stream *connect.ServerStream[T],
	fetch func(context.Context) (PT, bool, error),
) error {
	ctx, cancel := context.WithTimeout(ctx, cfg.MaxDuration)
	defer cancel()

	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()

	var last PT
	sent := false
	for {
		msg, done, err := fetch(ctx)
		if err != nil {
			// The stream's own deadline passing is the normal end, not an error.
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return nil
			}
			return err
		}

		if !sent || !proto.Equal(msg, last) {
			if err := stream.Send((*T)(msg)); err != nil {
				return err
			}
			last, sent = msg, true
		}
		if done {
			return nil
		}

		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return nil
			}
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
