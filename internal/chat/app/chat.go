// Package app merangkai aturan chat menjadi use case.
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

// EventWriter menulis event ke outbox.
type EventWriter interface {
	Write(ctx context.Context, aggregateType, aggregateID string, envelope *eventsv1.Envelope) error
}

// Repositories adalah repository yang berbagi satu transaksi.
type Repositories interface {
	Conversations() domain.ConversationRepository
	Events() EventWriter
}

// UnitOfWork menjalankan sebuah fungsi di dalam satu transaksi.
type UnitOfWork interface {
	Do(ctx context.Context, fn func(Repositories) error) error
}

// Service adalah seluruh use case chat.
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

// owned memuat percakapan dan memeriksa kepemilikannya.
//
// SATU tempat, dan selalu menjawab ErrConversationNotFound untuk milik orang
// lain: membedakannya dari "tidak ada" memberi tahu penanya bahwa slug itu ada
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

// ConversationList adalah satu halaman daftar percakapan.
type ConversationList struct {
	Items []*domain.Conversation
	Total int
	Page  domain.Page
}

// ListConversations mengembalikan percakapan milik pemanggil (F5-04).
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

// CreateConversationCommand adalah permintaan membuat percakapan.
type CreateConversationCommand struct {
	UserID string
	Title  string

	// FirstMessage boleh kosong: percakapan bisa dibuat sebelum ada pesan.
	// Bila ada, ia ditulis DAN balasannya diminta - satu perjalanan, bukan dua.
	FirstMessage   string
	IdempotencyKey string
}

// ConversationView adalah percakapan beserta pesannya.
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

		// Permintaan balasan ditulis di transaksi yang sama. Percakapan yang
		// tersimpan tanpa permintaannya akan menunggu balasan selamanya.
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

// SendMessageCommand adalah permintaan mengirim pesan.
type SendMessageCommand struct {
	Slug           string
	UserID         string
	Text           string
	IdempotencyKey string
}

// SendMessage menulis pesan pengguna dan meminta balasannya (F5-07).
//
// Ia menjawab SEGERA. Balasan model datang belakangan lewat llm.results.
// Sistem lama menunggu Gemini di dalam permintaan HTTP, dan satu penyedia yang
// lambat menahan permintaannya selama itu.
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

		// Percakapan naik ke atas daftar. Tanpa ini, percakapan yang aktif
		// tenggelam di bawah percakapan lama yang baru saja diganti judulnya.
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

// DeleteConversation menghapus percakapan beserta pesannya (F5-05).
func (s *Service) DeleteConversation(ctx context.Context, slug, userID string) error {
	return s.uow.Do(ctx, func(r Repositories) error {
		conversation, err := s.owned(ctx, r.Conversations(), slug, userID)
		if err != nil {
			return err
		}
		return r.Conversations().Delete(ctx, conversation.ID)
	})
}

// StoreReply menyimpan balasan model yang datang dari llm-worker.
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

// ConversationContext membaca jendela konteks untuk menyusun prompt (D8).
func (s *Service) ConversationContext(
	ctx context.Context, conversationID domain.ID,
) ([]*domain.Message, error) {
	return s.conversations.TailMessages(ctx, conversationID, domain.ContextWindow)
}

// replyRequest menyusun event permintaan balasan.
func replyRequest(
	c *domain.Conversation, m *domain.Message, key string, now time.Time,
) *eventsv1.Envelope {
	if key == "" {
		// Diturunkan dari PESANNYA, bukan dari percakapannya: satu percakapan
		// menerima banyak pesan, dan kunci per percakapan akan membuat pesan
		// kedua dan seterusnya dilewati sebagai duplikat.
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

				// coaching_thread_id sengaja TIDAK diisi: itu yang membedakan
				// percakapan umum dari thread coaching di sisi worker, dan
				// mengisinya di sini akan mengirim balasannya ke tempat yang
				// salah.
			},
		},
	}
}
