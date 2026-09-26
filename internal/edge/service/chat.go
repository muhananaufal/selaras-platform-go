package service

import (
	"context"

	"connectrpc.com/connect"

	chatv1 "github.com/muhananaufal/selaras-platform-go/gen/chat/v1"
	edgev1 "github.com/muhananaufal/selaras-platform-go/gen/edge/v1"
	"github.com/muhananaufal/selaras-platform-go/gen/edge/v1/edgev1connect"
	"github.com/muhananaufal/selaras-platform-go/internal/edge/rpcerr"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/watchhint"
)

// Chat implements edge.v1.Chat.
type Chat struct {
	chat  chatv1.ChatClient
	watch WatchConfig
}

var _ edgev1connect.ChatHandler = (*Chat)(nil)

func NewChat(chat chatv1.ChatClient, watch WatchConfig) *Chat {
	return &Chat{chat: chat, watch: watch}
}

func (h *Chat) ListConversations(
	ctx context.Context, req *edgev1.ListConversationsRequest,
) (*edgev1.ListConversationsResponse, error) {
	c, err := claims(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := h.chat.ListConversations(ctx, &chatv1.ListConversationsRequest{
		UserId: c.UserID.String(),
		Page:   pageFrom(req.GetPage()),
	})
	if err != nil {
		return nil, rpcerr.FromUpstream(ctx, edgev1connect.ChatListConversationsProcedure, err)
	}
	out := &edgev1.ListConversationsResponse{Page: pageOut(resp.GetPage())}
	for _, conv := range resp.GetConversations() {
		out.Conversations = append(out.Conversations, conversationView(conv))
	}
	return out, nil
}

// CreateConversation returns job_id when a first message was sent: the reply
// comes later.
func (h *Chat) CreateConversation(
	ctx context.Context, req *edgev1.CreateConversationRequest,
) (*edgev1.CreateConversationResponse, error) {
	c, err := claims(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := h.chat.CreateConversation(ctx, &chatv1.CreateConversationRequest{
		UserId:         c.UserID.String(),
		Message:        req.GetMessage(),
		IdempotencyKey: idempotencyKey(ctx, c),
	})
	if err != nil {
		return nil, rpcerr.FromUpstream(ctx, edgev1connect.ChatCreateConversationProcedure, err)
	}
	return &edgev1.CreateConversationResponse{
		Conversation: conversationView(resp.GetConversation()),
		JobId:        resp.GetJobId(),
	}, nil
}

func (h *Chat) GetConversation(
	ctx context.Context, req *edgev1.GetConversationRequest,
) (*edgev1.GetConversationResponse, error) {
	p, err := h.conversation(ctx, req.GetSlug(), req.GetPage(),
		edgev1connect.ChatGetConversationProcedure)
	if err != nil {
		return nil, err
	}
	return &edgev1.GetConversationResponse{Conversation: p.Conversation, Messages: p.Messages, Page: p.Page}, nil
}

func (h *Chat) UpdateConversationTitle(
	ctx context.Context, req *edgev1.UpdateConversationTitleRequest,
) (*edgev1.UpdateConversationTitleResponse, error) {
	c, err := claims(ctx)
	if err != nil {
		return nil, err
	}
	if err := invalid(required(field("slug", req.GetSlug()), field("title", req.GetTitle()))); err != nil {
		return nil, err
	}
	resp, err := h.chat.UpdateConversationTitle(ctx, &chatv1.UpdateConversationTitleRequest{
		Slug: req.GetSlug(), UserId: c.UserID.String(), Title: req.GetTitle(),
	})
	if err != nil {
		return nil, rpcerr.FromUpstream(ctx, edgev1connect.ChatUpdateConversationTitleProcedure, err)
	}
	return &edgev1.UpdateConversationTitleResponse{Conversation: conversationView(resp.GetConversation())}, nil
}

func (h *Chat) DeleteConversation(
	ctx context.Context, req *edgev1.DeleteConversationRequest,
) (*edgev1.DeleteConversationResponse, error) {
	user, err := slugCall(ctx, req.GetSlug())
	if err != nil {
		return nil, err
	}
	if _, err := h.chat.DeleteConversation(ctx, &chatv1.DeleteConversationRequest{
		Slug: req.GetSlug(), UserId: user,
	}); err != nil {
		return nil, rpcerr.FromUpstream(ctx, edgev1connect.ChatDeleteConversationProcedure, err)
	}
	return &edgev1.DeleteConversationResponse{}, nil
}

func (h *Chat) SendMessage(ctx context.Context, req *edgev1.SendMessageRequest) (*edgev1.SendMessageResponse, error) {
	c, err := claims(ctx)
	if err != nil {
		return nil, err
	}
	if err := invalid(required(
		field("conversationSlug", req.GetConversationSlug()), field("message", req.GetMessage()),
	)); err != nil {
		return nil, err
	}
	resp, err := h.chat.SendMessage(ctx, &chatv1.SendMessageRequest{
		ConversationSlug: req.GetConversationSlug(),
		UserId:           c.UserID.String(),
		Message:          req.GetMessage(),
		IdempotencyKey:   idempotencyKey(ctx, c),
	})
	if err != nil {
		return nil, rpcerr.FromUpstream(ctx, edgev1connect.ChatSendMessageProcedure, err)
	}
	return &edgev1.SendMessageResponse{Message: chatMessageView(resp.GetMessage())}, nil
}

// WatchConversation ends once the newest message is the model's.
//
// chat-svc pages messages OLDEST first with an offset token
// (conversation_repo.go ListMessages), so the first page of a long
// conversation never contains the reply being waited for. The stream
// therefore walks to the last page once, remembers the token that produced
// it, and afterwards reads only from there - messages are only appended, so
// that token stays valid. Each message on the stream carries the newest page,
// not the whole history.
func (h *Chat) WatchConversation(
	ctx context.Context, req *edgev1.WatchConversationRequest,
	stream *connect.ServerStream[edgev1.WatchConversationResponse],
) error {
	if err := invalid(required(field("slug", req.GetSlug()))); err != nil {
		return err
	}

	tail := ""
	type result = watchResult[*edgev1.WatchConversationResponse]
	return watch(ctx, h.watch, stream, func(ctx context.Context) (result, error) {
		last, token, err := h.lastPage(ctx, req.GetSlug(), tail)
		if err != nil {
			return result{}, err
		}
		tail = token
		return result{
			Msg:  &edgev1.WatchConversationResponse{Conversation: last.Conversation, Messages: last.Messages},
			Done: latestIsModel(last.Messages),
			Key:  watchhint.Key{Type: watchhint.TypeConversation, ID: last.ID},
		}, nil
	})
}

// watchPageSize is the largest page chat-svc serves (domain.Page.Normalise).
const watchPageSize = 100

// maxPagesPerTick bounds one walk: 100 pages of 100 is far beyond any real
// conversation, and a bound means a corrupt token cannot spin forever.
const maxPagesPerTick = 100

// lastPage walks from the token start to the last non-empty page and returns
// it together with the token that produced it.
func (h *Chat) lastPage(ctx context.Context, slug, start string) (conversationPage, string, error) {
	var (
		last conversationPage
		kept = start
	)
	token := start
	for range maxPagesPerTick {
		p, err := h.conversation(ctx, slug,
			&edgev1.PageRequest{PageSize: watchPageSize, PageToken: token},
			edgev1connect.ChatWatchConversationProcedure)
		if err != nil {
			return conversationPage{}, "", err
		}
		last.Conversation, last.ID = p.Conversation, p.ID
		if len(p.Messages) > 0 {
			last.Messages, kept = p.Messages, token
		}
		if p.Page.GetNextPageToken() == "" {
			break
		}
		token = p.Page.GetNextPageToken()
	}
	return last, kept, nil
}

// conversationPage is one page of a conversation as the gateway shows it,
// plus the conversation id - the aggregate its watch hints name.
type conversationPage struct {
	Conversation *edgev1.Conversation
	Messages     []*edgev1.ChatMessage
	Page         *edgev1.PageResponse
	ID           string
}

func (h *Chat) conversation(
	ctx context.Context, slug string, page *edgev1.PageRequest, procedure string,
) (conversationPage, error) {
	user, err := slugCall(ctx, slug)
	if err != nil {
		return conversationPage{}, err
	}
	resp, err := h.chat.GetConversation(ctx, &chatv1.GetConversationRequest{
		Slug: slug, UserId: user, Page: pageFrom(page),
	})
	if err != nil {
		return conversationPage{}, rpcerr.FromUpstream(ctx, procedure, err)
	}
	messages := make([]*edgev1.ChatMessage, 0, len(resp.GetMessages()))
	for _, m := range resp.GetMessages() {
		messages = append(messages, chatMessageView(m))
	}
	return conversationPage{
		Conversation: conversationView(resp.GetConversation()),
		Messages:     messages,
		Page:         pageOut(resp.GetPage()),
		ID:           resp.GetConversation().GetId(),
	}, nil
}

// latestIsModel reports whether the newest message is the model's.
//
// The page order is decided by chat-svc, so both ends are checked through the
// timestamps rather than assuming one direction.
func latestIsModel(messages []*edgev1.ChatMessage) bool {
	var latest *edgev1.ChatMessage
	for _, m := range messages {
		if latest == nil || !m.GetCreatedAt().AsTime().Before(latest.GetCreatedAt().AsTime()) {
			latest = m
		}
	}
	return latest != nil && latest.GetRole() == chatv1.MessageRole_MESSAGE_ROLE_MODEL
}

func conversationView(c *chatv1.Conversation) *edgev1.Conversation {
	if c == nil {
		return nil
	}
	return &edgev1.Conversation{
		Slug:      c.GetSlug(),
		Title:     c.GetTitle(),
		CreatedAt: ts(c.GetTimestamps().GetCreatedAt()),
		UpdatedAt: ts(c.GetTimestamps().GetUpdatedAt()),
	}
}

func chatMessageView(m *chatv1.ChatMessage) *edgev1.ChatMessage {
	if m == nil {
		return nil
	}
	return &edgev1.ChatMessage{
		Role:      m.GetRole(),
		Content:   jsonValue(m.GetContentJson()),
		CreatedAt: ts(m.GetTimestamps().GetCreatedAt()),
	}
}
