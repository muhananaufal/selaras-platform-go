package middleware

import (
	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"
)

// Tracing opens one server span per HTTP request and reads the traceparent the
// client carries.
//
// Its span becomes the trace root for almost every request - web clients do
// not send traceparent - and from here the trace flows to the services over
// gRPC and to the workers through Envelope (F9-05, F9-07).
//
// Health probes are not recorded. They arrive every few seconds from every
// replica, and recording them would mean most stored spans are spans nobody
// ever looks for.
func Tracing(service string) gin.HandlerFunc {
	return otelgin.Middleware(service, otelgin.WithGinFilter(func(c *gin.Context) bool {
		path := c.Request.URL.Path
		return path != "/healthz" && path != "/readyz"
	}))
}
