// Package service implements the edge.v1 public contract on top of the gRPC
// services behind the gateway (ADR-027).
//
// Every handler here follows the same three rules:
//
//  1. The user is taken from the verified claims, never from the request
//     (ADR-023). No request message of edge.v1 even has a user_id field.
//  2. Every upstream error goes through rpcerr.FromUpstream, so internal
//     messages never reach the client for classes that could leak them.
//  3. Validation that the services do not do themselves happens here and
//     answers with named field violations.
package service

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "github.com/muhananaufal/selaras-platform-go/gen/common/v1"
	edgev1 "github.com/muhananaufal/selaras-platform-go/gen/edge/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/edge/interceptor"
	"github.com/muhananaufal/selaras-platform-go/internal/edge/rpcerr"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
)

// claims takes the verified claims. Their absence can only mean a procedure
// was mounted without the Authenticator, and that must fail loudly - never
// run as the user with an empty id.
func claims(ctx context.Context) (domain.Claims, error) {
	c, ok := interceptor.ClaimsFrom(ctx)
	if !ok {
		return domain.Claims{}, connect.NewError(connect.CodeInternal, interceptor.ErrNoClaims)
	}
	return c, nil
}

// idempotencyHeader returns the Idempotency-Key header of the call in ctx.
func idempotencyHeader(ctx context.Context) string {
	info, ok := connect.CallInfoForHandlerContext(ctx)
	if !ok {
		return ""
	}
	return info.RequestHeader().Get("Idempotency-Key")
}

// idempotencyKey binds the client's Idempotency-Key header to the user who
// sent it.
//
// The key is used as the job key downstream (llm-worker, single scope).
// Without this binding, two users who happen - or choose - to use the same
// key cancel each other out. The separator is a control character so a user
// id and a client key cannot be strung together into someone else's key.
//
// nil when the client sends no key: the use case derives its own.
func idempotencyKey(ctx context.Context, c domain.Claims) *commonv1.IdempotencyKey {
	return keyFor(c, idempotencyHeader(ctx))
}

func keyFor(c domain.Claims, header string) *commonv1.IdempotencyKey {
	raw := strings.TrimSpace(header)
	if raw == "" {
		return nil
	}
	return &commonv1.IdempotencyKey{Value: c.UserID.String() + "\x1f" + raw}
}

// jsonValue turns stored JSON into a google.protobuf.Value.
//
// Invalid or empty JSON yields nil - the field is absent - rather than an
// error: one corrupt row must not make the whole response unreadable. The
// check happens before the conversion so the answer is the same whatever
// structpb would have done with it.
func jsonValue(raw string) *structpb.Value {
	if raw == "" || !json.Valid([]byte(raw)) {
		return nil
	}
	v := &structpb.Value{}
	if err := v.UnmarshalJSON([]byte(raw)); err != nil {
		return nil
	}
	return v
}

func ts(t *timestamppb.Timestamp) *timestamppb.Timestamp {
	if t == nil || !t.IsValid() {
		return nil
	}
	return t
}

func pageFrom(p *edgev1.PageRequest) *commonv1.PageRequest {
	if p == nil {
		return &commonv1.PageRequest{}
	}
	size := p.GetPageSize()
	// Zero means "the service default"; a negative size is read the same way
	// rather than refused - one odd parameter must not stop a list working.
	if size < 0 {
		size = 0
	}
	return &commonv1.PageRequest{PageSize: size, PageToken: p.GetPageToken()}
}

func pageOut(p *commonv1.PageResponse) *edgev1.PageResponse {
	return &edgev1.PageResponse{NextPageToken: p.GetNextPageToken()}
}

// msgRequired is the description of every missing-field violation.
const msgRequired = "This field is required."

// required refuses empty strings, naming each field.
func required(fields ...[2]string) []rpcerr.FieldViolation {
	var out []rpcerr.FieldViolation
	for _, f := range fields {
		if strings.TrimSpace(f[1]) == "" {
			out = append(out, rpcerr.FieldViolation{Field: f[0], Description: msgRequired})
		}
	}
	return out
}

// field pairs a JSON field name with its value for required().
func field(name, value string) [2]string { return [2]string{name, value} }

func invalid(violations []rpcerr.FieldViolation) error {
	if len(violations) == 0 {
		return nil
	}
	return rpcerr.Invalid(violations...)
}

// ageOn computes an age from an ISO-8601 date. The second value is false when
// the date cannot be parsed: zero is a possible age, so it cannot mark failure
// (finding B6).
func ageOn(iso string, on time.Time) (int32, bool) {
	born, err := time.Parse(time.DateOnly, iso)
	if err != nil {
		return 0, false
	}
	age := on.Year() - born.Year()
	if on.YearDay() < born.YearDay() {
		age--
	}
	return int32(age), true //nolint:gosec // a human age fits in int32
}
