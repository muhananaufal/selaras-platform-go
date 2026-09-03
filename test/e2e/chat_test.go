package e2e_test

import (
	"net/http"
	"testing"
	"time"
)

// TestAChatConversationRunsFromCreationToDeletion adalah gate F5-10.
//
// Buat percakapan -> kirim pesan -> balasan tiba -> hapus. Setiap langkah lewat
// HTTP, dan balasan itu menyeberangi gateway, chat-svc, Kafka, llm-worker, dan
// kembali.
func TestAChatConversationRunsFromCreationToDeletion(t *testing.T) {
	c := newClient(t)
	c.register()

	// 1. Percakapan dibuat BERSAMA pesan pertamanya. Jawabannya 202: balasan
	//    model datang belakangan.
	code, body := c.do(http.MethodPost, "/api/v1/chat/conversations", map[string]any{
		"message": "Apakah kopi berpengaruh pada tekanan darah saya?",
	})
	if code != http.StatusAccepted {
		t.Fatalf("creating a conversation with a message answered %d: %v", code, body)
	}

	slug, _ := dig(body, "data", "slug").(string)
	if slug == "" {
		t.Fatalf("the conversation has no slug: %v", body)
	}

	// Judulnya diturunkan dari pesan pertama, beserta penanda pemotongan (D12).
	title, _ := dig(body, "data", "title").(string)
	if title != "Apakah kopi berpengaruh pada tekanan darah sa..." {
		t.Fatalf("the derived title is %q", title)
	}

	// 2. Balasan model tiba.
	c.waitForModelReply(slug, 90*time.Second)

	// 3. Pesan kedua, dan balasannya juga tiba - kunci idempotensi per pesan
	//    yang membuatnya tidak dilewati sebagai duplikat.
	code, sent := c.do(http.MethodPost,
		"/api/v1/chat/conversations/"+slug+"/messages",
		map[string]any{"message": "Berapa cangkir yang aman?"})
	if code != http.StatusAccepted {
		t.Fatalf("sending a message answered %d: %v", code, sent)
	}

	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if c.countModelReplies(slug) >= 2 {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if got := c.countModelReplies(slug); got < 2 {
		t.Fatalf("only %d model replies arrived; the second message was skipped as a duplicate", got)
	}

	// 4. Judulnya diganti.
	code, renamed := c.do(http.MethodPatch, "/api/v1/chat/conversations/"+slug,
		map[string]any{"title": "Soal kopi"})
	if code != http.StatusOK {
		t.Fatalf("renaming answered %d: %v", code, renamed)
	}
	if got, _ := dig(renamed, "data", "title").(string); got != "Soal kopi" {
		t.Fatalf("the title came back as %q", got)
	}

	// 5. Dihapus, dan hilang.
	if code, _ := c.do(http.MethodDelete, "/api/v1/chat/conversations/"+slug, nil); code != http.StatusNoContent {
		t.Fatalf("deleting answered %d, want 204", code)
	}
	if code, _ := c.do(http.MethodGet, "/api/v1/chat/conversations/"+slug, nil); code != http.StatusNotFound {
		t.Fatalf("the conversation survived deletion: %d", code)
	}
}

// TestAnEmptyConversationQueuesNothing menjaga tombol "mulai baru".
func TestAnEmptyConversationQueuesNothing(t *testing.T) {
	c := newClient(t)
	c.register()

	// 201, bukan 202: tidak ada yang perlu ditunggu.
	code, body := c.do(http.MethodPost, "/api/v1/chat/conversations", map[string]any{})
	if code != http.StatusCreated {
		t.Fatalf("creating an empty conversation answered %d, want 201: %v", code, body)
	}

	slug, _ := dig(body, "data", "slug").(string)
	if title, _ := dig(body, "data", "title").(string); title != "Percakapan Baru" {
		t.Fatalf("an empty conversation is titled %q", title)
	}

	// Diberi waktu, lalu diperiksa: tidak ada balasan yang datang untuk pesan
	// yang tidak pernah ada.
	time.Sleep(8 * time.Second)
	if got := c.countModelReplies(slug); got != 0 {
		t.Fatalf("%d model replies arrived for a conversation with no message", got)
	}
}

// TestTheConversationListIsPagedAndPrivate adalah F5-04 lewat jalur yang
// sesungguhnya.
func TestTheConversationListIsPagedAndPrivate(t *testing.T) {
	c := newClient(t)
	c.register()

	for range 3 {
		if code, _ := c.do(http.MethodPost, "/api/v1/chat/conversations", map[string]any{}); code != http.StatusCreated {
			t.Fatalf("creating a conversation answered %d", code)
		}
	}

	code, first := c.do(http.MethodGet, "/api/v1/chat/conversations?page_size=2", nil)
	if code != http.StatusOK {
		t.Fatalf("listing answered %d: %v", code, first)
	}

	items, _ := dig(first, "data", "conversations").([]any)
	if len(items) != 2 {
		t.Fatalf("the first page holds %d conversations, want 2", len(items))
	}

	// Token halaman berikutnya ada, dan membawa ke sisa daftarnya.
	token, _ := dig(first, "data", "page", "next_page_token").(string)
	if token == "" {
		t.Fatalf("the first page carries no next token: %v", first)
	}

	code, second := c.do(http.MethodGet,
		"/api/v1/chat/conversations?page_size=2&page_token="+token, nil)
	if code != http.StatusOK {
		t.Fatalf("the second page answered %d", code)
	}
	rest, _ := dig(second, "data", "conversations").([]any)
	if len(rest) != 1 {
		t.Fatalf("the second page holds %d conversations, want 1", len(rest))
	}

	// Halaman terakhir TIDAK membawa token: kosongnya adalah tanda berhenti,
	// dan token yang selalu ada membuat klien meminta halaman kosong selamanya.
	if last, _ := dig(second, "data", "page", "next_page_token").(string); last != "" {
		t.Fatalf("the last page still carries a next token: %q", last)
	}

	// Dan orang lain tidak melihat satu pun.
	stranger := newClient(t)
	stranger.register()

	code, theirs := stranger.do(http.MethodGet, "/api/v1/chat/conversations", nil)
	if code != http.StatusOK {
		t.Fatalf("listing answered %d", code)
	}
	if items, _ := dig(theirs, "data", "conversations").([]any); len(items) != 0 {
		t.Fatalf("a stranger sees %d of someone else's conversations", len(items))
	}
}

// TestSomeoneElsesConversationIsNotFound adalah S9 lewat tiga lapisan.
func TestSomeoneElsesConversationIsNotFound(t *testing.T) {
	owner := newClient(t)
	owner.register()

	code, body := owner.do(http.MethodPost, "/api/v1/chat/conversations",
		map[string]any{"message": "halo"})
	if code != http.StatusAccepted {
		t.Fatalf("creating a conversation answered %d: %v", code, body)
	}
	slug, _ := dig(body, "data", "slug").(string)

	stranger := newClient(t)
	stranger.register()

	for _, probe := range []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodGet, "/api/v1/chat/conversations/" + slug, nil},
		{http.MethodPatch, "/api/v1/chat/conversations/" + slug, map[string]any{"title": "milik saya"}},
		{http.MethodPost, "/api/v1/chat/conversations/" + slug + "/messages", map[string]any{"message": "halo"}},
		{http.MethodDelete, "/api/v1/chat/conversations/" + slug, nil},
	} {
		if code, _ := stranger.do(probe.method, probe.path, probe.body); code != http.StatusNotFound {
			t.Errorf("%s %s answered %d, want 404", probe.method, probe.path, code)
		}
	}

	// Dan percakapan yang memang tidak ada menjawab sama.
	if code, _ := stranger.do(http.MethodGet, "/api/v1/chat/conversations/tidakadaslugini", nil); code != http.StatusNotFound {
		t.Errorf("a missing conversation answered %d, want 404", code)
	}
}

// waitForModelReply menunggu balasan pertama dari model.
func (c *client) waitForModelReply(slug string, timeout time.Duration) {
	c.t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if c.countModelReplies(slug) > 0 {
			return
		}
		time.Sleep(2 * time.Second)
	}
	c.t.Fatalf("the model never replied within %v", timeout)
}

// countModelReplies menghitung pesan berperan "model" di sebuah percakapan.
func (c *client) countModelReplies(slug string) int {
	c.t.Helper()

	code, body := c.do(http.MethodGet, "/api/v1/chat/conversations/"+slug, nil)
	if code != http.StatusOK {
		c.t.Fatalf("reading the conversation answered %d: %v", code, body)
	}

	messages, _ := dig(body, "data", "messages").([]any)

	var replies int
	for _, rm := range messages {
		message, _ := rm.(map[string]any)
		if role, _ := message["role"].(string); role == "model" {
			replies++
		}
	}
	return replies
}
