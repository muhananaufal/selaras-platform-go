package middleware

import (
	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"
)

// Tracing membuka satu span server per permintaan HTTP dan membaca
// traceparent yang dibawa klien.
//
// Span-nya menjadi akar trace untuk hampir semua permintaan - klien web tidak
// mengirim traceparent - dan dari sinilah trace mengalir ke service lewat gRPC
// dan ke worker lewat Envelope (F9-05, F9-07).
//
// Probe kesehatan tidak direkam. Ia datang setiap beberapa detik dari setiap
// replika, dan merekamnya berarti sebagian besar span yang tersimpan adalah
// span yang tidak pernah dicari siapa pun.
func Tracing(service string) gin.HandlerFunc {
	return otelgin.Middleware(service, otelgin.WithGinFilter(func(c *gin.Context) bool {
		path := c.Request.URL.Path
		return path != "/healthz" && path != "/readyz"
	}))
}
