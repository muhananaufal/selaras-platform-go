package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/proto"

	eventsv1 "github.com/muhananaufal/selaras-platform-go/gen/events/v1"
	chatpg "github.com/muhananaufal/selaras-platform-go/internal/chat/adapter/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/chat/app"
	"github.com/muhananaufal/selaras-platform-go/internal/chat/domain"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/outbox"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/postgres/pgtest"
)

type harness struct {
	pool *pgxpool.Pool
	svc  *app.Service
	ctx  context.Context

	// now can be advanced by tests. A truly frozen clock gives two different
	// occurrences the same timestamp, and an order that depends on that
	// timestamp falls back to the tie-breaker - which tests something else.
	now time.Time
}

func setup(t *testing.T) *harness {
	t.Helper()

	pool := pgtest.Open(t, "chat")
	pgtest.Truncate(t, pool, "conversations", "outbox")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	now := time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)

	h := &harness{pool: pool, ctx: ctx, now: now}

	events := func(q pg.Querier) app.EventWriter { return outbox.NewWriter(q) }
	svc, err := app.NewService(
		chatpg.NewRepository(pool),
		chatpg.NewUnitOfWork(pool, events),

		// The clock reads the harness rather than copying its value: a test that
		// advances the time has to really change what the service sees.
		func() time.Time { return h.now },
	)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	h.svc = svc

	return h
}

// advance moves the clock the service sees forward.
func (h *harness) advance(d time.Duration) { h.now = h.now.Add(d) }

func (h *harness) user() string { return uuid.NewString() }

func (h *harness) events(t *testing.T) []*eventsv1.Envelope {
	t.Helper()

	rows, err := h.pool.Query(h.ctx, `SELECT payload FROM outbox ORDER BY created_at, id`)
	if err != nil {
		t.Fatalf("reading the outbox: %v", err)
	}
	defer rows.Close()

	var out []*eventsv1.Envelope
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			t.Fatalf("scanning an event: %v", err)
		}
		env := &eventsv1.Envelope{}
		if err := proto.Unmarshal(payload, env); err != nil {
			t.Fatalf("decoding an event: %v", err)
		}
		out = append(out, env)
	}
	return out
}

// TestCreatingAConversationWithAMessageQueuesTheReply is F5-05 and F5-07.
func TestCreatingAConversationWithAMessageQueuesTheReply(t *testing.T) {
	h := setup(t)
	owner := h.user()

	view, err := h.svc.CreateConversation(h.ctx, app.CreateConversationCommand{
		UserID:       owner,
		FirstMessage: "Apakah kopi berpengaruh pada tekanan darah saya?",
	})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}

	// D12: the title is derived from the first message.
	if view.Conversation.Title != "Apakah kopi berpengaruh pada tekanan darah sa..." {
		t.Fatalf("the derived title is %q", view.Conversation.Title)
	}
	if len(view.Messages) != 1 || view.Messages[0].Role != domain.RoleUser {
		t.Fatalf("the conversation opened with %d messages", len(view.Messages))
	}

	events := h.events(t)
	if len(events) != 1 {
		t.Fatalf("%d events were written, want 1", len(events))
	}

	req := events[0].GetChatReplyRequested()
	if req == nil {
		t.Fatal("the event is not a chat reply request")
	}
	if req.GetConversationId() != view.Conversation.ID.String() {
		t.Fatalf("the event names conversation %q", req.GetConversationId())
	}

	// coaching_thread_id is NOT set: that is what tells a general conversation
	// from a coaching thread on the worker side, and setting it would send the
	// reply to the wrong place.
	if req.GetCoachingThreadId() != "" {
		t.Fatalf("a general conversation carries a coaching thread id: %q", req.GetCoachingThreadId())
	}
}

// TestAConversationCanBeCreatedEmpty guards the "start new" button.
func TestAConversationCanBeCreatedEmpty(t *testing.T) {
	h := setup(t)

	view, err := h.svc.CreateConversation(h.ctx, app.CreateConversationCommand{
		UserID: h.user(),
	})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	if view.Conversation.Title != domain.DefaultTitle {
		t.Fatalf("an empty conversation is titled %q", view.Conversation.Title)
	}
	if got := len(h.events(t)); got != 0 {
		t.Fatalf("%d events were written for a conversation with no message", got)
	}
}

// TestEachMessageGetsItsOwnKey keeps the second message from being skipped
// as a duplicate.
func TestEachMessageGetsItsOwnKey(t *testing.T) {
	h := setup(t)
	owner := h.user()

	view, err := h.svc.CreateConversation(h.ctx, app.CreateConversationCommand{
		UserID: owner, FirstMessage: "halo",
	})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}

	second, err := h.svc.SendMessage(h.ctx, app.SendMessageCommand{
		Slug: view.Conversation.Slug, UserID: owner, Text: "apa kabar?",
	})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	events := h.events(t)
	if len(events) != 2 {
		t.Fatalf("%d events were written, want 2", len(events))
	}

	first := events[0].GetIdempotencyKey().GetValue()
	latest := events[1].GetIdempotencyKey().GetValue()
	if first == latest {
		t.Fatalf("two messages carry the same key: %q", first)
	}
	if latest != "chat-reply:"+second.ID.String() {
		t.Fatalf("the second key is %q", latest)
	}
}

// TestAReplyArrivesAsAModelMessage guards the reply path.
func TestAReplyArrivesAsAModelMessage(t *testing.T) {
	h := setup(t)
	owner := h.user()

	view, _ := h.svc.CreateConversation(h.ctx, app.CreateConversationCommand{
		UserID: owner, FirstMessage: "halo",
	})

	if err := h.svc.StoreReply(h.ctx, view.Conversation.ID.String(),
		"Halo, ada yang bisa saya bantu?"); err != nil {
		t.Fatalf("StoreReply: %v", err)
	}

	shown, err := h.svc.ShowConversation(h.ctx, view.Conversation.Slug, owner, domain.Page{})
	if err != nil {
		t.Fatalf("ShowConversation: %v", err)
	}
	if len(shown.Messages) != 2 {
		t.Fatalf("the conversation holds %d messages, want 2", len(shown.Messages))
	}
	if shown.Messages[1].Role != domain.RoleModel {
		t.Fatalf("the last message has role %q, want model", shown.Messages[1].Role)
	}
}

// TestTheListIsPagedAndOrderedByRecentUse is F5-04.
func TestTheListIsPagedAndOrderedByRecentUse(t *testing.T) {
	h := setup(t)
	owner := h.user()

	// Five conversations, created at different times so the order can be
	// checked. The times are set directly in the database - the service clock
	// is frozen in the harness, and that is precisely what keeps the other
	// tests predictable.
	slugs := make([]string, 0, 5)
	for i := range 5 {
		view, err := h.svc.CreateConversation(h.ctx, app.CreateConversationCommand{
			UserID: owner, Title: "Percakapan " + string(rune('A'+i)),
		})
		if err != nil {
			t.Fatalf("CreateConversation: %v", err)
		}
		slugs = append(slugs, view.Conversation.Slug)

		if _, err := h.pool.Exec(h.ctx,
			`UPDATE conversations SET updated_at = $2 WHERE slug = $1`,
			view.Conversation.Slug, h.now.Add(time.Duration(i)*time.Hour)); err != nil {
			t.Fatalf("ageing a conversation: %v", err)
		}
	}

	list, err := h.svc.ListConversations(h.ctx, owner, domain.Page{Number: 1, Size: 2})
	if err != nil {
		t.Fatalf("ListConversations: %v", err)
	}
	if list.Total != 5 {
		t.Fatalf("the list reports %d conversations, want 5", list.Total)
	}
	if len(list.Items) != 2 {
		t.Fatalf("the first page holds %d items, want 2", len(list.Items))
	}

	// Newest first: the last one created is at the top.
	if list.Items[0].Slug != slugs[4] {
		t.Fatalf("the newest conversation is not first")
	}

	second, err := h.svc.ListConversations(h.ctx, owner, domain.Page{Number: 3, Size: 2})
	if err != nil {
		t.Fatalf("ListConversations: %v", err)
	}
	if len(second.Items) != 1 {
		t.Fatalf("the third page holds %d items, want 1", len(second.Items))
	}

	// And someone else's conversations are not included.
	stranger, err := h.svc.ListConversations(h.ctx, h.user(), domain.Page{})
	if err != nil {
		t.Fatalf("ListConversations: %v", err)
	}
	if len(stranger.Items) != 0 || stranger.Total != 0 {
		// Both are named: they come from DIFFERENT queries, and naming only one
		// once made a real failure read like no failure at all - "saw 0 of
		// someone else's conversations".
		t.Fatalf("a stranger sees %d items and a total of %d; both should be zero",
			len(stranger.Items), stranger.Total)
	}
}

// TestSendingAMessageMovesTheConversationUp keeps the list order
// meaningful.
func TestSendingAMessageMovesTheConversationUp(t *testing.T) {
	h := setup(t)
	owner := h.user()

	older, _ := h.svc.CreateConversation(h.ctx, app.CreateConversationCommand{
		UserID: owner, Title: "Lama",
	})
	if _, err := h.pool.Exec(h.ctx,
		`UPDATE conversations SET updated_at = $2 WHERE slug = $1`,
		older.Conversation.Slug, h.now.Add(-24*time.Hour)); err != nil {
		t.Fatalf("ageing: %v", err)
	}

	newer, _ := h.svc.CreateConversation(h.ctx, app.CreateConversationCommand{
		UserID: owner, Title: "Baru",
	})

	// The clock is advanced BEFORE the message is sent. Without that, the old
	// conversation gets the same timestamp as the new one, and the order falls
	// back to the tie-breaker - which tests something other than what this
	// test means to.
	h.advance(time.Hour)

	// The OLD conversation receives a message, so it moves to the top.
	if _, err := h.svc.SendMessage(h.ctx, app.SendMessageCommand{
		Slug: older.Conversation.Slug, UserID: owner, Text: "halo lagi",
	}); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	list, err := h.svc.ListConversations(h.ctx, owner, domain.Page{})
	if err != nil {
		t.Fatalf("ListConversations: %v", err)
	}
	if list.Items[0].Slug != older.Conversation.Slug {
		t.Fatalf("the conversation that just received a message is not first; %q is",
			list.Items[0].Title)
	}
	_ = newer
}

// TestSomeoneElsesConversationIsNotFound is S9.
func TestSomeoneElsesConversationIsNotFound(t *testing.T) {
	h := setup(t)
	owner := h.user()
	stranger := h.user()

	view, _ := h.svc.CreateConversation(h.ctx, app.CreateConversationCommand{
		UserID: owner, FirstMessage: "halo",
	})
	slug := view.Conversation.Slug

	if _, err := h.svc.ShowConversation(h.ctx, slug, stranger, domain.Page{}); !errors.Is(err, domain.ErrConversationNotFound) {
		t.Errorf("ShowConversation returned %v", err)
	}
	if _, err := h.svc.RenameConversation(h.ctx, slug, stranger, "Baru"); !errors.Is(err, domain.ErrConversationNotFound) {
		t.Errorf("RenameConversation returned %v", err)
	}
	if _, err := h.svc.SendMessage(h.ctx, app.SendMessageCommand{
		Slug: slug, UserID: stranger, Text: "halo",
	}); !errors.Is(err, domain.ErrConversationNotFound) {
		t.Errorf("SendMessage returned %v", err)
	}
	if err := h.svc.DeleteConversation(h.ctx, slug, stranger); !errors.Is(err, domain.ErrConversationNotFound) {
		t.Errorf("DeleteConversation returned %v", err)
	}

	// A conversation that really does not exist answers the SAME.
	if _, err := h.svc.ShowConversation(h.ctx, "tidakadaslugini", stranger, domain.Page{}); !errors.Is(err, domain.ErrConversationNotFound) {
		t.Errorf("a missing conversation returned %v", err)
	}
}

// TestDeletingAConversationTakesItsMessages is F5-05.
func TestDeletingAConversationTakesItsMessages(t *testing.T) {
	h := setup(t)
	owner := h.user()

	view, _ := h.svc.CreateConversation(h.ctx, app.CreateConversationCommand{
		UserID: owner, FirstMessage: "halo",
	})
	id := view.Conversation.ID.String()

	if err := h.svc.DeleteConversation(h.ctx, view.Conversation.Slug, owner); err != nil {
		t.Fatalf("DeleteConversation: %v", err)
	}

	// Checked through direct SQL: a mistaken repository could report empty for
	// data that is still there.
	var messages int
	if err := h.pool.QueryRow(h.ctx,
		`SELECT count(*) FROM chat_messages WHERE conversation_id = $1`, id).Scan(&messages); err != nil {
		t.Fatalf("counting messages: %v", err)
	}
	if messages != 0 {
		t.Fatalf("%d messages survived the deletion", messages)
	}

	if err := h.svc.DeleteConversation(h.ctx, view.Conversation.Slug, owner); !errors.Is(err, domain.ErrConversationNotFound) {
		t.Fatalf("deleting a missing conversation returned %v", err)
	}
}

// TestTheContextWindowTakesTheNewestMessages is D8.
func TestTheContextWindowTakesTheNewestMessages(t *testing.T) {
	h := setup(t)
	owner := h.user()

	view, _ := h.svc.CreateConversation(h.ctx, app.CreateConversationCommand{
		UserID: owner, Title: "Panjang",
	})

	repo := chatpg.NewRepository(h.pool)
	for i := range 30 {
		m, err := domain.NewMessage(view.Conversation.ID, domain.RoleUser,
			"pesan "+string(rune('a'+i%26)), h.now.Add(time.Duration(i)*time.Minute))
		if err != nil {
			t.Fatalf("NewMessage: %v", err)
		}
		if err := repo.CreateMessage(h.ctx, m); err != nil {
			t.Fatalf("CreateMessage %d: %v", i, err)
		}
	}

	window, err := h.svc.ConversationContext(h.ctx, view.Conversation.ID)
	if err != nil {
		t.Fatalf("ConversationContext: %v", err)
	}
	if len(window) != domain.ContextWindow {
		t.Fatalf("the window holds %d messages, want %d", len(window), domain.ContextWindow)
	}

	for i := 1; i < len(window); i++ {
		if window[i].CreatedAt.Before(window[i-1].CreatedAt) {
			t.Fatalf("message %d is older than the one before it", i)
		}
	}
	if !window[len(window)-1].CreatedAt.Equal(h.now.Add(29 * time.Minute)) {
		t.Fatalf("the newest message in the window is from %v", window[len(window)-1].CreatedAt)
	}
	if !window[0].CreatedAt.Equal(h.now.Add(10 * time.Minute)) {
		t.Fatalf("the window starts at %v, want the 11th message", window[0].CreatedAt)
	}
}

// TestAnEmptyMessageIsRefusedByTheDatabaseToo keeps the invariant enforced even
// when a path bypasses the domain.
func TestAnEmptyMessageIsRefusedByTheDatabaseToo(t *testing.T) {
	h := setup(t)

	view, _ := h.svc.CreateConversation(h.ctx, app.CreateConversationCommand{
		UserID: h.user(), Title: "x",
	})

	id, _ := domain.NewID()
	_, err := h.pool.Exec(h.ctx, `
		INSERT INTO chat_messages (id, conversation_id, role, content)
		VALUES ($1, $2, 'user', '   ')`,
		id.String(), view.Conversation.ID.String())

	if err == nil {
		t.Fatal("the database accepted a blank message")
	}
}
