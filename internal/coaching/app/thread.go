package app

import (
	"context"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "github.com/muhananaufal/selaras-platform-go/gen/common/v1"
	eventsv1 "github.com/muhananaufal/selaras-platform-go/gen/events/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/coaching/domain"
)

// ContextWindow is the number of messages that go into the prompt (D8).
//
// Twenty, the same as the legacy system. Every message included is paid for per
// token, and an unbounded conversation would make one reply cost many times the
// first.
const ContextWindow = 20

// StartThreadCommand is a request to open a new thread.
type StartThreadCommand struct {
	ProgramSlug string
	UserID      string

	// Title may be empty: the title is derived from FirstMessage (D12).
	Title string

	// FirstMessage is required. A thread without a first message has nothing
	// to display, and its derived title has no source.
	FirstMessage string

	IdempotencyKey string
}

// ThreadView is a thread together with its conversation.
type ThreadView struct {
	Thread   *domain.Thread
	Program  *domain.Program
	Messages []*domain.Message
}

// StartNewThread opens a new thread and sends its first message (F4-12).
func (s *Service) StartNewThread(
	ctx context.Context, cmd StartThreadCommand,
) (*ThreadView, error) {
	if cmd.FirstMessage == "" {
		return nil, domain.ErrNoMessageAtAll
	}

	now := s.now()
	view := &ThreadView{}

	err := s.uow.Do(ctx, func(r Repositories) error {
		program, err := s.ownedProgram(ctx, r.Programs(), cmd.ProgramSlug, cmd.UserID)
		if err != nil {
			return err
		}

		// D5: a non-active program freezes interaction. One check, in the domain,
		// used by every path - not fifteen copies.
		if err := program.EnsureInteractive(); err != nil {
			return err
		}

		thread, err := domain.NewThread(program.ID, cmd.Title, cmd.FirstMessage, now)
		if err != nil {
			return err
		}
		if err := r.Threads().CreateThread(ctx, thread); err != nil {
			return err
		}

		message, err := domain.NewUserMessage(thread.ID, cmd.FirstMessage, now)
		if err != nil {
			return err
		}
		if err := r.Threads().CreateMessage(ctx, message); err != nil {
			return err
		}

		view.Thread = thread
		view.Program = program
		view.Messages = []*domain.Message{message}

		// The reply request is written in the same transaction. A thread stored
		// without its request would wait for a reply forever.
		return r.Events().Write(ctx, "coaching_thread", thread.ID.String(),
			chatReplyRequest(thread, message, cmd.IdempotencyKey, now))
	})
	if err != nil {
		return nil, err
	}
	return view, nil
}

// SendMessageCommand is a request to send a message to an existing thread.
type SendMessageCommand struct {
	ThreadSlug     string
	UserID         string
	Text           string
	IdempotencyKey string
}

// SendMessage writes the user's message and requests its reply (F4-13).
//
// It answers IMMEDIATELY. The model's reply comes later through llm.results and
// enters the thread as a message with the "model" role. The legacy system
// waited for Gemini inside the HTTP request, and one slow provider held the
// request for that long.
func (s *Service) SendMessage(
	ctx context.Context, cmd SendMessageCommand,
) (*domain.Message, error) {
	now := s.now()

	var written *domain.Message
	err := s.uow.Do(ctx, func(r Repositories) error {
		thread, program, err := s.ownedThread(ctx, r, cmd.ThreadSlug, cmd.UserID)
		if err != nil {
			return err
		}
		if err := program.EnsureInteractive(); err != nil {
			return err
		}

		message, err := domain.NewUserMessage(thread.ID, cmd.Text, now)
		if err != nil {
			return err
		}
		if err := r.Threads().CreateMessage(ctx, message); err != nil {
			return err
		}
		written = message

		return r.Events().Write(ctx, "coaching_thread", thread.ID.String(),
			chatReplyRequest(thread, message, cmd.IdempotencyKey, now))
	})
	if err != nil {
		return nil, err
	}
	return written, nil
}

// ShowThread loads a thread together with its whole conversation (F4-12).
//
// The whole of it, not the context window: what is bounded is the path that
// builds the prompt, because that is where every message costs. A user
// opening their conversation is entitled to see all of it.
func (s *Service) ShowThread(ctx context.Context, slug, userID string) (*ThreadView, error) {
	var view *ThreadView

	err := s.uow.Do(ctx, func(r Repositories) error {
		thread, program, err := s.ownedThread(ctx, r, slug, userID)
		if err != nil {
			return err
		}
		messages, err := r.Threads().ListMessages(ctx, thread.ID, 0)
		if err != nil {
			return err
		}
		view = &ThreadView{Thread: thread, Program: program, Messages: messages}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return view, nil
}

// RenameThread changes the title of a thread (F4-12).
func (s *Service) RenameThread(
	ctx context.Context, slug, userID, title string,
) (*domain.Thread, error) {
	now := s.now()

	var renamed *domain.Thread
	err := s.uow.Do(ctx, func(r Repositories) error {
		thread, program, err := s.ownedThread(ctx, r, slug, userID)
		if err != nil {
			return err
		}
		if err := program.EnsureInteractive(); err != nil {
			return err
		}
		if err := thread.Rename(title, now); err != nil {
			return err
		}
		renamed = thread
		return r.Threads().UpdateThread(ctx, thread)
	})
	if err != nil {
		return nil, err
	}
	return renamed, nil
}

// DestroyThread deletes a thread together with its messages (F4-12).
func (s *Service) DestroyThread(ctx context.Context, slug, userID string) error {
	return s.uow.Do(ctx, func(r Repositories) error {
		thread, program, err := s.ownedThread(ctx, r, slug, userID)
		if err != nil {
			return err
		}
		if err := program.EnsureInteractive(); err != nil {
			return err
		}
		return r.Threads().DeleteThread(ctx, thread.ID)
	})
}

// StoreReply stores a model reply that comes from llm-worker.
//
// Idempotent through the caller's key: the outbox relay is at-least-once, and a
// reply stored twice would show up twice on the conversation screen.
func (s *Service) StoreReply(
	ctx context.Context, threadID string, content map[string]any,
) error {
	id, err := domain.ParseID(threadID)
	if err != nil {
		return err
	}
	if len(content) == 0 {
		return domain.ErrEmptyMessage
	}

	now := s.now()
	return s.uow.Do(ctx, func(r Repositories) error {
		message, err := domain.NewMessage(id, domain.RoleModel, content, now)
		if err != nil {
			return err
		}
		return r.Threads().CreateMessage(ctx, message)
	})
}

// ConversationContext reads the context window for building a prompt (D8).
func (s *Service) ConversationContext(
	ctx context.Context, threadID domain.ID,
) ([]*domain.Message, error) {
	return s.threads.ListMessages(ctx, threadID, ContextWindow)
}

// chatReplyRequest composes the reply request event.
func chatReplyRequest(
	thread *domain.Thread, message *domain.Message, key string, now time.Time,
) *eventsv1.Envelope {
	if key == "" {
		// Derived from the MESSAGE, not from the thread: one thread receives many
		// messages, and a per-thread key would make the second and later messages
		// be skipped as duplicates.
		key = "chat-reply:" + message.ID.String()
	}

	threadID := thread.ID.String()
	return &eventsv1.Envelope{
		EventId:        uuid.NewString(),
		OccurredAt:     timestamppb.New(now),
		SchemaVersion:  1,
		IdempotencyKey: &commonv1.IdempotencyKey{Value: key},
		Payload: &eventsv1.Envelope_ChatReplyRequested{
			ChatReplyRequested: &eventsv1.ChatReplyRequested{
				ConversationId:   threadID,
				MessageId:        message.ID.String(),
				JobId:            message.ID.String(),
				CoachingThreadId: &threadID,
			},
		},
	}
}
