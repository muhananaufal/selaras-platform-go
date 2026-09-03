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

// ContextWindow adalah jumlah pesan yang ikut ke prompt (D8).
//
// Dua puluh, sama dengan sistem lama. Setiap pesan yang ikut dibayar per token,
// dan percakapan yang panjang tanpa batas akan membuat satu balasan berbiaya
// berkali lipat balasan pertama.
const ContextWindow = 20

// StartThreadCommand adalah permintaan membuka utas baru.
type StartThreadCommand struct {
	ProgramSlug string
	UserID      string

	// Title boleh kosong: judulnya diturunkan dari FirstMessage (D12).
	Title string

	// FirstMessage wajib. Thread tanpa pesan pertama tidak punya apa pun untuk
	// ditampilkan, dan judul turunannya tidak punya sumber.
	FirstMessage string

	IdempotencyKey string
}

// ThreadView adalah thread beserta percakapannya.
type ThreadView struct {
	Thread   *domain.Thread
	Program  *domain.Program
	Messages []*domain.Message
}

// StartNewThread membuka utas baru dan mengirim pesan pertamanya (F4-12).
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

		// D5: program non-aktif membekukan interaksi. Satu pemeriksaan, di
		// domain, dipakai seluruh jalur - bukan lima belas salinan.
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

		// Permintaan balasan ditulis di transaksi yang sama. Thread yang
		// tersimpan tanpa permintaannya akan menunggu balasan selamanya.
		return r.Events().Write(ctx, "coaching_thread", thread.ID.String(),
			chatReplyRequest(thread, message, cmd.IdempotencyKey, now))
	})
	if err != nil {
		return nil, err
	}
	return view, nil
}

// SendMessageCommand adalah permintaan mengirim pesan ke utas yang ada.
type SendMessageCommand struct {
	ThreadSlug     string
	UserID         string
	Text           string
	IdempotencyKey string
}

// SendMessage menulis pesan pengguna dan meminta balasannya (F4-13).
//
// Ia menjawab SEGERA. Balasan model datang belakangan lewat llm.results dan
// masuk ke thread sebagai pesan berperan "model". Sistem lama menunggu Gemini
// di dalam permintaan HTTP, dan satu penyedia yang lambat menahan permintaannya
// selama itu.
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

// ShowThread memuat utas beserta seluruh percakapannya (F4-12).
//
// Seluruhnya, bukan jendela konteks: yang dibatasi adalah jalur yang menyusun
// prompt, karena di sanalah setiap pesan berbiaya. Pengguna yang membuka
// percakapannya berhak melihat semuanya.
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

// RenameThread mengubah judul utas (F4-12).
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

// DestroyThread menghapus utas beserta pesannya (F4-12).
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

// StoreReply menyimpan balasan model yang datang dari llm-worker.
//
// Idempoten lewat kunci pemanggil: relay outbox at-least-once, dan balasan yang
// tersimpan dua kali akan muncul dua kali di layar percakapan.
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

// ConversationContext membaca jendela konteks untuk menyusun prompt (D8).
func (s *Service) ConversationContext(
	ctx context.Context, threadID domain.ID,
) ([]*domain.Message, error) {
	return s.threads.ListMessages(ctx, threadID, ContextWindow)
}

// chatReplyRequest menyusun event permintaan balasan.
func chatReplyRequest(
	thread *domain.Thread, message *domain.Message, key string, now time.Time,
) *eventsv1.Envelope {
	if key == "" {
		// Diturunkan dari PESANNYA, bukan dari threadnya: satu thread menerima
		// banyak pesan, dan kunci per thread akan membuat pesan kedua dan
		// seterusnya dilewati sebagai duplikat.
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
