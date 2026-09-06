// Package rpc menampung opsi klien gRPC yang dipakai lintas unit.
package rpc

import (
	"context"
	"time"

	"google.golang.org/grpc"
)

// DefaultUpstreamTimeout adalah batas waktu satu panggilan ke service lain
// bila pemanggilnya tidak menetapkan sendiri.
//
// Lima detik: sepuluh kali RPC terlama yang pernah diukur (Register dengan
// argon2id, p99 di bawah setengah detik; laporan kinerja F9-10), dan masih
// di bawah batas yang membuat pengguna menganggap aplikasinya mati. Chaos
// F9-13 dengan sepuluh detik menunjukkan panggilan PERTAMA ke service yang
// baru mati menunggu penuh sampai tenggat - klien gRPC masih mencoba
// menyambung - jadi angka ini adalah berapa lama pengguna pertama menunggu
// sebelum 504, dan sepuluh terlalu lama untuk itu.
const DefaultUpstreamTimeout = 5 * time.Second

// WithUpstreamDeadline membatasi setiap panggilan unary yang belum punya
// batas waktu.
//
// Ini lahir dari chaos F9-13: saat profile-svc dimatikan, GET /profile di
// gateway TIDAK menjawab 503 - ia menggantung sampai klien menyerah. Klien
// gRPC yang sedang menyambung ulang menahan RPC selama ia mencoba, dan tanpa
// batas waktu dari pemanggil, "mencoba" itu tidak berujung. Batas di sini
// membuat kegagalan itu menjadi DeadlineExceeded yang dipetakan gateway ke
// 504, dalam waktu yang bisa dijelaskan kepada siapa pun.
//
// Batas waktu yang SUDAH ada di ctx dihormati: pemanggil yang tahu lebih
// baik - pekerjaan latar dengan tenggat sendiri - tidak ditimpa.
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
