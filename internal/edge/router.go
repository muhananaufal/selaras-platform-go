// Package edge assembles the public REST gateway.
package edge

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	assessmentv1 "github.com/muhananaufal/selaras-platform-go/gen/assessment/v1"
	identityv1 "github.com/muhananaufal/selaras-platform-go/gen/identity/v1"
	profilev1 "github.com/muhananaufal/selaras-platform-go/gen/profile/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/edge/handler"
	"github.com/muhananaufal/selaras-platform-go/internal/edge/httperr"
	"github.com/muhananaufal/selaras-platform-go/internal/edge/middleware"
	"github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/httpx"
)

// Deps is everything the router needs.
type Deps struct {
	Identity identityv1.IdentityClient
	Profiles profilev1.ProfileClient
	// Chat may be nil: an environment without chat-svc still serves the rest,
	// and the chat routes are NOT mounted.
	Chat *handler.Chat

	// Coaching may be nil: an environment without coaching-svc still serves the
	// rest, and the coaching routes are NOT mounted. 404 is far more honest
	// than 500 from a client connected to nothing.
	Coaching    *handler.Coaching
	Tokens      middleware.TokenVerifier
	Revocations domain.RevocationChecker
	Probes      *httpx.Health
	Now         func() time.Time

	// Assessments may be nil: an environment without assessment-svc still
	// serves authentication and profiles. The routes are not mounted, so the
	// answer is 404.
	Assessments *handler.Assessment

	// Regions maps a country to its risk region for the profile view (F1-12).
	// May be nil.
	Regions assessmentv1.AssessmentClient

	// Dashboards may be nil: an environment without dashboard-svc still serves
	// the rest, and the /dashboard route is NOT mounted.
	Dashboards *handler.Dashboard

	// Nutrition may be nil: an environment without nutrition-svc still serves
	// the rest, and the culinary routes are NOT mounted.
	Nutrition *handler.Nutrition

	// Social may be nil: an environment without provider credentials still
	// serves password registration. The routes are not mounted at all, so the
	// answer is 404 - not an endpoint that exists but always fails.
	Social *handler.Social

	// Limiter may be nil: an environment without Redis still serves, without
	// rate limiting. That is stated in the log at start-up, not silently - an
	// authentication path without a limit is where passwords get guessed.
	Limiter *middleware.Limiter
}

// passthrough is a middleware that does nothing.
//
// Used when rate limiting is not installed. It exists so the route list keeps
// the same shape in both states - two branches of route registration means
// one of them eventually loses an endpoint.
func passthrough() gin.HandlerFunc {
	return func(c *gin.Context) { c.Next() }
}

// NewRouter assembles every route.
//
// Public and protected routes are split into two groups, not marked one by
// one. Marking one by one means a new route is public by default whenever
// someone forgets to add the middleware - and that forgetting produces no
// error, only an open endpoint.
func NewRouter(deps Deps) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)

	router := gin.New()
	router.Use(gin.Recovery())

	// The trace is opened before any other middleware, so even a refusal by
	// the body size limit or the rate limiter is recorded as part of the
	// trace.
	router.Use(middleware.Tracing("edge-gateway"))

	// The body size limit is installed GLOBALLY, before any route.
	//
	// Installed per route, it would be missed on the next endpoint someone
	// adds - and that missed endpoint is exactly the one that would be used to
	// send something very large.
	router.Use(middleware.LimitBody())

	// 404 and 405 are answered with the same error shape as the rest. Gin's
	// default sends an empty text body, so a client parsing JSON fails
	// precisely on the error path.
	router.NoRoute(func(c *gin.Context) {
		httperr.Write(c, http.StatusNotFound, httperr.CodeNotFound, "No such endpoint.")
	})
	router.HandleMethodNotAllowed = true
	router.NoMethod(func(c *gin.Context) {
		httperr.Write(c, http.StatusMethodNotAllowed, httperr.CodeInvalidArgument, "Method not allowed.")
	})

	router.GET("/healthz", gin.WrapF(deps.Probes.Live))
	router.GET("/readyz", gin.WrapF(deps.Probes.Ready))

	auth := handler.NewAuth(deps.Identity)
	profiles := handler.NewProfile(deps.Profiles, deps.Regions, deps.Now)

	// The prefix is kept from the legacy system; the frontend calls /api/v1.
	api := router.Group("/api/v1")

	// authLimit limits every path that compares credentials or sends email.
	// Nil means no limiter is installed - an environment without Redis still
	// serves, and that is stated in the log at start-up.
	authLimit := passthrough()
	llmLimit := passthrough()
	if deps.Limiter != nil {
		authLimits, llmLimits := middleware.LimitsFromEnv()
		authLimit = deps.Limiter.ByIP("auth", authLimits)
		llmLimit = deps.Limiter.ByUser("llm", llmLimits)
	}

	public := api.Group("")
	{
		public.POST("/register", authLimit, auth.Register)
		public.POST("/login", authLimit, auth.Login)
		public.POST("/password-reset/request", authLimit, auth.RequestPasswordReset)
		public.POST("/password-reset/confirm", authLimit, auth.ConfirmPasswordReset)

		if deps.Social != nil {
			public.GET("/auth/:provider/redirect", deps.Social.Redirect)
			public.GET("/auth/:provider/callback", deps.Social.Callback)
			public.POST("/auth/session", deps.Social.Session)
		}
	}

	protected := api.Group("")
	protected.Use(middleware.Authenticate(deps.Tokens, deps.Revocations))
	{
		protected.POST("/logout", auth.Logout)

		// DELETE, the same shape as the legacy system (ADR-005). What changes is
		// the answer code - 202, not 200 - because the deletion crosses six units
		// and is not finished when the request is answered. Account deletion
		// compares a password, so it is limited like every other authentication
		// path - without that, it becomes an unlimited place to guess passwords.
		protected.DELETE("/delete-account", authLimit, auth.DeleteAccount)
		protected.GET("/me", handler.Me)
		protected.GET("/profile", profiles.Show)
		protected.PATCH("/profile", profiles.Update)

		if deps.Assessments != nil {
			protected.POST("/risk-assessments", deps.Assessments.Start)
			protected.GET("/risk-assessments", deps.Assessments.Index)
			protected.GET("/risk-assessments/:slug", deps.Assessments.Show)

			// PATCH, not POST: the shape is kept from the legacy system so existing
			// clients need not change (ADR-005). What changes is the answer - 202
			// with a job_id, not the report - and that is recorded as a deliberate
			// exception.
			protected.PATCH("/risk-assessments/:slug/personalize", llmLimit, deps.Assessments.Personalize)
			// Twelve coaching endpoints. The URL shapes are kept from the legacy
			// system so existing clients need not change (ADR-005).
			//
			// What CHANGES is the answer code: starting a program, opening a thread,
			// and sending a message now answer 202 Accepted because the result comes
			// later - the legacy system held the HTTP request while Gemini worked.
			if deps.Coaching != nil {
				mountCoaching(protected, deps.Coaching, llmLimit)
			}

			if deps.Chat != nil {
				mountChat(protected, deps.Chat, llmLimit)
			}

			if deps.Nutrition != nil {
				mountNutrition(protected, deps.Nutrition, llmLimit)
			}

			if deps.Dashboards != nil {
				// One endpoint, the whole home page. The URL shape is kept from the
				// legacy system (ADR-005).
				protected.GET("/dashboard", deps.Dashboards.Show)
			}

		}
	}

	return router
}

// mountCoaching mounts the twelve coaching endpoints.
//
// Separate from NewRouter so the route list reads as one list, not hidden in
// the middle of the rest of the assembly. The URL shapes are kept from the
// legacy system so existing clients need not change (ADR-005).
//
// What CHANGES is the answer code: starting a program, opening a thread, and
// sending a message now answer 202 Accepted because the result comes later. The
// legacy system held the HTTP request while Gemini worked.
func mountCoaching(r gin.IRouter, h *handler.Coaching, limit gin.HandlerFunc) {
	group := r.Group("/coaching")

	group.POST("/programs", limit, h.StartProgram)
	group.GET("/programs/:slug", h.ShowProgram)
	group.PATCH("/programs/:slug/toggle-program-status", h.ToggleProgramStatus)
	group.DELETE("/programs/:slug", h.DestroyProgram)
	group.GET("/programs/:slug/graduation-report", h.GraduationReport)
	group.POST("/programs/:slug/threads", limit, h.StartThread)

	group.POST("/threads/:slug/messages", limit, h.SendMessage)
	group.GET("/threads/:slug", h.ShowThread)
	group.PATCH("/threads/:slug", h.UpdateThread)
	group.DELETE("/threads/:slug", h.DestroyThread)

	group.PATCH("/tasks/:id/toggle-task-status", h.ToggleTaskStatus)
}

// mountChat mounts the six general-assistant conversation endpoints.
//
// The URL shapes are kept from the legacy system (ADR-005). What changes is
// the answer code: creating a conversation with a message and sending a
// message now answer 202 Accepted, because the reply comes later.
func mountChat(r gin.IRouter, h *handler.Chat, limit gin.HandlerFunc) {
	group := r.Group("/chat")

	group.GET("/conversations", h.Index)
	group.POST("/conversations", limit, h.Store)
	group.GET("/conversations/:slug", h.Show)
	group.PATCH("/conversations/:slug", h.Update)
	group.POST("/conversations/:slug/messages", limit, h.SendMessage)
	group.DELETE("/conversations/:slug", h.Destroy)
}

// mountNutrition mounts the three culinary endpoints.
//
// The URL shapes are kept from the legacy system (ADR-005). What CHANGES is the
// answer code of guide generation: 202 Accepted, because the guide comes later.
// The legacy system held the HTTP request while Gemini worked, with a
// 180-second timeout (B14).
func mountNutrition(r gin.IRouter, h *handler.Nutrition, limit gin.HandlerFunc) {
	group := r.Group("/culinary")

	group.GET("/hub-data", h.HubData)
	group.PATCH("/preferences", h.UpdatePreferences)
	group.POST("/daily-guides", limit, h.GenerateDailyGuide)
}
