package telemetry

import (
	"strings"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/stats"
)

// GRPCServerOption memasang instrumentasi pada server gRPC.
//
// Satu opsi yang sama untuk ketujuh service, sehingga tidak ada service yang
// span-nya berbentuk lain - atau tidak ada sama sekali - karena disusun
// sendiri-sendiri.
func GRPCServerOption() grpc.ServerOption {
	return grpc.StatsHandler(otelgrpc.NewServerHandler(
		otelgrpc.WithFilter(notPlumbing),
	))
}

// GRPCDialOption memasang instrumentasi pada klien gRPC, dan bersamanya
// propagasi traceparent ke service yang dipanggil.
func GRPCDialOption() grpc.DialOption {
	return grpc.WithStatsHandler(otelgrpc.NewClientHandler(
		otelgrpc.WithFilter(notPlumbing),
	))
}

// notPlumbing menyaring panggilan yang bukan milik pengguna.
//
// Probe kesehatan datang setiap beberapa detik dari setiap replika, dan
// reflection hanya dipakai grpcurl. Merekamnya berarti sebagian besar span
// yang tersimpan adalah span yang tidak pernah dicari siapa pun, dan yang
// dicari harus ditemukan di antaranya.
func notPlumbing(info *stats.RPCTagInfo) bool {
	return !strings.HasPrefix(info.FullMethodName, "/grpc.health.v1.") &&
		!strings.HasPrefix(info.FullMethodName, "/grpc.reflection.")
}
