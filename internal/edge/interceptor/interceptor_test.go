package interceptor_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/genproto/googleapis/rpc/errdetails"

	edgev1 "github.com/muhananaufal/selaras-platform-go/gen/edge/v1"
	nutritionv1 "github.com/muhananaufal/selaras-platform-go/gen/nutrition/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/edge/interceptor"
	"github.com/muhananaufal/selaras-platform-go/internal/edge/wire"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/redis/redistest"
)

// ---------------------------------------------------------------- client IP

func TestClientIPIgnoresForwardedForFromAnUntrustedPeer(t *testing.T) {
	proxies, err := interceptor.ParseTrustedProxies("")
	if err != nil {
		t.Fatal(err)
	}
	h := http.Header{}
	h.Set("X-Forwarded-For", "198.51.100.1")

	if got := proxies.ClientIP("203.0.113.7:40000", h); got != "203.0.113.7" {
		t.Fatalf("got %q, want the peer 203.0.113.7: an untrusted peer's header proves nothing", got)
	}
}

func TestClientIPReadsForwardedForFromTheRightThroughTrustedHops(t *testing.T) {
	proxies, err := interceptor.ParseTrustedProxies("10.0.0.0/8, 192.168.0.0/16")
	if err != nil {
		t.Fatal(err)
	}
	h := http.Header{}
	// The client wrote the first entry itself; 198.51.100.9 is what our
	// outer proxy saw; 10.1.2.3 is our inner proxy.
	h.Set("X-Forwarded-For", "1.2.3.4, 198.51.100.9, 10.1.2.3")

	if got := proxies.ClientIP("192.168.1.10:5000", h); got != "198.51.100.9" {
		t.Fatalf("got %q, want 198.51.100.9 (the first untrusted hop from the right)", got)
	}
}

func TestParseTrustedProxiesRefusesAMalformedEntry(t *testing.T) {
	if _, err := interceptor.ParseTrustedProxies("10.0.0.0/8, 10.0.0.300/8"); err == nil {
		t.Fatal("a malformed CIDR was accepted; a typo would silently drop the proxy range")
	}
}

// ---------------------------------------------------------------- auth

type fakeVerifier struct{ claims map[string]domain.Claims }

func (f fakeVerifier) Verify(raw string) (domain.Claims, error) {
	c, ok := f.claims[raw]
	if !ok {
		return domain.Claims{}, errors.New("bad token")
	}
	return c, nil
}

type fakeRevocations struct {
	current bool
	err     error
}

func (f fakeRevocations) IsCurrent(context.Context, domain.UserID, int64) (bool, error) {
	return f.current, f.err
}

const (
	procMe    = "/edge.v1.Auth/GetMe"
	procLogin = "/edge.v1.Auth/Login"
)

// server mounts two procedures: GetMe (protected) and Login (public), both
// answering with whatever user the context carries.
func server(t *testing.T, interceptors ...connect.Interceptor) *httptest.Server {
	t.Helper()
	opts := append([]connect.HandlerOption{connect.WithInterceptors(interceptors...)}, wire.Codecs()...)

	mux := http.NewServeMux()
	mux.Handle(procMe, connect.NewUnaryHandlerSimple(procMe,
		func(ctx context.Context, _ *edgev1.GetMeRequest) (*edgev1.GetMeResponse, error) {
			claims, ok := interceptor.ClaimsFrom(ctx)
			if !ok {
				return nil, connect.NewError(connect.CodeInternal, interceptor.ErrNoClaims)
			}
			return &edgev1.GetMeResponse{UserId: claims.UserID.String()}, nil
		}, opts...))
	mux.Handle(procLogin, connect.NewUnaryHandlerSimple(procLogin,
		func(context.Context, *edgev1.LoginRequest) (*edgev1.LoginResponse, error) {
			return &edgev1.LoginResponse{}, nil
		}, opts...))
	mux.Handle("/edge.v1.Nutrition/GenerateDailyGuide", connect.NewUnaryHandlerSimple(
		"/edge.v1.Nutrition/GenerateDailyGuide",
		func(context.Context, *edgev1.GenerateDailyGuideRequest) (*edgev1.GenerateDailyGuideResponse, error) {
			return &edgev1.GenerateDailyGuideResponse{GuideId: "g"}, nil
		}, opts...))

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func authenticator(t *testing.T, rev fakeRevocations) *interceptor.Authenticator {
	t.Helper()
	uid, err := domain.ParseUserID("0192f1c0-0000-7000-8000-000000000001")
	if err != nil {
		t.Fatal(err)
	}
	a, err := interceptor.NewAuthenticator(
		fakeVerifier{claims: map[string]domain.Claims{"good": {UserID: uid}}},
		rev, procLogin)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func call(t *testing.T, url, procedure, token, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url+procedure, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

func TestProtectedProcedureRefusesAMissingToken(t *testing.T) {
	srv := server(t, authenticator(t, fakeRevocations{current: true}))

	code, body := call(t, srv.URL, procMe, "", "{}")
	if code != http.StatusUnauthorized || !strings.Contains(body, `"unauthenticated"`) {
		t.Fatalf("got %d %s, want 401 unauthenticated", code, body)
	}
}

func TestProtectedProcedureRefusesAnUnverifiableToken(t *testing.T) {
	srv := server(t, authenticator(t, fakeRevocations{current: true}))

	code, _ := call(t, srv.URL, procMe, "forged", "{}")
	if code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", code)
	}
}

func TestProtectedProcedureRefusesARevokedToken(t *testing.T) {
	srv := server(t, authenticator(t, fakeRevocations{current: false}))

	code, _ := call(t, srv.URL, procMe, "good", "{}")
	if code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401 for a revoked generation", code)
	}
}

// ADR-020: when revocation cannot be confirmed, refuse - and say it is our
// fault (503), not the user's (401).
func TestRevocationOutageFailsClosedWithUnavailable(t *testing.T) {
	srv := server(t, authenticator(t, fakeRevocations{err: errors.New("redis down")}))

	code, body := call(t, srv.URL, procMe, "good", "{}")
	if code != http.StatusServiceUnavailable || !strings.Contains(body, `"unavailable"`) {
		t.Fatalf("got %d %s, want 503 unavailable", code, body)
	}
}

func TestValidTokenReachesTheHandlerWithItsClaims(t *testing.T) {
	srv := server(t, authenticator(t, fakeRevocations{current: true}))

	code, body := call(t, srv.URL, procMe, "good", "{}")
	if code != http.StatusOK || !strings.Contains(body, "0192f1c0-0000-7000-8000-000000000001") {
		t.Fatalf("got %d %s, want 200 with the user id from the token", code, body)
	}
}

func TestPublicProcedurePassesWithoutAToken(t *testing.T) {
	srv := server(t, authenticator(t, fakeRevocations{current: true}))

	if code, body := call(t, srv.URL, procLogin, "", "{}"); code != http.StatusOK {
		t.Fatalf("got %d %s, want 200 for a public procedure", code, body)
	}
}

// Default-deny: a procedure nobody put on the public list is protected.
func TestUnlistedProcedureIsProtectedByDefault(t *testing.T) {
	srv := server(t, authenticator(t, fakeRevocations{current: true}))

	code, _ := call(t, srv.URL, "/edge.v1.Nutrition/GenerateDailyGuide", "", "{}")
	if code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401: a procedure not listed as public must be protected", code)
	}
}

// ---------------------------------------------------------------- codec + enums

// The strict codec refuses an enum NAME it does not know. connect-go's
// default codec would silently read it as zero, and for craving_type zero
// means "no craving" - the request would be answered as something else.
func TestUnknownEnumNameIsRefusedNotReadAsNone(t *testing.T) {
	srv := server(t)

	code, body := call(t, srv.URL, "/edge.v1.Nutrition/GenerateDailyGuide", "",
		`{"input":{"cravingType":"CRAVING_TYPE_GRILLD"}}`)
	if code != http.StatusBadRequest || !strings.Contains(body, `"invalid_argument"`) {
		t.Fatalf("got %d %s, want 400 invalid_argument for a mistyped enum name", code, body)
	}
}

func TestUnknownFieldIsRefused(t *testing.T) {
	srv := server(t)

	code, _ := call(t, srv.URL, "/edge.v1.Nutrition/GenerateDailyGuide", "",
		`{"input":{"cravingTpye":"CRAVING_TYPE_GRILLED"}}`)
	if code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400 for a misspelled field", code)
	}
}

// A NUMBER passes the codec, so the enum guard has to catch it.
func TestUndefinedEnumNumberIsRefusedWithTheFieldNamed(t *testing.T) {
	srv := server(t, interceptor.EnumGuard{})

	code, body := call(t, srv.URL, "/edge.v1.Nutrition/GenerateDailyGuide", "",
		`{"input":{"cravingType":42}}`)
	if code != http.StatusBadRequest {
		t.Fatalf("got %d %s, want 400", code, body)
	}
	if !strings.Contains(body, "input.cravingType") {
		t.Fatalf("body %s does not name the field input.cravingType", body)
	}
}

func TestDefinedEnumPasses(t *testing.T) {
	srv := server(t, interceptor.EnumGuard{})

	body := `{"input":{"cravingType":"` + nutritionv1.CravingType_CRAVING_TYPE_GRILLED.String() + `"}}`
	if code, resp := call(t, srv.URL, "/edge.v1.Nutrition/GenerateDailyGuide", "", body); code != http.StatusOK {
		t.Fatalf("got %d %s, want 200", code, resp)
	}
}

// ---------------------------------------------------------------- rate limit

// Regression for the spoofed X-Forwarded-For bypass (proven red against the
// REST limiter): one client, six attempts, six invented headers, a limit of
// five. The sixth must be refused.
func TestSpoofedForwardedForDoesNotResetTheLimit(t *testing.T) {
	client := redistest.Open(t)
	proxies, _ := interceptor.ParseTrustedProxies("")
	name := "spoof" + strconv.FormatInt(time.Now().UnixNano(), 10)

	limiter, err := interceptor.NewRateLimiter(client, slog.New(slog.NewTextHandler(io.Discard, nil)), proxies,
		map[string]interceptor.Policy{procLogin: {
			Name: name, Limit: interceptor.Limit{Requests: 5, Window: time.Minute}, Subject: interceptor.ByClientIP,
		}})
	if err != nil {
		t.Fatal(err)
	}
	srv := server(t, limiter)

	var code int
	var body string
	for i := 1; i <= 6; i++ {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+procLogin, strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Forwarded-For", "198.51.100."+strconv.Itoa(i))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		code, body = resp.StatusCode, string(raw)
		if i == 6 && resp.Header.Get("Retry-After") == "" {
			t.Error("the refusal carries no Retry-After header")
		}
	}
	if code != http.StatusTooManyRequests || !strings.Contains(body, `"resource_exhausted"`) {
		t.Fatalf("sixth attempt answered %d %s, want 429 resource_exhausted", code, body)
	}
}

// The refusal carries a RetryInfo detail a generated client can read.
func TestRateLimitRefusalCarriesRetryInfo(t *testing.T) {
	client := redistest.Open(t)
	proxies, _ := interceptor.ParseTrustedProxies("")
	name := "retry" + strconv.FormatInt(time.Now().UnixNano(), 10)

	limiter, err := interceptor.NewRateLimiter(client, slog.New(slog.NewTextHandler(io.Discard, nil)), proxies,
		map[string]interceptor.Policy{procLogin: {
			Name: name, Limit: interceptor.Limit{Requests: 1, Window: time.Minute}, Subject: interceptor.ByClientIP,
		}})
	if err != nil {
		t.Fatal(err)
	}
	srv := server(t, limiter)

	rpc := connect.NewClient[edgev1.LoginRequest, edgev1.LoginResponse](http.DefaultClient, srv.URL+procLogin)
	ctx := context.Background()
	if _, err := rpc.CallUnary(ctx, connect.NewRequest(&edgev1.LoginRequest{})); err != nil {
		t.Fatalf("first call: %v", err)
	}
	_, err = rpc.CallUnary(ctx, connect.NewRequest(&edgev1.LoginRequest{}))

	var cerr *connect.Error
	if !errors.As(err, &cerr) || cerr.Code() != connect.CodeResourceExhausted {
		t.Fatalf("second call: got %v, want resource_exhausted", err)
	}
	for _, d := range cerr.Details() {
		if v, derr := d.Value(); derr == nil {
			if info, ok := v.(*errdetails.RetryInfo); ok && info.GetRetryDelay().AsDuration() == time.Minute {
				return
			}
		}
	}
	t.Fatal("no RetryInfo detail with the window as the delay")
}
