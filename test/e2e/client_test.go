package e2e_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	edgev1 "github.com/muhananaufal/selaras-platform-go/gen/edge/v1"
	"github.com/muhananaufal/selaras-platform-go/gen/edge/v1/edgev1connect"
)

// These tests drive the RUNNING system through the public edge.v1 contract -
// the gateway, the services, a broker, and a real database.
//
// They are no substitute for the per-package integration tests: what is proven
// here is what no single package can prove on its own - that a request entering
// through the gateway really becomes a job, and that its result really comes
// back to where it is awaited.
//
// The clients are the GENERATED Connect clients, the same code a Go consumer
// would use. A change to the contract that breaks a consumer therefore breaks
// this suite at compile time, not at run time.
//
// Without TEST_E2E_BASE_URL they skip themselves; in CI they FAIL instead of
// skipping, because a test that silently skips itself in CI turns the pipeline
// green without checking anything.

func baseURL(t *testing.T) string {
	t.Helper()

	url := os.Getenv("TEST_E2E_BASE_URL")
	if url == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("TEST_E2E_BASE_URL is not set; end-to-end tests must not be skipped in CI")
		}
		t.Skip("TEST_E2E_BASE_URL is not set; start the stack with 'task up:apps' to run this test")
	}
	return url
}

// defaultPassword is used by every account register() creates. A constant,
// because the deletion test must send the SAME password to confirm.
const defaultPassword = "correct-horse-battery"

// client is one user talking to the gateway.
type client struct {
	t     *testing.T
	base  string
	http  *http.Client
	token string
	email string

	auth       edgev1connect.AuthClient
	profile    edgev1connect.ProfileClient
	assessment edgev1connect.AssessmentClient
	coaching   edgev1connect.CoachingClient
	chat       edgev1connect.ChatClient
	nutrition  edgev1connect.NutritionClient
	dashboard  edgev1connect.DashboardClient
	clinic     edgev1connect.ClinicClient
}

func newClient(t *testing.T) *client {
	t.Helper()

	c := &client{
		t:    t,
		base: baseURL(t),
		// A timeout on the client, not only on ctx: a test that hangs because
		// one service is silent holds the whole suite until the package
		// timeout, and the message does not say which request.
		http: &http.Client{Timeout: 30 * time.Second},
	}

	// JSON over the Connect protocol - the same wire the browser uses - and
	// GET for reads, so the path the frontend will take is the path tested.
	opts := []connect.ClientOption{
		connect.WithProtoJSON(),
		connect.WithHTTPGet(),
		connect.WithInterceptors(bearer{c}),
	}
	c.auth = edgev1connect.NewAuthClient(c.http, c.base, opts...)
	c.profile = edgev1connect.NewProfileClient(c.http, c.base, opts...)
	c.assessment = edgev1connect.NewAssessmentClient(c.http, c.base, opts...)
	c.coaching = edgev1connect.NewCoachingClient(c.http, c.base, opts...)
	c.chat = edgev1connect.NewChatClient(c.http, c.base, opts...)
	c.nutrition = edgev1connect.NewNutritionClient(c.http, c.base, opts...)
	c.dashboard = edgev1connect.NewDashboardClient(c.http, c.base, opts...)
	c.clinic = edgev1connect.NewClinicClient(c.http, c.base, opts...)
	return c
}

// ctx is the test's context, bounded so a silent service fails the test with
// a clear message instead of holding the suite.
func (c *client) ctx() context.Context {
	ctx, cancel := context.WithTimeout(c.t.Context(), 30*time.Second)
	c.t.Cleanup(cancel)
	return ctx
}

// register creates a new account and keeps its token.
func (c *client) register() {
	c.t.Helper()

	email := fmt.Sprintf("e2e-%d-%s@user.co", time.Now().UnixNano(), sanitize(c.t.Name()))
	resp, err := c.auth.Register(c.ctx(), &edgev1.RegisterRequest{
		Email:                email,
		Password:             defaultPassword,
		PasswordConfirmation: defaultPassword,
	})
	if err != nil {
		c.t.Fatalf("register: %v", err)
	}
	if resp.GetSession().GetAccessToken() == "" {
		c.t.Fatalf("register returned no access token: %v", resp)
	}
	c.token = resp.GetSession().GetAccessToken()
	c.email = email
}

// anonymous runs fn without the token, then restores it.
func (c *client) anonymous(fn func()) {
	saved := c.token
	c.token = ""
	defer func() { c.token = saved }()
	fn()
}

// codeOf is the Connect code of an error; CodeUnknown for a non-Connect error
// and 0 for nil, so an unexpected success reads plainly in a failure message.
func codeOf(err error) connect.Code {
	if err == nil {
		return 0
	}
	var cerr *connect.Error
	if errors.As(err, &cerr) {
		return cerr.Code()
	}
	return connect.CodeUnknown
}

// expectCode fails the test unless err carries want.
func expectCode(t *testing.T, what string, err error, want connect.Code) {
	t.Helper()
	if got := codeOf(err); got != want {
		t.Fatalf("%s: got %v (%v), want %v", what, got, err, want)
	}
}

// sanitize keeps a test name usable inside an email address.
func sanitize(name string) string {
	out := make([]rune, 0, len(name))
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
			out = append(out, r)
		default:
			out = append(out, '-')
		}
	}
	return string(out)
}

// bearer adds the client's current token to every call.
type bearer struct{ c *client }

func (b bearer) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if b.c.token != "" {
			req.Header().Set("Authorization", "Bearer "+b.c.token)
		}
		return next(ctx, req)
	}
}

func (b bearer) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn {
		conn := next(ctx, spec)
		if b.c.token != "" {
			conn.RequestHeader().Set("Authorization", "Bearer "+b.c.token)
		}
		return conn
	}
}

func (b bearer) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

// contextUntil is a context that ends at deadline, for one stream.
func contextUntil(t *testing.T, deadline time.Time) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithDeadline(t.Context(), deadline)
}

// raw sends a JSON body straight to one procedure over the Connect protocol,
// for tests that must send what a typed client cannot: an unknown enum name,
// a misspelled field, a legacy label. It returns the HTTP status and the
// Connect error code ("" on success).
func (c *client) raw(procedure string, body string) (int, string) {
	c.t.Helper()

	req, err := http.NewRequestWithContext(c.ctx(), http.MethodPost, c.base+procedure, strings.NewReader(body))
	if err != nil {
		c.t.Fatalf("building the request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatalf("%s: %v", procedure, err)
	}
	defer func() { _ = resp.Body.Close() }()

	var decoded struct {
		Code string `json:"code"`
	}
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		c.t.Fatalf("reading %s: %v", procedure, err)
	}
	if resp.StatusCode != http.StatusOK {
		if err := json.Unmarshal(payload, &decoded); err != nil {
			c.t.Fatalf("%s answered %d with something that is not a Connect error: %s", procedure, resp.StatusCode, payload)
		}
	}
	return resp.StatusCode, decoded.Code
}
