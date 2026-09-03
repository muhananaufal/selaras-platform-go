// Package edge merakit gateway REST publik.
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

// Deps adalah seluruh yang dibutuhkan router.
type Deps struct {
	Identity identityv1.IdentityClient
	Profiles profilev1.ProfileClient
	// Chat boleh nil: lingkungan tanpa chat-svc tetap melayani sisanya, dan
	// rute chat TIDAK dipasang.
	Chat *handler.Chat

	// Coaching boleh nil: lingkungan tanpa coaching-svc tetap melayani sisanya,
	// dan rute coaching TIDAK dipasang. 404 jauh lebih jujur daripada 500 dari
	// klien yang tidak menyambung ke mana-mana.
	Coaching    *handler.Coaching
	Tokens      middleware.TokenVerifier
	Revocations domain.RevocationChecker
	Probes      *httpx.Health
	Now         func() time.Time

	// Assessments boleh nil: lingkungan tanpa assessment-svc tetap melayani
	// autentikasi dan profil. Rutenya tidak dipasang, jadi jawabannya 404.
	Assessments *handler.Assessment

	// Regions memetakan negara ke wilayah risiko untuk tampilan profil
	// (F1-12). Boleh nil.
	Regions assessmentv1.AssessmentClient

	// Nutrition boleh nil: lingkungan tanpa nutrition-svc tetap melayani
	// sisanya, dan rute culinary TIDAK dipasang.
	Nutrition *handler.Nutrition

	// Social boleh nil: lingkungan tanpa kredensial penyedia tetap
	// melayani pendaftaran lewat kata sandi. Rutenya tidak dipasang sama
	// sekali, sehingga jawabannya 404 - bukan endpoint yang ada tetapi
	// selalu gagal.
	Social *handler.Social
}

// NewRouter merakit seluruh rute.
//
// Rute publik dan rute terproteksi dipisahkan menjadi dua grup, bukan
// ditandai satu per satu. Menandai satu per satu berarti rute baru menjadi
// publik secara bawaan setiap kali seseorang lupa menambahkan middleware -
// dan lupa itu tidak menghasilkan galat apa pun, hanya endpoint terbuka.
func NewRouter(deps Deps) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)

	router := gin.New()
	router.Use(gin.Recovery())

	// 404 dan 405 dijawab dengan bentuk galat yang sama seperti selebihnya.
	// Bawaan Gin mengirim badan teks kosong, sehingga klien yang mengurai
	// JSON justru gagal justru pada jalur galat.
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

	// Prefiks dipertahankan dari sistem lama; frontend memanggil /api/v1.
	api := router.Group("/api/v1")

	public := api.Group("")
	{
		public.POST("/register", auth.Register)
		public.POST("/login", auth.Login)
		public.POST("/password-reset/request", auth.RequestPasswordReset)
		public.POST("/password-reset/confirm", auth.ConfirmPasswordReset)

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
		protected.GET("/me", handler.Me)
		protected.GET("/profile", profiles.Show)
		protected.PATCH("/profile", profiles.Update)

		if deps.Assessments != nil {
			protected.POST("/risk-assessments", deps.Assessments.Start)
			protected.GET("/risk-assessments", deps.Assessments.Index)
			protected.GET("/risk-assessments/:slug", deps.Assessments.Show)

			// PATCH, bukan POST: bentuknya dipertahankan dari sistem lama supaya
			// klien yang ada tidak perlu berubah (ADR-005). Yang berubah adalah
			// jawabannya - 202 dengan job_id, bukan laporannya - dan itu dicatat
			// sebagai pengecualian yang disengaja.
			protected.PATCH("/risk-assessments/:slug/personalize", deps.Assessments.Personalize)
			// Dua belas endpoint coaching. Bentuk URL-nya dipertahankan dari
			// sistem lama supaya klien yang ada tidak perlu berubah (ADR-005).
			//
			// Yang BERUBAH adalah kode jawabannya: memulai program, membuka
			// thread, dan mengirim pesan kini menjawab 202 Accepted karena
			// hasilnya datang belakangan - sistem lama menahan permintaan HTTP
			// selama Gemini bekerja.
			if deps.Coaching != nil {
				mountCoaching(protected, deps.Coaching)
			}

			if deps.Chat != nil {
				mountChat(protected, deps.Chat)
			}

			if deps.Nutrition != nil {
				mountNutrition(protected, deps.Nutrition)
			}

		}
	}

	return router
}

// mountCoaching memasang dua belas endpoint coaching.
//
// Terpisah dari NewRouter supaya daftar rutenya terbaca sebagai satu daftar,
// bukan tersembunyi di tengah perakitan yang lain. Bentuk URL-nya dipertahankan
// dari sistem lama supaya klien yang ada tidak perlu berubah (ADR-005).
//
// Yang BERUBAH adalah kode jawabannya: memulai program, membuka thread, dan
// mengirim pesan kini menjawab 202 Accepted karena hasilnya datang belakangan.
// Sistem lama menahan permintaan HTTP selama Gemini bekerja.
func mountCoaching(r gin.IRouter, h *handler.Coaching) {
	group := r.Group("/coaching")

	group.POST("/programs", h.StartProgram)
	group.GET("/programs/:slug", h.ShowProgram)
	group.PATCH("/programs/:slug/toggle-program-status", h.ToggleProgramStatus)
	group.DELETE("/programs/:slug", h.DestroyProgram)
	group.GET("/programs/:slug/graduation-report", h.GraduationReport)
	group.POST("/programs/:slug/threads", h.StartThread)

	group.POST("/threads/:slug/messages", h.SendMessage)
	group.GET("/threads/:slug", h.ShowThread)
	group.PATCH("/threads/:slug", h.UpdateThread)
	group.DELETE("/threads/:slug", h.DestroyThread)

	group.PATCH("/tasks/:id/toggle-task-status", h.ToggleTaskStatus)
}

// mountChat memasang enam endpoint percakapan asisten umum.
//
// Bentuk URL-nya dipertahankan dari sistem lama (ADR-005). Yang berubah adalah
// kode jawabannya: membuat percakapan dengan pesan dan mengirim pesan kini
// menjawab 202 Accepted, karena balasannya datang belakangan.
func mountChat(r gin.IRouter, h *handler.Chat) {
	group := r.Group("/chat")

	group.GET("/conversations", h.Index)
	group.POST("/conversations", h.Store)
	group.GET("/conversations/:slug", h.Show)
	group.PATCH("/conversations/:slug", h.Update)
	group.POST("/conversations/:slug/messages", h.SendMessage)
	group.DELETE("/conversations/:slug", h.Destroy)
}

// mountNutrition memasang tiga endpoint culinary.
//
// Bentuk URL-nya dipertahankan dari sistem lama (ADR-005). Yang BERUBAH adalah
// kode jawaban pembuatan panduan: 202 Accepted, karena panduannya datang
// belakangan. Sistem lama menahan permintaan HTTP selama Gemini bekerja, dengan
// timeout 180 detik (B14).
func mountNutrition(r gin.IRouter, h *handler.Nutrition) {
	group := r.Group("/culinary")

	group.GET("/hub-data", h.HubData)
	group.PATCH("/preferences", h.UpdatePreferences)
	group.POST("/daily-guides", h.GenerateDailyGuide)
}
