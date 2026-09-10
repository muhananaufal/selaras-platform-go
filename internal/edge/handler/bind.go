package handler

import (
	"errors"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"

	"github.com/muhananaufal/selaras-platform-go/internal/edge/httperr"
)

// bind parses the JSON body and answers 422 if it is malformed.
//
// Its return value is a boolean, not an error, because the answer has already
// been sent: every caller only needs to know whether it may continue. That
// shape makes the "write the error and forget to return" pattern impossible to
// write.
func bind(c *gin.Context, target any) bool {
	if err := c.ShouldBindJSON(target); err != nil {
		httperr.WriteValidation(c, fieldErrors(err))
		return false
	}
	return true
}

// fieldErrors translates validator errors into the per-field shape the
// frontend already uses today.
//
// Errors that are not from the validator - broken JSON, say - have no field
// to point at, and their message is NOT passed on: JSON parser messages
// carry byte offsets and Go type names, which are useless to a client and
// leak the internal shape.
func fieldErrors(err error) map[string][]string {
	var invalid validator.ValidationErrors
	if !errors.As(err, &invalid) {
		return map[string][]string{
			"body": {"The request body could not be read."},
		}
	}

	fields := make(map[string][]string, len(invalid))
	for _, fieldErr := range invalid {
		name := jsonName(fieldErr.Field())
		fields[name] = append(fields[name], messageFor(fieldErr))
	}
	return fields
}

// jsonName turns a Go field name into the name the client sends.
//
// The client sends snake_case and never sees the Go name; an error naming
// "PasswordConfirmation" forces its reader to guess which field is meant.
func jsonName(field string) string {
	var out strings.Builder
	for i, r := range field {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				out.WriteByte('_')
			}
			out.WriteRune(r + ('a' - 'A'))
			continue
		}
		out.WriteRune(r)
	}
	return out.String()
}

func messageFor(err validator.FieldError) string {
	switch err.Tag() {
	case "required":
		return "This field is required."
	case "email":
		return "This must be a valid email address."
	case "min":
		return "This must be at least " + err.Param() + " characters."
	case "max":
		return "This must not exceed " + err.Param() + " characters."
	default:
		return "This value is invalid."
	}
}

// bearer takes the token from the Authorization header.
//
// It assumes the header has already passed the authentication middleware, so
// it does not re-validate - all it needs is the value to pass on.
func bearer(header string) string {
	_, value, found := strings.Cut(strings.TrimSpace(header), " ")
	if !found {
		return ""
	}
	return strings.TrimSpace(value)
}
