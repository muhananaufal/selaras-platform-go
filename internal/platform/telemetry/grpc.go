package telemetry

import (
	"strings"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/stats"
)

// GRPCServerOption installs instrumentation on a gRPC server.
//
// One and the same option for all seven services, so no service ends up with
// differently shaped spans - or none at all - because it assembled its own.
func GRPCServerOption() grpc.ServerOption {
	return grpc.StatsHandler(otelgrpc.NewServerHandler(
		otelgrpc.WithFilter(notPlumbing),
	))
}

// GRPCDialOption installs instrumentation on a gRPC client, and with it
// traceparent propagation to the service being called.
func GRPCDialOption() grpc.DialOption {
	return grpc.WithStatsHandler(otelgrpc.NewClientHandler(
		otelgrpc.WithFilter(notPlumbing),
	))
}

// notPlumbing filters out calls that do not belong to a user.
//
// Health probes arrive every few seconds from every replica, and reflection
// is only used by grpcurl. Recording them would mean most stored spans are
// spans nobody ever looks for, and the ones people do look for would have
// to be found among them.
func notPlumbing(info *stats.RPCTagInfo) bool {
	return !strings.HasPrefix(info.FullMethodName, "/grpc.health.v1.") &&
		!strings.HasPrefix(info.FullMethodName, "/grpc.reflection.")
}
