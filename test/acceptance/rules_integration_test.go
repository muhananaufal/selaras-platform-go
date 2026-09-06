package acceptance

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	eventsv1 "github.com/muhananaufal/selaras-platform-go/gen/events/v1"
	identityv1 "github.com/muhananaufal/selaras-platform-go/gen/identity/v1"
	chatconsumer "github.com/muhananaufal/selaras-platform-go/internal/chat/adapter/consumer"
	chatpg "github.com/muhananaufal/selaras-platform-go/internal/chat/adapter/postgres"
	chatapp "github.com/muhananaufal/selaras-platform-go/internal/chat/app"
	chatdomain "github.com/muhananaufal/selaras-platform-go/internal/chat/domain"
	"github.com/muhananaufal/selaras-platform-go/internal/edge/handler"
	"github.com/muhananaufal/selaras-platform-go/internal/edge/oauth"
	identitydomain "github.com/muhananaufal/selaras-platform-go/internal/identity/domain"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/outbox"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/postgres/pgtest"
)

// Aturan yang butuh satu dependensi (Postgres atau Redis) tetapi bukan
// seluruh stack. Tanpa variabel TEST_* yang sesuai, test melewati dirinya
// sendiri - persis kebijakan pgtest.

// S5 - Login sosial tidak dapat menimpa akun kata sandi. Menautkan Google ke
// akun yang sudah punya kata sandi TIDAK mengubah hash-nya.
func TestS05_SocialLoginNeverOverwritesAPasswordAccount(t *testing.T) {
	email, err := identitydomain.NewEmail("s5@user.co")
	if err != nil {
		t.Fatal(err)
	}
	const hash = identitydomain.PasswordHash("$argon2id$v=19$m=65536,t=3,p=2$salt$hash-yang-ada")
	now := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)

	user, err := identitydomain.Register(email, hash, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := user.LinkGoogle("google-sub-123", now.Add(time.Hour)); err != nil {
		t.Fatalf("linking Google to a password account must be allowed: %v", err)
	}
	if user.PasswordHash() != hash {
		t.Fatalf("the password hash changed on link: %q", user.PasswordHash())
	}
	if user.GoogleID() != "google-sub-123" {
		t.Fatalf("google id = %q", user.GoogleID())
	}
}

// fakeProvider menukar kode apa pun dengan id_token tetap.
type fakeProvider struct{}

func (fakeProvider) AuthCodeURL(state string) string {
	return "https://provider.test/auth?state=" + state
}
func (fakeProvider) Exchange(context.Context, string) (string, error) {
	return "id-token-dari-provider", nil
}

// fakeIdentity menjawab ExchangeSocialToken dengan token akses yang diketahui;
// RPC lain tidak pernah dipanggil di jalur ini.
type fakeIdentity struct {
	identityv1.IdentityClient
	accessToken string
}

func (f fakeIdentity) ExchangeSocialToken(
	context.Context, *identityv1.ExchangeSocialTokenRequest, ...grpc.CallOption,
) (*identityv1.ExchangeSocialTokenResponse, error) {
	return &identityv1.ExchangeSocialTokenResponse{
		Token: &identityv1.TokenPair{AccessToken: f.accessToken, ExpiresInSeconds: 3600},
	}, nil
}

func redisStore(t *testing.T) *oauth.Store {
	t.Helper()
	url := os.Getenv("TEST_REDIS_URL")
	if url == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("TEST_REDIS_URL is not set")
		}
		t.Skip("TEST_REDIS_URL is not set")
	}
	opts, err := goredis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	client := goredis.NewClient(opts)
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Error(err)
		}
	})
	store, err := oauth.NewStore(client, time.Minute, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func socialHandler(t *testing.T, store *oauth.Store) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.ReleaseMode)
	social := handler.NewSocial(fakeIdentity{accessToken: "token-akses-rahasia"},
		map[string]handler.ProviderClient{"google": fakeProvider{}}, store, "https://frontend.test")
	router := gin.New()
	router.GET("/auth/:provider/redirect", social.Redirect)
	router.GET("/auth/:provider/callback", social.Callback)
	return router
}

// S6 - Token tidak pernah dikirim lewat query string. Callback mengalihkan ke
// frontend dengan KODE sekali pakai di fragment (#), bukan token di ?query.
// S11 - Alur OAuth memakai parameter state: callback tanpa state yang pernah
// diterbitkan ditolak, dan state hanya bisa dipakai sekali.
func TestS06_S11_CallbackUsesAFragmentCodeAndDemandsAFreshState(t *testing.T) {
	store := redisStore(t)
	router := socialHandler(t, store)

	// Redirect menerbitkan state.
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/auth/google/redirect", nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("redirect answered %d", rec.Code)
	}
	location := rec.Header().Get("Location")
	state := location[strings.LastIndex(location, "state=")+len("state="):]
	if state == "" {
		t.Fatalf("no state in the provider redirect: %s", location)
	}

	// S11: callback dengan state yang tidak pernah diterbitkan ditolak.
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/auth/google/callback?state=dikarang&code=abc", nil))
	if rec.Code == http.StatusFound && strings.Contains(rec.Header().Get("Location"), "#code=") {
		t.Fatal("a callback with a forged state was accepted")
	}

	// Callback dengan state yang benar berhasil - dan bentuk pengalihannya
	// adalah S6.
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/auth/google/callback?state="+state+"&code=abc", nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("callback answered %d: %s", rec.Code, rec.Body.String())
	}
	location = rec.Header().Get("Location")
	if strings.Contains(location, "token-akses-rahasia") {
		t.Fatalf("S6: the access token is in the redirect URL: %s", location)
	}
	if !strings.HasPrefix(location, "https://frontend.test/auth/callback#code=") {
		t.Fatalf("S6: the redirect must carry a one-time code in the FRAGMENT, got %s", location)
	}
	if strings.Contains(location, "?") {
		t.Fatalf("S6: nothing may travel in the query string: %s", location)
	}

	// S11: state yang sama tidak bisa dipakai dua kali.
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/auth/google/callback?state="+state+"&code=abc", nil))
	if rec.Code == http.StatusFound && strings.Contains(rec.Header().Get("Location"), "#code=") {
		t.Fatal("S11: a state was accepted twice")
	}

	// Kode di fragment ditukar sekali; kode yang tidak dikenal ditolak.
	if _, err := store.ConsumeHandoffCode(context.Background(), "kode-dikarang"); !errors.Is(err, oauth.ErrUnknownCode) {
		t.Fatalf("a forged handoff code answered %v, want ErrUnknownCode", err)
	}
}

// D9 - Kegagalan AI tidak menjadi pesan model: event LlmJobFailed untuk
// percakapan TIDAK menulis apa pun ke riwayat, dan konsumen menerimanya
// (offset maju) alih-alih mengulang.
func TestD09_AnAIFailureNeverBecomesAModelMessage(t *testing.T) {
	pool := pgtest.Open(t, "chat")
	pgtest.Truncate(t, pool, "conversations")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)

	events := func(q pg.Querier) chatapp.EventWriter { return outbox.NewWriter(q) }
	svc, err := chatapp.NewService(chatpg.NewRepository(pool), chatpg.NewUnitOfWork(pool, events), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	userID, err := chatdomain.ParseUserID(uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	conversation, err := chatdomain.NewConversation(userID, "", "Halo, saya ingin bertanya soal tekanan darah", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := chatpg.NewRepository(pool).Create(ctx, conversation); err != nil {
		t.Fatal(err)
	}
	count := func() int {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM chat_messages WHERE conversation_id = $1`, conversation.ID.String()).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := count()

	client, err := kgo.NewClient(kgo.SeedBrokers("127.0.0.1:1"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	consumer, err := chatconsumer.NewResults(client, svc, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		t.Fatal(err)
	}

	reason := "fake provider fault: every call fails"
	env := &eventsv1.Envelope{
		EventId: uuid.NewString(), OccurredAt: timestamppb.Now(), SchemaVersion: 1,
		Payload: &eventsv1.Envelope_LlmJobFailed{LlmJobFailed: &eventsv1.LlmJobFailed{
			JobId: uuid.NewString(), Reason: reason,
		}},
	}
	value, err := proto.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	err = consumer.Handle(ctx, &kgo.Record{
		Topic: outbox.TopicLLMResults, Key: []byte(conversation.ID.String()), Value: value,
		Headers: []kgo.RecordHeader{
			{Key: "aggregate_type", Value: []byte("conversation")},
			{Key: "event_type", Value: []byte(outbox.EventLLMJobFailed)},
		},
	})
	if err != nil {
		t.Fatalf("a failure event must be accepted, not retried: %v", err)
	}
	if after := count(); after != before {
		t.Fatalf("D9: the failure was written into the conversation (%d -> %d messages)", before, after)
	}
}
