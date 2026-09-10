// Package grpc melayani chat.v1.
package grpc

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	chatv1 "github.com/muhananaufal/selaras-platform-go/gen/chat/v1"
	commonv1 "github.com/muhananaufal/selaras-platform-go/gen/common/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/chat/app"
	"github.com/muhananaufal/selaras-platform-go/internal/chat/domain"
)

// Server melayani chat.v1.
type Server struct {
	chatv1.UnimplementedChatServer
	svc *app.Service
}

func NewServer(svc *app.Service) (*Server, error) {
	if svc == nil {
		return nil, errors.New("nil chat service")
	}
	return &Server{svc: svc}, nil
}

var _ chatv1.ChatServer = (*Server)(nil)

func (s *Server) ListConversations(
	ctx context.Context, req *chatv1.ListConversationsRequest,
) (*chatv1.ListConversationsResponse, error) {
	list, err := s.svc.ListConversations(ctx, req.GetUserId(), pageFrom(req.GetPage()))
	if err != nil {
		return nil, toStatus(ctx, "ListConversations", err)
	}

	// An empty slice, not nil: nil becomes `null` in JSON, and a client
	// iterating the list fails instead of showing an empty list.
	out := make([]*chatv1.Conversation, 0, len(list.Items))
	for _, c := range list.Items {
		out = append(out, conversationToProto(c))
	}

	return &chatv1.ListConversationsResponse{
		Conversations: out,
		Page:          pageToProto(list.Page, list.Total),
	}, nil
}

func (s *Server) CreateConversation(
	ctx context.Context, req *chatv1.CreateConversationRequest,
) (*chatv1.CreateConversationResponse, error) {
	view, err := s.svc.CreateConversation(ctx, app.CreateConversationCommand{
		UserID:         req.GetUserId(),
		FirstMessage:   req.GetMessage(),
		IdempotencyKey: req.GetIdempotencyKey().GetValue(),
	})
	if err != nil {
		return nil, toStatus(ctx, "CreateConversation", err)
	}

	out := &chatv1.CreateConversationResponse{
		Conversation: conversationToProto(view.Conversation),
	}
	// job_id is present only when a message is included: an empty conversation
	// queues no job, and returning an id that points at nothing would make the
	// client wait for a reply that was never requested.
	if len(view.Messages) > 0 {
		out.JobId = view.Messages[0].ID.String()
	}
	return out, nil
}

func (s *Server) GetConversation(
	ctx context.Context, req *chatv1.GetConversationRequest,
) (*chatv1.GetConversationResponse, error) {
	view, err := s.svc.ShowConversation(ctx, req.GetSlug(), req.GetUserId(), pageFrom(req.GetPage()))
	if err != nil {
		return nil, toStatus(ctx, "GetConversation", err)
	}

	messages := make([]*chatv1.ChatMessage, 0, len(view.Messages))
	for _, m := range view.Messages {
		messages = append(messages, messageToProto(m))
	}

	return &chatv1.GetConversationResponse{
		Conversation: conversationToProto(view.Conversation),
		Messages:     messages,
		Page:         pageToProto(view.Page, view.Total),
	}, nil
}

func (s *Server) UpdateConversationTitle(
	ctx context.Context, req *chatv1.UpdateConversationTitleRequest,
) (*chatv1.UpdateConversationTitleResponse, error) {
	conversation, err := s.svc.RenameConversation(ctx,
		req.GetSlug(), req.GetUserId(), req.GetTitle())
	if err != nil {
		return nil, toStatus(ctx, "UpdateConversationTitle", err)
	}
	return &chatv1.UpdateConversationTitleResponse{
		Conversation: conversationToProto(conversation),
	}, nil
}

func (s *Server) DeleteConversation(
	ctx context.Context, req *chatv1.DeleteConversationRequest,
) (*chatv1.DeleteConversationResponse, error) {
	if err := s.svc.DeleteConversation(ctx, req.GetSlug(), req.GetUserId()); err != nil {
		return nil, toStatus(ctx, "DeleteConversation", err)
	}
	return &chatv1.DeleteConversationResponse{}, nil
}

func (s *Server) SendMessage(
	ctx context.Context, req *chatv1.SendMessageRequest,
) (*chatv1.SendMessageResponse, error) {
	message, err := s.svc.SendMessage(ctx, app.SendMessageCommand{
		Slug:           req.GetConversationSlug(),
		UserID:         req.GetUserId(),
		Text:           req.GetMessage(),
		IdempotencyKey: req.GetIdempotencyKey().GetValue(),
	})
	if err != nil {
		return nil, toStatus(ctx, "SendMessage", err)
	}
	return &chatv1.SendMessageResponse{
		Message: messageToProto(message),
		JobId:   message.ID.String(),
	}, nil
}

func conversationToProto(c *domain.Conversation) *chatv1.Conversation {
	if c == nil {
		return nil
	}
	return &chatv1.Conversation{
		Id:     c.ID.String(),
		Slug:   c.Slug,
		UserId: c.UserID.String(),
		Title:  c.Title,
		Timestamps: &commonv1.Timestamps{
			CreatedAt: timestamppb.New(c.CreatedAt),
			UpdatedAt: timestamppb.New(c.UpdatedAt),
		},
	}
}

// messageToProto maps a message to its contract shape.
//
// The contract names the field content_json, while chat stores plain text. It
// is wrapped as {"text": ...} so the wire shape matches coaching threads - a
// client displaying both need not have two readers.
func messageToProto(m *domain.Message) *chatv1.ChatMessage {
	if m == nil {
		return nil
	}

	out := &chatv1.ChatMessage{
		Id:   m.ID.String(),
		Role: roleToProto(m.Role),
		Timestamps: &commonv1.Timestamps{
			CreatedAt: timestamppb.New(m.CreatedAt),
			UpdatedAt: timestamppb.New(m.UpdatedAt),
		},
	}
	if encoded, err := json.Marshal(map[string]string{"text": m.Content}); err == nil {
		out.ContentJson = string(encoded)
	}
	return out
}

func roleToProto(r domain.Role) chatv1.MessageRole {
	switch r {
	case domain.RoleUser:
		return chatv1.MessageRole_MESSAGE_ROLE_USER
	case domain.RoleModel:
		return chatv1.MessageRole_MESSAGE_ROLE_MODEL
	default:
		return chatv1.MessageRole_MESSAGE_ROLE_UNSPECIFIED
	}
}

// pageFrom reads the page request from the contract.
//
// The shared contract uses page_token, not a page number. The token is OPAQUE
// to the client - that is its purpose - and what is inside is this adapter's
// business. What is stored here is the page number, and that is a choice that
// can be replaced by a real cursor later WITHOUT changing the contract or the
// client.
//
// An unreadable token is treated as the first page, not as an error: a token
// expired or cut short by its user is far more common than a forged one, and
// returning an error for it only makes the list stop working.
func pageFrom(p *commonv1.PageRequest) domain.Page {
	return domain.Page{
		Number: pageNumberFromToken(p.GetPageToken()),
		Size:   int(p.GetPageSize()),
	}
}

// pageTokenPrefix makes this token recognisable when investigating.
//
// Without a marker, a token from another source that happens to read as a
// number would be accepted silently.
const pageTokenPrefix = "p:"

func pageNumberFromToken(token string) int {
	if !strings.HasPrefix(token, pageTokenPrefix) {
		return 1
	}
	number, err := strconv.Atoi(strings.TrimPrefix(token, pageTokenPrefix))
	if err != nil || number < 1 {
		return 1
	}
	return number
}

// pageToProto composes the page answer.
//
// next_page_token is EMPTY on the last page. The client uses its emptiness as
// the signal to stop; a token that is always present would make it request
// empty pages forever.
func pageToProto(p domain.Page, total int) *commonv1.PageResponse {
	out := &commonv1.PageResponse{}

	if p.Size > 0 && p.Number*p.Size < total {
		out.NextPageToken = pageTokenPrefix + strconv.Itoa(p.Number+1)
	}
	return out
}

// toStatus translates a domain error into a gRPC code.
func toStatus(ctx context.Context, op string, err error) error {
	switch {
	case err == nil:
		return nil

	// Someone else's and non-existent answer the SAME (S9).
	case errors.Is(err, domain.ErrConversationNotFound):
		return status.Error(codes.NotFound, "no such conversation")

	case errors.Is(err, domain.ErrEmptyMessage),
		errors.Is(err, domain.ErrMessageTooLong),
		errors.Is(err, domain.ErrTitleTooLong),
		errors.Is(err, domain.ErrBlankTitle),
		errors.Is(err, domain.ErrInvalidRole),
		errors.Is(err, domain.ErrInvalidID):
		return status.Error(codes.InvalidArgument, err.Error())

	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "the caller went away")
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, "the deadline passed")

	default:
		slog.ErrorContext(ctx, "unhandled error", "operation", op, "error", err)
		return status.Error(codes.Internal, "internal error")
	}
}
