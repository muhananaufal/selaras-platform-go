package authn

import (
	"context"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// The metadata name that carries the token, the same as its HTTP header so
// whoever reads a trace or a tcpdump does not have to learn a new name.
const metadataKey = "authorization"

// userIDField is the name of the field that authorisation is based on
// across the whole api/proto contract. A request that has it MUST arrive
// with a token whose `sub` matches; a request without this field (Register,
// Login, ResolveRiskRegion) is a public RPC.
const userIDField protoreflect.Name = "user_id"

// TokenVerifier is what the interceptor needs from a verifier.
type TokenVerifier interface {
	Verify(raw string) (Principal, error)
}

// UnaryServerInterceptor enforces ADR-026 on every unary RPC.
//
// Three states, three answers:
//   - the request has user_id and the token matches -> passed through, Principal in ctx;
//   - the request has user_id but the token is missing or invalid ->
//     Unauthenticated; present but someone else's -> PermissionDenied;
//   - the request has no user_id -> a public RPC, passed through (a token
//     that came along is still verified when present, so a forged token does
//     not "get through" just because the RPC is public).
//
// gRPC health and reflection are skipped: neither is user data.
func UnaryServerInterceptor(verifier TokenVerifier) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler,
	) (any, error) {
		if isPlumbing(info.FullMethod) {
			return handler(ctx, req)
		}

		raw, hasToken := bearerFromIncoming(ctx)
		claimed, hasUserID := userIDOf(req)

		var principal Principal
		if hasToken {
			p, err := verifier.Verify(raw)
			if err != nil {
				return nil, status.Error(codes.Unauthenticated, "the access token is not valid")
			}
			principal = p
			ctx = withPrincipal(ctx, p)
		}

		if hasUserID {
			if !hasToken {
				return nil, status.Error(codes.Unauthenticated, "this request needs an access token")
			}
			if claimed != principal.UserID {
				// 403, not 404: this is not "the resource does not exist" (S9), this is
				// a caller claiming to be someone else. The caller is the one at fault,
				// and it is entitled to know.
				return nil, status.Error(codes.PermissionDenied, "the access token does not belong to this user")
			}
		}
		return handler(ctx, req)
	}
}

// UnaryClientInterceptor forwards the token downstream: from WithToken (the
// gateway) or from the incoming metadata (a service calling another service
// on behalf of the same user). Without a token the request is sent as it is
// - public RPCs and internal calls without a user still work.
func UnaryClientInterceptor() grpc.UnaryClientInterceptor {
	return func(
		ctx context.Context, method string, req, reply any,
		cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption,
	) error {
		if raw, ok := outgoingToken(ctx); ok {
			ctx = metadata.AppendToOutgoingContext(ctx, metadataKey, "Bearer "+raw)
		}
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

func outgoingToken(ctx context.Context) (string, bool) {
	if raw, ok := TokenFrom(ctx); ok {
		return raw, true
	}
	return bearerFromIncoming(ctx)
}

func bearerFromIncoming(ctx context.Context) (string, bool) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", false
	}
	for _, v := range md.Get(metadataKey) {
		scheme, value, found := strings.Cut(strings.TrimSpace(v), " ")
		if found && strings.EqualFold(scheme, "Bearer") && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value), true
		}
	}
	return "", false
}

// userIDOf reads the user_id field through protobuf reflection, so one
// interceptor enforces the same rule across some 60 RPCs without a single
// handler having to remember to call it.
func userIDOf(req any) (string, bool) {
	msg, ok := req.(proto.Message)
	if !ok {
		return "", false
	}
	m := msg.ProtoReflect()
	fd := m.Descriptor().Fields().ByName(userIDField)
	if fd == nil || fd.Kind() != protoreflect.StringKind || fd.IsList() {
		return "", false
	}
	return m.Get(fd).String(), true
}

func isPlumbing(fullMethod string) bool {
	return strings.HasPrefix(fullMethod, "/grpc.health.") ||
		strings.HasPrefix(fullMethod, "/grpc.reflection.")
}
