// Package httperr translates errors from the services into HTTP answers.
package httperr

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Body is the error shape the REST contract promises.
type Body struct {
	Success bool                `json:"success"`
	Message string              `json:"message"`
	Code    string              `json:"code,omitempty"`
	Errors  map[string][]string `json:"errors,omitempty"`
}

// The codes allowed out. The list is closed and matches the enum in
// edge-v1.yaml: a code not in there is a contract silently broken, and the
// client has no way to handle it.
const (
	CodeInvalidArgument    = "INVALID_ARGUMENT"
	CodeUnauthenticated    = "UNAUTHENTICATED"
	CodeNotFound           = "NOT_FOUND"
	CodeAlreadyExists      = "ALREADY_EXISTS"
	CodeFailedPrecondition = "FAILED_PRECONDITION"

	// CodePermissionDenied is used when the caller IS authenticated but still
	// may not do this - for instance a wrong password when confirming an
	// account deletion.
	//
	// That path used to use FAILED_PRECONDITION, and that was wrong: a client
	// that tells errors apart by code would treat it as "state not ready"
	// instead of "what you typed is wrong", and then retry with exactly the
	// same input.
	CodePermissionDenied = "PERMISSION_DENIED"
	CodeRateLimited      = "RATE_LIMITED"
	CodeInternal         = "INTERNAL"
	CodeUnavailable      = "UNAVAILABLE"
)

// Write sends the error answer and stops the handler chain.
//
// Abort is called, not just a write: without it, the next middleware or
// handler keeps running and can write a second body to the same connection.
func Write(c *gin.Context, statusCode int, code, message string) {
	c.AbortWithStatusJSON(statusCode, Body{
		Success: false,
		Message: message,
		Code:    code,
	})
}

// WriteValidation sends 422 with per-field errors, the shape the frontend
// already uses today.
func WriteValidation(c *gin.Context, fields map[string][]string) {
	c.AbortWithStatusJSON(http.StatusUnprocessableEntity, Body{
		Success: false,
		Message: "The given data was invalid.",
		Code:    CodeInvalidArgument,
		Errors:  fields,
	})
}

// FromGRPC translates errors from the services behind the gateway.
//
// Messages from internal services are NOT passed through as they are for
// unrecognised error classes. Internal messages carry table names, query
// fragments, and host addresses - all useful to someone mapping this system,
// and none of it useful to a legitimate client.
//
// For recognised classes, the message was written to be read by the caller on
// the service side, so it is passed on.
func FromGRPC(c *gin.Context, err error) {
	st, ok := status.FromError(err)
	if !ok {
		writeUnexpected(c, err)
		return
	}

	switch st.Code() {
	case codes.InvalidArgument:
		Write(c, http.StatusUnprocessableEntity, CodeInvalidArgument, st.Message())

	// Unauthenticated is always one message, without further detail. Telling
	// "email not registered" from "wrong password" here would undo the
	// uniformity identity-svc worked for.
	case codes.Unauthenticated:
		Write(c, http.StatusUnauthorized, CodeUnauthenticated, "Unauthenticated.")

	case codes.PermissionDenied:
		Write(c, http.StatusForbidden, CodePermissionDenied, st.Message())

	case codes.NotFound:
		Write(c, http.StatusNotFound, CodeNotFound, st.Message())

	case codes.AlreadyExists:
		Write(c, http.StatusConflict, CodeAlreadyExists, st.Message())

	case codes.FailedPrecondition:
		Write(c, http.StatusConflict, CodeFailedPrecondition, st.Message())

	case codes.ResourceExhausted:
		Write(c, http.StatusTooManyRequests, CodeRateLimited, "Too many requests.")

	// Unimplemented is a capability that simply does not exist yet, not a
	// client mistake. 501 says exactly that, and the message may pass because
	// it was written for a human trying to use it.
	case codes.Unimplemented:
		Write(c, http.StatusNotImplemented, CodeUnavailable, st.Message())

	case codes.Unavailable:
		Write(c, http.StatusServiceUnavailable, CodeUnavailable, "The service is temporarily unavailable.")

	case codes.DeadlineExceeded:
		Write(c, http.StatusGatewayTimeout, CodeUnavailable, "The request took too long.")

	// A caller that went away is not an error worth reporting to anyone.
	// Writing an answer to a connection that is already closed only adds noise
	// to the log without helping anyone.
	case codes.Canceled:
		c.Abort()

	default:
		writeUnexpected(c, err)
	}
}

func writeUnexpected(c *gin.Context, err error) {
	if errors.Is(err, context.Canceled) {
		c.Abort()
		return
	}

	slog.ErrorContext(c.Request.Context(), "unhandled upstream error",
		"path", c.FullPath(), "method", c.Request.Method, "error", err)
	Write(c, http.StatusInternalServerError, CodeInternal, "Internal server error.")
}
