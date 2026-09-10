// Package app composes the chat rules into use cases.
package app

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "github.com/muhananaufal/selaras-platform-go/gen/common/v1"
	eventsv1 "github.com/muhananaufal/selaras-platform-go/gen/events/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/chat/domain"
)

// EventWriter writes events to the outbox.
type EventWriter interface {
	Write(ctx context.Context, aggregateType, aggregateID string, envelope *eventsv1.Envelope) error
}

// Repositories are the repositories that share one transaction.
type Repositories interface {
	Conversations() domain.ConversationRepository
	Events() EventWriter
}

// UnitOfWork runs a function inside one transaction.
type UnitOfWork interface {
	Do(ctx context.Context, fn func(Repositories) error) error
}

// Service is the whole set of chat use cases.
type Service struct {
	conversations domain.ConversationRepository
	uow           UnitOfWork
	now           func() time.Time
}

func NewService(
	conversations domain.ConversationRepository,
	uow UnitOfWork,
	now func() time.Time,
) (*Service, error) {
	switch {
	case conversations == nil:
		return nil, errors.New("nil conversation repository")
	case uow == nil:
		return nil, errors.New("nil unit of work")
	case now == nil:
		return nil, errors.New("nil clock")
	}
	return &Service{conversations: conversations, uow: uow, now: now}, nil
}

// owned loads a conversation and checks its ownership.
//
// ONE place, and it always answers ErrConversationNotFound for someone else's:
// telling it apart from "does not exist" tells the asker that the slug exists
// (S9).
func (s *Service) owned(
	ctx context.Context, repo domain.ConversationRepository, slug, userID string,
) (*domain.Conversation, error) {
	user, err := domain.ParseUserID(userID)
	if err != nil {
		return nil, err
	}

	conversation, err := repo.FindBySlug(ctx, slug)
	if err != nil {
		return nil, err
	}
	if !conversation.BelongsTo(user) {
		return nil, domain.ErrConversationNotFound
	}
	return conversation, nil
}

// ConversationList is one page of the conversation list.
type ConversationList struct {
	Items []*domain.Conversation
	Total int
	Page  domain.Page
}

// ListConversations returns the caller's conversations (F5-04).
func (s *Service) ListConversations(
	ctx context.Context, userID string, page domain.Page,
) (*ConversationList, error) {
	user, err := domain.ParseUserID(userID)
	if err != nil {
		return nil, err
	}

	page = page.Normalise()
	items, total, err := s.conversations.ListForUser(ctx, user, page)
	if err != nil {
		return nil, err
	}
	return &ConversationList{Items: items, Total: total, Page: page}, nil
}

// CreateConversationCommand is a request to create a conversation.
type CreateConversationCommand struct {
	UserID string
	Title  string

	// FirstMessage may be empty: a conversation can be created before there is
	// a message. When present, it is written AND its reply is requested - one
	// round trip, not two.
	FirstMessage   string
	IdempotencyKey string
}

// ConversationView is a conversation together with its messages.
type ConversationView struct {
	Conversation *domain.Conversation
	Messages     []*domain.Message
	Total        int
	Page         domain.Page
}

// CreateConversation membuat percakapan baru (F5-05).
func (s *Service) CreateConversation(
	ctx context.Context, cmd CreateConversationCommand,
) (*ConversationView, error) {
	user, err := domain.ParseUserID(cmd.UserID)
	if err != nil {
		return nil, err
	}

	now := s.now()
	view := &ConversationView{Page: domain.Page{}.Normalise()}

	err = s.uow.Do(ctx, func(r Repositories) error {
		conversation, err := domain.NewConversation(user, cmd.Title, cmd.FirstMessage, now)
		if err != nil {
			return err
		}
		if err := r.Conversations().Create(ctx, conversation); err != nil {
			return err
		}
		view.Conversation = conversation

		if cmd.FirstMessage == "" {
			return nil
		}

		message, err := domain.NewMessage(conversation.ID, domain.RoleUser, cmd.FirstMessage, now)
		if err != nil {
			return err
		}
		if err := r.Conversations().CreateMessage(ctx, message); err != nil {
			return err
		}
		view.Messages = []*domain.Message{message}
		view.Total = 1

		// The reply request is written in the same transaction. A conversation
		// stored without its request would wait for a reply forever.
		return r.Events().Write(ctx, "conversation", conversation.ID.String(),
			replyRequest(conversation, message, cmd.IdempotencyKey, now))
	})
	if err != nil {
		return nil, err
	}
	return view, nil
}

// ShowConversation memuat percakapan beserta riwayatnya (F5-06).
func (s *Service) ShowConversation(
	ctx context.Context, slug, userID string, page domain.Page,
) (*ConversationView, error) {
	conversation, err := s.owned(ctx, s.conversations, slug, userID)
	if err != nil {
		return nil, err
	}

	page = page.Normalise()
	messages, total, err := s.conversations.ListMessages(ctx, conversation.ID, page)
	if err != nil {
		return nil, err
	}

	return &ConversationView{
		Conversation: conversation, Messages: messages, Total: total, Page: page,
	}, nil
}

// SendMessageCommand is a request to send a message.
type SendMessageCommand struct {
	Slug           string
	UserID         string
	Text           string
	IdempotencyKey string
}

// SendMessage writes the user's message and requests its reply (F5-07).
//
// It answers IMMEDIATELY. The model's reply comes later through llm.results.
// The legacy system waited for Gemini inside the HTTP request, and one slow
// provider held the request for that long.
func (s *Service) SendMessage(
	ctx context.Context, cmd SendMessageCommand,
) (*domain.Message, error) {
	now := s.now()

	var written *domain.Message
	err := s.uow.Do(ctx, func(r Repositories) error {
		conversation, err := s.owned(ctx, r.Conversations(), cmd.Slug, cmd.UserID)
		if err != nil {
			return err
		}

		message, err := domain.NewMessage(conversation.ID, domain.RoleUser, cmd.Text, now)
		if err != nil {
			return err
		}
		if err := r.Conversations().CreateMessage(ctx, message); err != nil {
			return err
		}
		written = message

		// The conversation moves to the top of the list. Without this, an active
		// conversation sinks below an old one that was merely renamed.
		conversation.Touch(now)
		if err := r.Conversations().Update(ctx, conversation); err != nil {
			return err
		}

		return r.Events().Write(ctx, "conversation", conversation.ID.String(),
			replyRequest(conversation, message, cmd.IdempotencyKey, now))
	})
	if err != nil {
		return nil, err
	}
	return written, nil
}

// RenameConversation mengubah judul percakapan (F5-08).
func (s *Service) RenameConversation(
	ctx context.Context, slug, userID, title string,
) (*domain.Conversation, error) {
	now := s.now()

	var renamed *domain.Conversation
	err := s.uow.Do(ctx, func(r Repositories) error {
		conversation, err := s.owned(ctx, r.Conversations(), slug, userID)
		if err != nil {
			return err
		}
		if err := conversation.Rename(title, now); err != nil {
			return err
		}
		renamed = conversation
		return r.Conversations().Update(ctx, conversation)
	})
	if err != nil {
		return nil, err
	}
	return renamed, nil
}

// DeleteConversation deletes a conversation together with its messages
// (F5-05).
func (s *Service) DeleteConversation(ctx context.Context, slug, userID string) error {
	return s.uow.Do(ctx, func(r Repositories) error {
		conversation, err := s.owned(ctx, r.Conversations(), slug, userID)
		if err != nil {
			return err
		}
		return r.Conversations().Delete(ctx, conversation.ID)
	})
}

// StoreReply stores a model reply that comes from llm-worker.
func (s *Service) StoreReply(ctx context.Context, conversationID, text string) error {
	id, err := domain.ParseID(conversationID)
	if err != nil {
		return err
	}

	now := s.now()
	return s.uow.Do(ctx, func(r Repositories) error {
		message, err := domain.NewMessage(id, domain.RoleModel, text, now)
		if err != nil {
			return err
		}
		return r.Conversations().CreateMessage(ctx, message)
	})
}

// ConversationContext reads the context window for building a prompt (D8).
func (s *Service) ConversationContext(
	ctx context.Context, conversationID domain.ID,
) ([]*domain.Message, error) {
	return s.conversations.TailMessages(ctx, conversationID, domain.ContextWindow)
}

// replyRequest composes the reply request event.
func replyRequest(
	c *domain.Conversation, m *domain.Message, key string, now time.Time,
) *eventsv1.Envelope {
	if key == "" {
		// Derived from the MESSAGE, not from the conversation: one conversation
		// receives many messages, and a per-conversation key would make the
		// second and later messages be skipped as duplicates.
		key = "chat-reply:" + m.ID.String()
	}

	return &eventsv1.Envelope{
		EventId:        uuid.NewString(),
		OccurredAt:     timestamppb.New(now),
		SchemaVersion:  1,
		IdempotencyKey: &commonv1.IdempotencyKey{Value: key},
		Payload: &eventsv1.Envelope_ChatReplyRequested{
			ChatReplyRequested: &eventsv1.ChatReplyRequested{
				ConversationId: c.ID.String(),
				MessageId:      m.ID.String(),
				JobId:          m.ID.String(),

				// coaching_thread_id is deliberately NOT set: that is what tells a
				// general conversation from a coaching thread on the worker side, and
				// setting it here would send the reply to the wrong place.
			},
		},
	}
}
