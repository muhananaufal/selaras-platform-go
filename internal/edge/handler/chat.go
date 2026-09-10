package handler

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	chatv1 "github.com/muhananaufal/selaras-platform-go/gen/chat/v1"
	commonv1 "github.com/muhananaufal/selaras-platform-go/gen/common/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/edge/httperr"
	"github.com/muhananaufal/selaras-platform-go/internal/edge/middleware"
)

// Chat melayani enam endpoint percakapan asisten umum.
type Chat struct {
	chat chatv1.ChatClient
}

func NewChat(chat chatv1.ChatClient) *Chat { return &Chat{chat: chat} }

// The shape the REST contract promises.
type conversationView struct {
	Slug      string `json:"slug"`
	Title     string `json:"title"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type chatMessageView struct {
	Role      string          `json:"role"`
	Content   json.RawMessage `json:"content"`
	CreatedAt string          `json:"created_at"`
}

// pageView passes the next-page token through as it is.
//
// The token is OPAQUE: the client sends it back without reading it, and what is
// inside may change without changing the client.
type pageView struct {
	NextPageToken string `json:"next_page_token,omitempty"`
}

// Index returns the caller's conversations.
func (h *Chat) Index(c *gin.Context) {
	claims, ok := middleware.ClaimsFrom(c)
	if !ok {
		httperr.Write(c, http.StatusUnauthorized, httperr.CodeUnauthenticated, "Unauthenticated.")
		return
	}

	resp, err := h.chat.ListConversations(c.Request.Context(), &chatv1.ListConversationsRequest{
		UserId: claims.UserID.String(),
		Page:   pageRequestFrom(c),
	})
	if err != nil {
		httperr.FromGRPC(c, err)
		return
	}

	items := make([]conversationView, 0, len(resp.GetConversations()))
	for _, conversation := range resp.GetConversations() {
		items = append(items, viewOfConversation(conversation))
	}

	writeData(c, http.StatusOK, struct {
		Conversations []conversationView `json:"conversations"`
		Page          pageView           `json:"page"`
	}{items, pageView{NextPageToken: resp.GetPage().GetNextPageToken()}})
}

// Store creates a new conversation.
//
// It answers 202 when a message is included - the reply comes later - and 201
// when the conversation is created empty, because there is nothing to wait
// for.
func (h *Chat) Store(c *gin.Context) {
	claims, ok := middleware.ClaimsFrom(c)
	if !ok {
		httperr.Write(c, http.StatusUnauthorized, httperr.CodeUnauthenticated, "Unauthenticated.")
		return
	}

	var body struct {
		Message string `json:"message"`
	}
	if !bind(c, &body) {
		return
	}

	req := &chatv1.CreateConversationRequest{
		UserId:  claims.UserID.String(),
		Message: body.Message,
	}
	req.IdempotencyKey = idempotencyKeyFor(claims, c.GetHeader("Idempotency-Key"))

	resp, err := h.chat.CreateConversation(c.Request.Context(), req)
	if err != nil {
		httperr.FromGRPC(c, err)
		return
	}

	code := http.StatusCreated
	if resp.GetJobId() != "" {
		code = http.StatusAccepted
	}
	writeData(c, code, viewOfConversation(resp.GetConversation()))
}

// Show memuat percakapan beserta riwayatnya.
func (h *Chat) Show(c *gin.Context) {
	claims, ok := middleware.ClaimsFrom(c)
	if !ok {
		httperr.Write(c, http.StatusUnauthorized, httperr.CodeUnauthenticated, "Unauthenticated.")
		return
	}

	resp, err := h.chat.GetConversation(c.Request.Context(), &chatv1.GetConversationRequest{
		Slug:   c.Param("slug"),
		UserId: claims.UserID.String(),
		Page:   pageRequestFrom(c),
	})
	if err != nil {
		httperr.FromGRPC(c, err)
		return
	}

	messages := make([]chatMessageView, 0, len(resp.GetMessages()))
	for _, m := range resp.GetMessages() {
		messages = append(messages, viewOfChatMessage(m))
	}

	writeData(c, http.StatusOK, struct {
		conversationView
		Messages []chatMessageView `json:"messages"`
		Page     pageView          `json:"page"`
	}{
		viewOfConversation(resp.GetConversation()),
		messages,
		pageView{NextPageToken: resp.GetPage().GetNextPageToken()},
	})
}

// Update mengubah judul percakapan.
func (h *Chat) Update(c *gin.Context) {
	claims, ok := middleware.ClaimsFrom(c)
	if !ok {
		httperr.Write(c, http.StatusUnauthorized, httperr.CodeUnauthenticated, "Unauthenticated.")
		return
	}

	var body struct {
		Title string `json:"title" binding:"required"`
	}
	if !bind(c, &body) {
		return
	}

	resp, err := h.chat.UpdateConversationTitle(c.Request.Context(),
		&chatv1.UpdateConversationTitleRequest{
			Slug: c.Param("slug"), UserId: claims.UserID.String(), Title: body.Title,
		})
	if err != nil {
		httperr.FromGRPC(c, err)
		return
	}
	writeData(c, http.StatusOK, viewOfConversation(resp.GetConversation()))
}

// SendMessage writes a message and requests its reply.
func (h *Chat) SendMessage(c *gin.Context) {
	claims, ok := middleware.ClaimsFrom(c)
	if !ok {
		httperr.Write(c, http.StatusUnauthorized, httperr.CodeUnauthenticated, "Unauthenticated.")
		return
	}

	var body struct {
		Message string `json:"message" binding:"required"`
	}
	if !bind(c, &body) {
		return
	}

	req := &chatv1.SendMessageRequest{
		ConversationSlug: c.Param("slug"),
		UserId:           claims.UserID.String(),
		Message:          body.Message,
	}
	req.IdempotencyKey = idempotencyKeyFor(claims, c.GetHeader("Idempotency-Key"))

	resp, err := h.chat.SendMessage(c.Request.Context(), req)
	if err != nil {
		httperr.FromGRPC(c, err)
		return
	}

	// 202: the model's reply comes later, through the same conversation.
	writeData(c, http.StatusAccepted, viewOfChatMessage(resp.GetMessage()))
}

// Destroy deletes a conversation together with its messages.
func (h *Chat) Destroy(c *gin.Context) {
	claims, ok := middleware.ClaimsFrom(c)
	if !ok {
		httperr.Write(c, http.StatusUnauthorized, httperr.CodeUnauthenticated, "Unauthenticated.")
		return
	}

	if _, err := h.chat.DeleteConversation(c.Request.Context(),
		&chatv1.DeleteConversationRequest{
			Slug: c.Param("slug"), UserId: claims.UserID.String(),
		}); err != nil {
		httperr.FromGRPC(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// pageRequestFrom reads the page request from the query string.
//
// An unreadable size becomes zero, and zero means "use the service default"
// - not "no content". Refusing the request over one mistyped parameter only
// makes the list stop working.
func pageRequestFrom(c *gin.Context) *commonv1.PageRequest {
	out := &commonv1.PageRequest{PageToken: c.Query("page_token")}

	if raw := c.Query("page_size"); raw != "" {
		// ParseInt with a 32-bit width, not Atoi followed by a conversion: the
		// latter wraps large numbers into small or negative values on 64-bit
		// platforms, and "page_size=4294967297" becomes 1 without anyone knowing.
		if size, err := strconv.ParseInt(raw, 10, 32); err == nil && size > 0 {
			out.PageSize = int32(size)
		}
	}
	return out
}

func viewOfConversation(c *chatv1.Conversation) conversationView {
	if c == nil {
		return conversationView{}
	}

	out := conversationView{Slug: c.GetSlug(), Title: c.GetTitle()}
	if ts := c.GetTimestamps().GetCreatedAt(); ts != nil {
		out.CreatedAt = ts.AsTime().Format(time.RFC3339)
	}
	if ts := c.GetTimestamps().GetUpdatedAt(); ts != nil {
		out.UpdatedAt = ts.AsTime().Format(time.RFC3339)
	}
	return out
}

func viewOfChatMessage(m *chatv1.ChatMessage) chatMessageView {
	if m == nil {
		return chatMessageView{}
	}

	out := chatMessageView{Role: chatRoleName(m.GetRole())}
	if ts := m.GetTimestamps().GetCreatedAt(); ts != nil {
		out.CreatedAt = ts.AsTime().Format(time.RFC3339)
	}

	// Checked first: bytes that are not JSON would make the WHOLE response
	// unparseable for the client, so one corrupt row takes the endpoint down.
	if raw := m.GetContentJson(); raw != "" && json.Valid([]byte(raw)) {
		out.Content = json.RawMessage(raw)
	}
	return out
}

func chatRoleName(r chatv1.MessageRole) string {
	switch r {
	case chatv1.MessageRole_MESSAGE_ROLE_USER:
		return roleNameUser
	case chatv1.MessageRole_MESSAGE_ROLE_MODEL:
		return roleNameModel
	default:
		return ""
	}
}
