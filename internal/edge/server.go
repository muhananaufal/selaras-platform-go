// Package edge assembles the public gateway: the edge.v1 Connect contract
// (ADR-027) plus the few plain HTTP routes a browser opens directly.
package edge

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"google.golang.org/protobuf/reflect/protoreflect"

	assessmentv1 "github.com/muhananaufal/selaras-platform-go/gen/assessment/v1"
	chatv1 "github.com/muhananaufal/selaras-platform-go/gen/chat/v1"
	coachingv1 "github.com/muhananaufal/selaras-platform-go/gen/coaching/v1"
	dashboardv1 "github.com/muhananaufal/selaras-platform-go/gen/dashboard/v1"
	edgev1 "github.com/muhananaufal/selaras-platform-go/gen/edge/v1"
	"github.com/muhananaufal/selaras-platform-go/gen/edge/v1/edgev1connect"
	identityv1 "github.com/muhananaufal/selaras-platform-go/gen/identity/v1"
	nutritionv1 "github.com/muhananaufal/selaras-platform-go/gen/nutrition/v1"
	profilev1 "github.com/muhananaufal/selaras-platform-go/gen/profile/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/edge/interceptor"
	"github.com/muhananaufal/selaras-platform-go/internal/edge/service"
	"github.com/muhananaufal/selaras-platform-go/internal/edge/wire"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/httpx"
)

// MaxBodyBytes is the request body size limit: one megabyte. The largest
// legitimate message is the risk questionnaire - a few dozen fields - and a
// chat message is bounded to 16 KiB in its domain. Without a limit, one
// request could make the gateway read an unbounded body into memory before
// any validation ran.
const MaxBodyBytes = 1 << 20

// streamGrace is added to the Watch limit when a stream's write deadline is
// extended, so the stream ends on its own terms before the socket does.
const streamGrace = 30 * time.Second

// Deps is everything the gateway needs.
//
// Every client that may be nil is optional: an environment without that
// service does not mount its procedures at all, so a client gets
// unimplemented (HTTP 404) - far more honest than an endpoint that exists and
// always fails.
type Deps struct {
	Identity    identityv1.IdentityClient
	Profiles    profilev1.ProfileClient
	Regions     assessmentv1.AssessmentClient
	Assessments assessmentv1.AssessmentClient
	Coaching    coachingv1.CoachingClient
	Chat        chatv1.ChatClient
	Nutrition   nutritionv1.NutritionClient
	Dashboards  dashboardv1.DashboardClient

	Tokens      interceptor.TokenVerifier
	Revocations domain.RevocationChecker

	// Limiter may be nil: an environment without Redis serves without rate
	// limiting, and main says so at start-up.
	Limiter *interceptor.RateLimiter

	// Social and Handoff may be nil together: social sign-in not configured.
	Social  *service.Social
	Handoff service.HandoffCodes

	Probes *httpx.Health
	Now    func() time.Time
	Watch  service.WatchConfig
}

// NewHandler builds the whole public HTTP handler.
func NewHandler(deps Deps) (http.Handler, error) {
	switch {
	case deps.Identity == nil || deps.Profiles == nil:
		return nil, errors.New("identity and profile clients are required")
	case deps.Tokens == nil || deps.Revocations == nil:
		return nil, errors.New("token verifier and revocation checker are required")
	case deps.Probes == nil:
		return nil, errors.New("probes are required")
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.Watch.Interval <= 0 || deps.Watch.MaxDuration <= 0 {
		deps.Watch = service.DefaultWatch
	}

	auth, err := interceptor.NewAuthenticator(deps.Tokens, deps.Revocations, service.PublicProcedures()...)
	if err != nil {
		return nil, err
	}

	// Order matters: the first interceptor is the outermost. Authentication
	// runs first so the limiter can count by user; the limiter runs before
	// validation so a flood of malformed requests is still counted; NoStore
	// wraps the answer whatever it is.
	interceptors := []connect.Interceptor{auth}
	if deps.Limiter != nil {
		interceptors = append(interceptors, deps.Limiter)
	}
	interceptors = append(interceptors, interceptor.EnumGuard{}, interceptor.NoStore{})

	opts := append([]connect.HandlerOption{
		connect.WithInterceptors(interceptors...),
		connect.WithReadMaxBytes(MaxBodyBytes),
	}, wire.Codecs()...)

	mux := http.NewServeMux()
	m := mounter{mux: mux, streamDeadline: deps.Watch.MaxDuration + streamGrace}

	m.mount(edgev1connect.NewAuthHandler(service.NewAuth(deps.Identity, deps.Handoff), opts...))
	m.mount(edgev1connect.NewProfileHandler(service.NewProfile(deps.Profiles, deps.Regions, deps.Now), opts...))
	if deps.Assessments != nil {
		m.mount(edgev1connect.NewAssessmentHandler(service.NewAssessment(deps.Assessments, deps.Watch), opts...))
	}
	if deps.Coaching != nil {
		m.mount(edgev1connect.NewCoachingHandler(service.NewCoaching(deps.Coaching, deps.Watch), opts...))
	}
	if deps.Chat != nil {
		m.mount(edgev1connect.NewChatHandler(service.NewChat(deps.Chat, deps.Watch), opts...))
	}
	if deps.Nutrition != nil {
		m.mount(edgev1connect.NewNutritionHandler(service.NewNutrition(deps.Nutrition, deps.Watch), opts...))
	}
	if deps.Dashboards != nil {
		m.mount(edgev1connect.NewDashboardHandler(service.NewDashboard(deps.Dashboards), opts...))
	}
	if m.err != nil {
		return nil, m.err
	}

	if deps.Social != nil {
		mux.HandleFunc("GET /auth/{provider}/redirect", deps.Social.Redirect)
		mux.HandleFunc("GET /auth/{provider}/callback", deps.Social.Callback)
	}

	mux.HandleFunc("GET /healthz", deps.Probes.Live)
	mux.HandleFunc("GET /readyz", deps.Probes.Ready)

	// The trace is opened outside everything else, so even a refusal by the
	// body limit is part of a trace. otelhttp - not otelconnect - on purpose:
	// the SLO alerts read http_server_request_duration_seconds with
	// http_route and http_response_status_code, and otelhttp keeps exactly
	// that metric. http_route is the full procedure path because every
	// procedure is registered as its own ServeMux pattern.
	return otelhttp.NewHandler(limitBody(mux), "edge-gateway",
		otelhttp.WithFilter(func(r *http.Request) bool {
			return r.URL.Path != "/healthz" && r.URL.Path != "/readyz"
		}),
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			if r.Pattern != "" {
				return r.Pattern
			}
			return r.Method
		}),
	), nil
}

// mounter registers each procedure of a service as its own pattern.
//
// Mounting the service prefix once would work too, but every metric would
// then carry the same http_route for all of a service's procedures, and the
// per-endpoint latency alerts would be blind.
type mounter struct {
	mux            *http.ServeMux
	streamDeadline time.Duration
	err            error
}

func (m *mounter) mount(prefix string, handler http.Handler) {
	if m.err != nil {
		return
	}
	svc, ok := serviceFor(prefix)
	if !ok {
		m.err = errors.New("no edge.v1 service descriptor for " + prefix)
		return
	}

	methods := svc.Methods()
	for i := range methods.Len() {
		method := methods.Get(i)
		path := prefix + string(method.Name())
		if method.IsStreamingServer() {
			m.mux.Handle(path, extendWriteDeadline(handler, m.streamDeadline))
			continue
		}
		m.mux.Handle(path, handler)
	}
}

// serviceFor finds the descriptor of a generated handler's path prefix,
// such as "/edge.v1.Auth/".
func serviceFor(prefix string) (protoreflect.ServiceDescriptor, bool) {
	files := []protoreflect.FileDescriptor{
		edgev1.File_edge_v1_auth_proto,
		edgev1.File_edge_v1_profile_proto,
		edgev1.File_edge_v1_assessment_proto,
		edgev1.File_edge_v1_coaching_proto,
		edgev1.File_edge_v1_chat_proto,
		edgev1.File_edge_v1_nutrition_proto,
		edgev1.File_edge_v1_dashboard_proto,
	}
	for _, f := range files {
		services := f.Services()
		for i := range services.Len() {
			s := services.Get(i)
			if "/"+string(s.FullName())+"/" == prefix {
				return s, true
			}
		}
	}
	return nil, false
}

// extendWriteDeadline lifts the server's WriteTimeout for one stream.
//
// The server keeps a short WriteTimeout because a Go server without one holds
// hanging connections forever. A Watch stream legitimately stays open for
// minutes, so ONLY its connection gets a longer deadline - bounded by the
// Watch limit itself, so a stream still cannot live forever.
func extendWriteDeadline(next http.Handler, d time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(d)); err != nil {
			// Only a writer that cannot set deadlines refuses (a test recorder,
			// say). The stream still works; it just keeps the server default.
			slog.WarnContext(r.Context(), "could not extend the write deadline of a stream", "error", err)
		}
		next.ServeHTTP(w, r)
	})
}

// limitBody stops reading a request body midway once it passes the limit,
// instead of reading everything and measuring afterwards - by then the memory
// is already spent. connect.WithReadMaxBytes guards the message size; this
// guards the raw bytes, including the plain HTTP routes.
func limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, MaxBodyBytes)
		}
		next.ServeHTTP(w, r)
	})
}

// RateLimitPolicies maps each limited procedure to its policy.
//
// The auth limit covers every procedure that compares a credential or sends
// email, counted per client address. The LLM limit covers every procedure
// that queues paid work, counted per user. A procedure that queues LLM work
// and is missing here is an unlimited bill; TestEveryLLMProcedureIsLimited
// keeps this list honest.
func RateLimitPolicies(auth, llm interceptor.Limit) map[string]interceptor.Policy {
	byIP := func(name string) interceptor.Policy {
		return interceptor.Policy{Name: name, Limit: auth, Subject: interceptor.ByClientIP}
	}
	byUser := func(name string) interceptor.Policy {
		return interceptor.Policy{Name: name, Limit: llm, Subject: interceptor.ByUser}
	}
	return map[string]interceptor.Policy{
		edgev1connect.AuthRegisterProcedure:              byIP("auth"),
		edgev1connect.AuthLoginProcedure:                 byIP("auth"),
		edgev1connect.AuthRequestPasswordResetProcedure:  byIP("auth"),
		edgev1connect.AuthConfirmPasswordResetProcedure:  byIP("auth"),
		edgev1connect.AuthExchangeSocialSessionProcedure: byIP("auth"),

		// Deletion compares a password, so it is limited like every other
		// credential check - otherwise it becomes an unlimited guessing place.
		edgev1connect.AuthDeleteAccountProcedure: byIP("auth"),

		edgev1connect.AssessmentRequestPersonalizationProcedure: byUser("llm"),
		edgev1connect.CoachingStartProgramProcedure:             byUser("llm"),
		edgev1connect.CoachingStartThreadProcedure:              byUser("llm"),
		edgev1connect.CoachingSendThreadMessageProcedure:        byUser("llm"),

		// Queues a paid report whenever the last one FAILED (coaching app
		// RequestGraduationReport). The REST route had no limit; this one does.
		edgev1connect.CoachingGetGraduationReportProcedure: byUser("llm"),

		edgev1connect.ChatCreateConversationProcedure:      byUser("llm"),
		edgev1connect.ChatSendMessageProcedure:             byUser("llm"),
		edgev1connect.NutritionGenerateDailyGuideProcedure: byUser("llm"),
	}
}
