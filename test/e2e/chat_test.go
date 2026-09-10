package e2e_test

import (
	"net/http"
	"testing"
	"time"
)

// TestAChatConversationRunsFromCreationToDeletion is gate F5-10.
//
// Create a conversation -> send a message -> the reply arrives -> delete. Every
// step goes over HTTP, and that reply crosses the gateway, chat-svc, Kafka,
// llm-worker, and back.
func TestAChatConversationRunsFromCreationToDeletion(t *testing.T) {
	c := newClient(t)
	c.register()

	// 1. The conversation is created TOGETHER with its first message. The
	//    answer is 202: the model's reply comes later.
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

	// The title is derived from the first message, with the truncation marker
	// (D12).
	title, _ := dig(body, "data", "title").(string)
	if title != "Apakah kopi berpengaruh pada tekanan darah sa..." {
		t.Fatalf("the derived title is %q", title)
	}

	// 2. The model's reply arrives.
	c.waitForModelReply(slug, 90*time.Second)

	// 3. A second message, and its reply arrives too - the per-message
	//    idempotency key is what keeps it from being skipped as a duplicate.
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

	// 4. The title is changed.
	code, renamed := c.do(http.MethodPatch, "/api/v1/chat/conversations/"+slug,
		map[string]any{"title": "Soal kopi"})
	if code != http.StatusOK {
		t.Fatalf("renaming answered %d: %v", code, renamed)
	}
	if got, _ := dig(renamed, "data", "title").(string); got != "Soal kopi" {
		t.Fatalf("the title came back as %q", got)
	}

	// 5. Deleted, and gone.
	if code, _ := c.do(http.MethodDelete, "/api/v1/chat/conversations/"+slug, nil); code != http.StatusNoContent {
		t.Fatalf("deleting answered %d, want 204", code)
	}
	if code, _ := c.do(http.MethodGet, "/api/v1/chat/conversations/"+slug, nil); code != http.StatusNotFound {
		t.Fatalf("the conversation survived deletion: %d", code)
	}
}

// TestAnEmptyConversationQueuesNothing guards the "start new" button.
func TestAnEmptyConversationQueuesNothing(t *testing.T) {
	c := newClient(t)
	c.register()

	// 201, not 202: there is nothing to wait for.
	code, body := c.do(http.MethodPost, "/api/v1/chat/conversations", map[string]any{})
	if code != http.StatusCreated {
		t.Fatalf("creating an empty conversation answered %d, want 201: %v", code, body)
	}

	slug, _ := dig(body, "data", "slug").(string)
	if title, _ := dig(body, "data", "title").(string); title != "Percakapan Baru" {
		t.Fatalf("an empty conversation is titled %q", title)
	}

	// Given time, then checked: no reply arrives for a message that never
	// existed.
	time.Sleep(8 * time.Second)
	if got := c.countModelReplies(slug); got != 0 {
		t.Fatalf("%d model replies arrived for a conversation with no message", got)
	}
}

// TestTheConversationListIsPagedAndPrivate is F5-04 through the real path.
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

	// The next-page token is present, and leads to the rest of the list.
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

	// The last page carries NO token: its emptiness is the stop signal, and a
	// token that is always present makes the client request empty pages
	// forever.
	if last, _ := dig(second, "data", "page", "next_page_token").(string); last != "" {
		t.Fatalf("the last page still carries a next token: %q", last)
	}

	// And someone else sees none at all.
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

// TestSomeoneElsesConversationIsNotFound is S9 through three layers.
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

	// And a conversation that really does not exist answers the same.
	if code, _ := stranger.do(http.MethodGet, "/api/v1/chat/conversations/tidakadaslugini", nil); code != http.StatusNotFound {
		t.Errorf("a missing conversation answered %d, want 404", code)
	}
}

// waitForModelReply waits for the first reply from the model.
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

// countModelReplies counts the messages with the "model" role in a
// conversation.
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
