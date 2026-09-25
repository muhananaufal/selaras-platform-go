package e2e_test

import (
	"testing"
	"time"

	"connectrpc.com/connect"

	chatv1 "github.com/muhananaufal/selaras-platform-go/gen/chat/v1"
	edgev1 "github.com/muhananaufal/selaras-platform-go/gen/edge/v1"
)

// TestAChatConversationRunsFromCreationToDeletion is gate F5-10.
//
// Create a conversation -> send a message -> the reply arrives -> delete. The
// reply crosses the gateway, chat-svc, Kafka, llm-worker, and back.
func TestAChatConversationRunsFromCreationToDeletion(t *testing.T) {
	c := newClient(t)
	c.register()

	// 1. Created TOGETHER with its first message; job_id says a reply is
	//    being produced.
	created, err := c.chat.CreateConversation(c.ctx(), &edgev1.CreateConversationRequest{
		Message: "Apakah kopi berpengaruh pada tekanan darah saya?",
	})
	if err != nil {
		t.Fatalf("creating a conversation with a message: %v", err)
	}
	slug := created.GetConversation().GetSlug()
	if slug == "" || created.GetJobId() == "" {
		t.Fatalf("want a slug and a job id: %v", created)
	}
	// The title is derived from the first message, with the truncation
	// marker (D12).
	if got := created.GetConversation().GetTitle(); got != "Apakah kopi berpengaruh pada tekanan darah sa..." {
		t.Fatalf("the derived title is %q", got)
	}

	// 2. The model's reply arrives on the WatchConversation stream.
	c.watchReplies(slug, 1, 90*time.Second)

	// 3. A second message, and its reply arrives too - the per-message
	//    idempotency key is what keeps it from being skipped as a duplicate.
	if _, err := c.chat.SendMessage(c.ctx(), &edgev1.SendMessageRequest{
		ConversationSlug: slug, Message: "Berapa cangkir yang aman?",
	}); err != nil {
		t.Fatalf("sending a message: %v", err)
	}
	c.watchReplies(slug, 2, 90*time.Second)

	// 4. The title is changed.
	renamed, err := c.chat.UpdateConversationTitle(c.ctx(), &edgev1.UpdateConversationTitleRequest{
		Slug: slug, Title: "Soal kopi",
	})
	if err != nil {
		t.Fatalf("renaming: %v", err)
	}
	if got := renamed.GetConversation().GetTitle(); got != "Soal kopi" {
		t.Fatalf("the title came back as %q", got)
	}

	// 5. Deleted, and gone.
	if _, err := c.chat.DeleteConversation(c.ctx(), &edgev1.DeleteConversationRequest{Slug: slug}); err != nil {
		t.Fatalf("deleting: %v", err)
	}
	_, err = c.chat.GetConversation(c.ctx(), &edgev1.GetConversationRequest{Slug: slug})
	expectCode(t, "reading a deleted conversation", err, connect.CodeNotFound)
}

// TestAnEmptyConversationQueuesNothing guards the "start new" button.
func TestAnEmptyConversationQueuesNothing(t *testing.T) {
	c := newClient(t)
	c.register()

	created, err := c.chat.CreateConversation(c.ctx(), &edgev1.CreateConversationRequest{})
	if err != nil {
		t.Fatalf("creating an empty conversation: %v", err)
	}
	// No job: there is nothing to wait for.
	if created.GetJobId() != "" {
		t.Fatalf("an empty conversation queued job %q", created.GetJobId())
	}
	if got := created.GetConversation().GetTitle(); got != "Percakapan Baru" {
		t.Fatalf("an empty conversation is titled %q", got)
	}

	// Given time, then checked: no reply arrives for a message that never
	// existed.
	time.Sleep(8 * time.Second)
	if got := c.countModelReplies(created.GetConversation().GetSlug()); got != 0 {
		t.Fatalf("%d model replies arrived for a conversation with no message", got)
	}
}

// TestTheConversationListIsPagedAndPrivate is F5-04 through the real path.
func TestTheConversationListIsPagedAndPrivate(t *testing.T) {
	c := newClient(t)
	c.register()

	for range 3 {
		if _, err := c.chat.CreateConversation(c.ctx(), &edgev1.CreateConversationRequest{}); err != nil {
			t.Fatalf("creating a conversation: %v", err)
		}
	}

	first, err := c.chat.ListConversations(c.ctx(), &edgev1.ListConversationsRequest{
		Page: &edgev1.PageRequest{PageSize: 2},
	})
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	if n := len(first.GetConversations()); n != 2 {
		t.Fatalf("the first page holds %d conversations, want 2", n)
	}
	token := first.GetPage().GetNextPageToken()
	if token == "" {
		t.Fatalf("the first page carries no next token: %v", first)
	}

	second, err := c.chat.ListConversations(c.ctx(), &edgev1.ListConversationsRequest{
		Page: &edgev1.PageRequest{PageSize: 2, PageToken: token},
	})
	if err != nil {
		t.Fatalf("the second page: %v", err)
	}
	if n := len(second.GetConversations()); n != 1 {
		t.Fatalf("the second page holds %d conversations, want 1", n)
	}
	// The last page carries NO token: its absence is the stop signal.
	if last := second.GetPage().GetNextPageToken(); last != "" {
		t.Fatalf("the last page still carries a next token: %q", last)
	}

	// And someone else sees none at all.
	stranger := newClient(t)
	stranger.register()
	theirs, err := stranger.chat.ListConversations(stranger.ctx(), &edgev1.ListConversationsRequest{})
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	if n := len(theirs.GetConversations()); n != 0 {
		t.Fatalf("a stranger sees %d of someone else's conversations", n)
	}
}

// TestSomeoneElsesConversationIsNotFound is S9 through three layers.
func TestSomeoneElsesConversationIsNotFound(t *testing.T) {
	owner := newClient(t)
	owner.register()
	created, err := owner.chat.CreateConversation(owner.ctx(), &edgev1.CreateConversationRequest{Message: "halo"})
	if err != nil {
		t.Fatalf("creating a conversation: %v", err)
	}
	slug := created.GetConversation().GetSlug()

	stranger := newClient(t)
	stranger.register()
	ctx := stranger.ctx()

	_, err = stranger.chat.GetConversation(ctx, &edgev1.GetConversationRequest{Slug: slug})
	expectCode(t, "GetConversation", err, connect.CodeNotFound)
	_, err = stranger.chat.UpdateConversationTitle(ctx, &edgev1.UpdateConversationTitleRequest{Slug: slug, Title: "milik saya"})
	expectCode(t, "UpdateConversationTitle", err, connect.CodeNotFound)
	_, err = stranger.chat.SendMessage(ctx, &edgev1.SendMessageRequest{ConversationSlug: slug, Message: "halo"})
	expectCode(t, "SendMessage", err, connect.CodeNotFound)
	_, err = stranger.chat.DeleteConversation(ctx, &edgev1.DeleteConversationRequest{Slug: slug})
	expectCode(t, "DeleteConversation", err, connect.CodeNotFound)

	// A stream is guarded the same way as a unary call.
	stream, err := stranger.chat.WatchConversation(ctx, &edgev1.WatchConversationRequest{Slug: slug})
	if err == nil {
		for stream.Receive() {
			t.Fatal("a stranger received a message of someone else's conversation")
		}
		err = stream.Err()
	}
	expectCode(t, "WatchConversation", err, connect.CodeNotFound)

	// And a conversation that really does not exist answers the same.
	_, err = stranger.chat.GetConversation(ctx, &edgev1.GetConversationRequest{Slug: "tidakadaslugini"})
	expectCode(t, "GetConversation on a missing slug", err, connect.CodeNotFound)
}

// watchReplies waits on the WatchConversation stream until at least want
// model replies are on the newest page, reopening the stream when it ends.
func (c *client) watchReplies(slug string, want int, timeout time.Duration) {
	c.t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ctx, cancel := contextUntil(c.t, deadline)
		stream, err := c.chat.WatchConversation(ctx, &edgev1.WatchConversationRequest{Slug: slug})
		if err != nil {
			cancel()
			c.t.Fatalf("opening WatchConversation: %v", err)
		}
		for stream.Receive() {
			if modelReplies(stream.Msg().GetMessages()) >= want {
				cancel()
				return
			}
		}
		cancel()
	}
	c.t.Fatalf("fewer than %d model replies within %v; the message was lost or skipped as a duplicate", want, timeout)
}

// countModelReplies counts the model's messages on the first page.
func (c *client) countModelReplies(slug string) int {
	c.t.Helper()
	resp, err := c.chat.GetConversation(c.ctx(), &edgev1.GetConversationRequest{Slug: slug})
	if err != nil {
		c.t.Fatalf("reading the conversation: %v", err)
	}
	return modelReplies(resp.GetMessages())
}

func modelReplies(messages []*edgev1.ChatMessage) int {
	var n int
	for _, m := range messages {
		if m.GetRole() == chatv1.MessageRole_MESSAGE_ROLE_MODEL {
			n++
		}
	}
	return n
}
