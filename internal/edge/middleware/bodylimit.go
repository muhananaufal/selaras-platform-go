package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/muhananaufal/selaras-platform-go/internal/edge/httperr"
)

// MaxBodyBytes is the request body size limit.
//
// One megabyte. The largest legitimate body in this API is the risk assessment
// questionnaire - a few dozen fields - and a chat message bounded to 16 KiB in
// its domain. One megabyte leaves many times that headroom above both while
// still stopping the unreasonable.
//
// Without a limit, one request could force the gateway to read the whole body
// into memory before any validation had a chance to run - and the cheapest way
// to take a service down is to send it something very large.
const MaxBodyBytes int64 = 1 << 20

// LimitBody refuses request bodies that are too large.
//
// It installs http.MaxBytesReader, which stops reading MIDWAY instead of
// reading everything and then measuring. Reading first and measuring afterwards
// protects nothing: the memory is already spent by the time the size is known.
//
// A Content-Length declaring a large size is refused earlier still, without
// reading a single byte. That header is not trusted as the truth - the bounded
// reader below still guards against a request that lies - but refusing the
// honest ones early saves work.
func LimitBody() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Body == nil {
			c.Next()
			return
		}

		if c.Request.ContentLength > MaxBodyBytes {
			tooLarge(c)
			return
		}

		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, MaxBodyBytes)
		c.Next()
	}
}

// tooLarge answers with the same error shape as every other refusal.
//
// The uniform shape is the point: a client handling errors through the `code`
// field must not find one endpoint answering in a different shape, because
// branching error handling always has a branch that is never tested.
func tooLarge(c *gin.Context) {
	httperr.Write(c, http.StatusRequestEntityTooLarge, httperr.CodeInvalidArgument,
		"The request body is too large.")
	c.Abort()
}
