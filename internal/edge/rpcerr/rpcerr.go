// Package rpcerr translates failures into Connect errors for the public
// contract (ADR-027).
//
// Every refusal the gateway sends goes through here, so the error shape a
// client sees is the same whether the refusal came from validation, the rate
// limiter, authentication, or a service behind the gateway. Branching error
// handling always has a branch that is never tested.
package rpcerr

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
)

// Messages that are fixed on purpose. They are the whole answer for their
// class, never extended with detail from below.
const (
	msgUnauthenticated = "unauthenticated"
	msgInvalid         = "the given data was invalid"
	msgRateLimited     = "too many requests, try again in a moment"
	msgUnavailable     = "the service is temporarily unavailable"
	msgDeadline        = "the request took too long"
	msgInternal        = "internal server error"
)

// FieldViolation names one invalid field and why.
//
// Field uses the JSON name the client sent (camelCase), not the proto name,
// because that is the name the client can find in its own code.
type FieldViolation struct {
	Field       string
	Description string
}

// Invalid refuses a request with per-field reasons.
//
// The reasons travel as a google.rpc.BadRequest detail, the standard carrier
// for field violations in both gRPC and Connect, so a generated client can
// read them without parsing the message.
func Invalid(violations ...FieldViolation) *connect.Error {
	err := connect.NewError(connect.CodeInvalidArgument, errors.New(msgInvalid))

	bad := &errdetails.BadRequest{}
	for _, v := range violations {
		bad.FieldViolations = append(bad.FieldViolations, &errdetails.BadRequest_FieldViolation{
			Field:       v.Field,
			Description: v.Description,
		})
	}
	if detail, detailErr := connect.NewErrorDetail(bad); detailErr == nil {
		err.AddDetail(detail)
	}
	return err
}

// Unauthenticated is one message, always, without the reason.
//
// Telling "bad signature" from "expired" or "revoked" tells an attacker which
// part of a forged token was right.
func Unauthenticated() *connect.Error {
	return connect.NewError(connect.CodeUnauthenticated, errors.New(msgUnauthenticated))
}

// Unavailable says the gateway itself cannot answer right now.
//
// It is used where the client did nothing wrong - the revocation check
// failing, for instance. Unauthenticated there would sign a user out over a
// momentary hiccup on our side.
func Unavailable(message string) *connect.Error {
	return connect.NewError(connect.CodeUnavailable, errors.New(message))
}

// RateLimited refuses a request that exceeded its limit.
//
// resource_exhausted is the Connect code for this (HTTP 429). The wait is
// sent twice: as a RetryInfo detail for generated clients, and as the
// Retry-After header for everything else.
func RateLimited(retryAfter time.Duration) *connect.Error {
	err := connect.NewError(connect.CodeResourceExhausted, errors.New(msgRateLimited))

	// Seconds, rounded up: a client retrying exactly at the window boundary
	// would be refused again.
	seconds := int64((retryAfter + time.Second - 1) / time.Second)
	err.Meta().Set("Retry-After", strconv.FormatInt(seconds, 10))

	if detail, detailErr := connect.NewErrorDetail(&errdetails.RetryInfo{
		RetryDelay: durationpb.New(time.Duration(seconds) * time.Second),
	}); detailErr == nil {
		err.AddDetail(detail)
	}
	return err
}

// FromUpstream translates an error from a service behind the gateway.
//
// Messages from internal services are passed on ONLY for classes whose
// message was written for the caller (invalid argument, not found, and the
// like). For every other class the message is replaced: internal messages
// carry table names, query fragments, and host addresses - all useful to
// someone mapping this system, none of it useful to a legitimate client.
func FromUpstream(ctx context.Context, procedure string, err error) *connect.Error {
	if err == nil {
		return nil
	}

	// A caller that went away is not an error worth reporting to anyone.
	if errors.Is(err, context.Canceled) {
		return connect.NewError(connect.CodeCanceled, err)
	}

	st, ok := status.FromError(err)
	if !ok {
		return unexpected(ctx, procedure, err)
	}

	switch st.Code() {
	case codes.InvalidArgument:
		return connect.NewError(connect.CodeInvalidArgument, errors.New(st.Message()))

	// One message, whatever the reason. Telling "email not registered" from
	// "wrong password" here would undo the uniformity identity-svc worked for.
	case codes.Unauthenticated:
		return Unauthenticated()

	case codes.PermissionDenied:
		return connect.NewError(connect.CodePermissionDenied, errors.New(st.Message()))
	case codes.NotFound:
		return connect.NewError(connect.CodeNotFound, errors.New(st.Message()))
	case codes.AlreadyExists:
		return connect.NewError(connect.CodeAlreadyExists, errors.New(st.Message()))
	case codes.FailedPrecondition:
		return connect.NewError(connect.CodeFailedPrecondition, errors.New(st.Message()))
	case codes.ResourceExhausted:
		return connect.NewError(connect.CodeResourceExhausted, errors.New(msgRateLimited))

	// A capability that does not exist yet, not a client mistake. The message
	// was written for a human trying to use it.
	case codes.Unimplemented:
		return connect.NewError(connect.CodeUnimplemented, errors.New(st.Message()))

	case codes.Unavailable:
		return Unavailable(msgUnavailable)
	case codes.DeadlineExceeded:
		return connect.NewError(connect.CodeDeadlineExceeded, errors.New(msgDeadline))
	case codes.Canceled:
		return connect.NewError(connect.CodeCanceled, errors.New("canceled"))
	default:
		return unexpected(ctx, procedure, err)
	}
}

func unexpected(ctx context.Context, procedure string, err error) *connect.Error {
	slog.ErrorContext(ctx, "unhandled upstream error", "procedure", procedure, "error", err)
	return connect.NewError(connect.CodeInternal, errors.New(msgInternal))
}
