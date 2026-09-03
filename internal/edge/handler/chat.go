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

// Bentuk yang dijanjikan kontrak REST.
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

// pageView menyampaikan token halaman berikutnya apa adanya.
//
// Token itu OPAQUE: klien mengirimkannya kembali tanpa membacanya, dan apa yang
// ada di dalamnya boleh berubah tanpa mengubah klien.
type pageView struct {
	NextPageToken string `json:"next_page_token,omitempty"`
}

// Index mengembalikan daftar percakapan milik pemanggil.
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

// Store membuat percakapan baru.
//
// Ia menjawab 202 bila pesannya ikut - balasannya datang belakangan - dan 201
// bila percakapannya dibuat kosong, karena tidak ada yang perlu ditunggu.
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
	if key := c.GetHeader("Idempotency-Key"); key != "" {
		req.IdempotencyKey = &commonv1.IdempotencyKey{Value: key}
	}

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

// SendMessage menulis pesan dan meminta balasannya.
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
	if key := c.GetHeader("Idempotency-Key"); key != "" {
		req.IdempotencyKey = &commonv1.IdempotencyKey{Value: key}
	}

	resp, err := h.chat.SendMessage(c.Request.Context(), req)
	if err != nil {
		httperr.FromGRPC(c, err)
		return
	}

	// 202: balasan model datang belakangan, lewat percakapan yang sama.
	writeData(c, http.StatusAccepted, viewOfChatMessage(resp.GetMessage()))
}

// Destroy menghapus percakapan beserta pesannya.
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

// pageRequestFrom membaca permintaan halaman dari query string.
//
// Ukuran yang tidak bisa dibaca menjadi nol, dan nol berarti "pakai bawaan
// service" - bukan "tidak ada isinya". Menolak permintaannya karena satu
// parameter salah ketik hanya membuat daftar berhenti bekerja.
func pageRequestFrom(c *gin.Context) *commonv1.PageRequest {
	out := &commonv1.PageRequest{PageToken: c.Query("page_token")}

	if raw := c.Query("page_size"); raw != "" {
		// ParseInt dengan lebar 32 bit, bukan Atoi lalu dikonversi: yang kedua
		// membungkus angka besar menjadi nilai kecil atau negatif di platform
		// 64-bit, dan "page_size=4294967297" menjadi 1 tanpa ada yang tahu.
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

	// Diperiksa dulu: byte yang bukan JSON akan membuat SELURUH respons tidak
	// bisa di-parse klien, sehingga satu baris rusak menjatuhkan endpoint-nya.
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
