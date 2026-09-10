// Package rpc holds the gRPC client options used across units.
package rpc

import (
	"context"
	"time"

	"google.golang.org/grpc"
)

// DefaultUpstreamTimeout is the deadline for one call to another service
// when the caller sets none of its own.
//
// Five seconds: ten times the longest RPC ever measured (Register with
// argon2id, p99 under half a second; F9-10 performance report), and still
// below the limit at which a user decides the app is dead. Chaos F9-13 with
// ten seconds showed the FIRST call to a freshly dead service waiting the
// full deadline - the gRPC client still trying to connect - so this number
// is how long the first user waits before a 504, and ten is too long for
// that.
const DefaultUpstreamTimeout = 5 * time.Second

// WithUpstreamDeadline bounds every unary call that has no deadline yet.
//
// This was born from chaos F9-13: with profile-svc stopped, GET /profile at
// the gateway did NOT answer 503 - it hung until the client gave up. A gRPC
// client that is reconnecting holds the RPC while it tries, and without a
// deadline from the caller that "trying" has no end. The bound here turns
// that failure into a DeadlineExceeded that the gateway maps to 504, within
// a time that can be explained to anyone.
//
// A deadline ALREADY on the ctx is honoured: a caller that knows better -
// background work with its own deadline - is not overridden.
func WithUpstreamDeadline(timeout time.Duration) grpc.DialOption {
	if timeout <= 0 {
		timeout = DefaultUpstreamTimeout
	}
	return grpc.WithChainUnaryInterceptor(func(
		ctx context.Context, method string, req, reply any,
		cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption,
	) error {
		if _, has := ctx.Deadline(); !has {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, timeout)
			defer cancel()
		}
		return invoker(ctx, method, req, reply, cc, opts...)
	})
}
