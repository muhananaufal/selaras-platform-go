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

// Nama metadata yang membawa token, sama dengan header HTTP-nya supaya yang
// membaca trace atau tcpdump tidak perlu belajar nama baru.
const metadataKey = "authorization"

// userIDField adalah nama bidang yang menjadi dasar otorisasi di seluruh
// kontrak api/proto. Permintaan yang memilikinya WAJIB datang dengan token
// yang `sub`-nya sama; permintaan tanpa bidang ini (Register, Login,
// ResolveRiskRegion) adalah RPC publik.
const userIDField protoreflect.Name = "user_id"

// TokenVerifier adalah yang dibutuhkan interceptor dari sebuah pemeriksa.
type TokenVerifier interface {
	Verify(raw string) (Principal, error)
}

// UnaryServerInterceptor menegakkan ADR-026 pada setiap RPC unary.
//
// Tiga keadaan, tiga jawaban:
//   - permintaan punya user_id dan tokennya cocok -> diteruskan, Principal di ctx;
//   - permintaan punya user_id tetapi token tidak ada atau tidak sah ->
//     Unauthenticated; ada tetapi milik orang lain -> PermissionDenied;
//   - permintaan tidak punya user_id -> RPC publik, diteruskan (token yang
//     ikut terkirim tetap diverifikasi bila ada, supaya token palsu tidak
//     "lolos" hanya karena RPC-nya publik).
//
// Health dan reflection gRPC dilewati: keduanya bukan data pengguna.
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
				// 403, bukan 404: ini bukan "sumber daya tidak ada" (S9), ini
				// pemanggil yang mengaku sebagai orang lain. Yang salah adalah
				// pemanggilnya, dan ia berhak tahu.
				return nil, status.Error(codes.PermissionDenied, "the access token does not belong to this user")
			}
		}
		return handler(ctx, req)
	}
}

// UnaryClientInterceptor meneruskan token ke hilir: dari WithToken (gateway)
// atau dari metadata masuk (service yang memanggil service lain atas nama
// pengguna yang sama). Tanpa token, permintaan dikirim apa adanya - RPC
// publik dan panggilan internal tanpa pengguna tetap bisa berjalan.
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

// userIDOf membaca bidang user_id lewat refleksi protobuf, supaya satu
// interceptor menegakkan aturan yang sama di 60-an RPC tanpa satu pun
// handler yang harus ingat memanggilnya.
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
