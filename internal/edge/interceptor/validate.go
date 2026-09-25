package interceptor

import (
	"context"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"github.com/muhananaufal/selaras-platform-go/internal/edge/rpcerr"
	"github.com/muhananaufal/selaras-platform-go/internal/edge/wire"
)

// EnumGuard refuses any request message carrying an enum value the schema
// does not define (see wire.UndefinedEnums).
//
// It runs for every procedure, so no handler has to remember it.
type EnumGuard struct{}

var _ connect.Interceptor = EnumGuard{}

func (EnumGuard) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if msg, ok := req.Any().(proto.Message); ok {
			if violations := wire.UndefinedEnums(msg.ProtoReflect()); len(violations) > 0 {
				return nil, rpcerr.Invalid(violations...)
			}
		}
		return next(ctx, req)
	}
}

func (EnumGuard) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

// WrapStreamingHandler checks each message as it is received. The public
// contract has only server streams, whose single request is received here too.
func (EnumGuard) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		return next(ctx, &guardedConn{StreamingHandlerConn: conn})
	}
}

type guardedConn struct {
	connect.StreamingHandlerConn
}

func (c *guardedConn) Receive(msg any) error {
	if err := c.StreamingHandlerConn.Receive(msg); err != nil {
		return err
	}
	if m, ok := msg.(proto.Message); ok {
		if violations := wire.UndefinedEnums(m.ProtoReflect()); len(violations) > 0 {
			return rpcerr.Invalid(violations...)
		}
	}
	return nil
}
