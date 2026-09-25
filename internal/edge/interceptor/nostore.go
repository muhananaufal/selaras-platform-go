package interceptor

import (
	"context"
	"errors"

	"connectrpc.com/connect"
)

// NoStore marks every response as not cacheable.
//
// Read procedures accept HTTP GET (NO_SIDE_EFFECTS), which makes their
// answers cacheable by default in browsers and any shared proxy. Every answer
// of this contract is one user's own data - a profile, a risk score, a chat -
// so nothing may be kept by a shared cache, and "private" alone would still
// leave it on a shared computer's disk. The header is set on errors too:
// a cached 404 can outlive the thing it described.
type NoStore struct{}

var _ connect.Interceptor = NoStore{}

const cacheControl = "no-store"

func (NoStore) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		res, err := next(ctx, req)
		if err != nil {
			if cerr, ok := asConnectError(err); ok {
				cerr.Meta().Set("Cache-Control", cacheControl)
			}
			return nil, err
		}
		res.Header().Set("Cache-Control", cacheControl)
		return res, nil
	}
}

func (NoStore) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (NoStore) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		conn.ResponseHeader().Set("Cache-Control", cacheControl)
		return next(ctx, conn)
	}
}

func asConnectError(err error) (*connect.Error, bool) {
	var cerr *connect.Error
	if errors.As(err, &cerr) {
		return cerr, true
	}
	return nil, false
}
