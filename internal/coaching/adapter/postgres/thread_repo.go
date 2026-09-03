package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muhananaufal/selaras-platform-go/internal/coaching/domain"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

// ThreadRepository memenuhi domain.ThreadRepository.
type ThreadRepository struct {
	db pg.Querier
}

func NewThreadRepository(db pg.Querier) *ThreadRepository {
	return &ThreadRepository{db: db}
}

var _ domain.ThreadRepository = (*ThreadRepository)(nil)

func (r *ThreadRepository) CreateThread(ctx context.Context, t *domain.Thread) error {
	const q = `
		INSERT INTO coaching_threads (id, coaching_program_id, slug, title, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6)`

	if _, err := r.db.Exec(ctx, q,
		t.ID.String(), t.ProgramID.String(), t.Slug, t.Title, t.CreatedAt, t.UpdatedAt); err != nil {
		return fmt.Errorf("creating the thread: %w", err)
	}
	return nil
}

func (r *ThreadRepository) FindThreadBySlug(ctx context.Context, slug string) (*domain.Thread, error) {
	const q = `
		SELECT id, coaching_program_id, slug, title, created_at, updated_at
		FROM coaching_threads WHERE slug = $1`

	t, err := scanThread(r.db.QueryRow(ctx, q, domain.NormaliseSlug(slug)))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrThreadNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("querying the thread: %w", err)
	}
	return t, nil
}

func (r *ThreadRepository) ListThreads(ctx context.Context, programID domain.ID) ([]*domain.Thread, error) {
	const q = `
		SELECT id, coaching_program_id, slug, title, created_at, updated_at
		FROM coaching_threads
		WHERE coaching_program_id = $1
		ORDER BY created_at DESC`

	rows, err := r.db.Query(ctx, q, programID.String())
	if err != nil {
		return nil, fmt.Errorf("querying threads: %w", err)
	}
	defer rows.Close()

	// Slice kosong, bukan nil: nil menjadi `null` di JSON, dan klien yang
	// mengiterasi daftar akan gagal alih-alih menampilkan daftar kosong.
	out := make([]*domain.Thread, 0)
	for rows.Next() {
		t, err := scanThread(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating threads: %w", err)
	}
	return out, nil
}

func (r *ThreadRepository) UpdateThread(ctx context.Context, t *domain.Thread) error {
	const q = `UPDATE coaching_threads SET title = $2, updated_at = $3 WHERE id = $1`

	tag, err := r.db.Exec(ctx, q, t.ID.String(), t.Title, t.UpdatedAt)
	if err != nil {
		return fmt.Errorf("updating the thread: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrThreadNotFound
	}
	return nil
}

func (r *ThreadRepository) DeleteThread(ctx context.Context, id domain.ID) error {
	// Pesannya ikut terhapus lewat ON DELETE CASCADE.
	const q = `DELETE FROM coaching_threads WHERE id = $1`

	tag, err := r.db.Exec(ctx, q, id.String())
	if err != nil {
		return fmt.Errorf("deleting the thread: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrThreadNotFound
	}
	return nil
}

func (r *ThreadRepository) CreateMessage(ctx context.Context, m *domain.Message) error {
	content, err := encodeJSON(m.Content)
	if err != nil {
		return fmt.Errorf("encoding the message: %w", err)
	}
	if content == nil {
		// Kolomnya NOT NULL, dan pesan tanpa isi memang tidak boleh ada.
		// Menyerahkannya ke basis data akan menghasilkan galat constraint yang
		// tidak menyebutkan apa yang sebenarnya salah.
		return domain.ErrEmptyMessage
	}

	const q = `
		INSERT INTO coaching_messages (id, coaching_thread_id, role, content, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6)`

	if _, err := r.db.Exec(ctx, q,
		m.ID.String(), m.ThreadID.String(), string(m.Role), content,
		m.CreatedAt, m.UpdatedAt); err != nil {
		return fmt.Errorf("creating the message: %w", err)
	}
	return nil
}

// ListMessages membaca percakapan, terlama lebih dulu.
//
// limit membatasi jendela konteks (D8). Pembatasannya diterapkan pada yang
// TERBARU lalu urutannya dibalik: mengambil dua puluh pesan pertama akan
// memberi model awal percakapan dan melewatkan yang baru saja dikatakan.
func (r *ThreadRepository) ListMessages(
	ctx context.Context, threadID domain.ID, limit int,
) ([]*domain.Message, error) {
	q := `
		SELECT id, coaching_thread_id, role, content, created_at, updated_at
		FROM coaching_messages
		WHERE coaching_thread_id = $1
		ORDER BY created_at`

	args := []any{threadID.String()}
	if limit > 0 {
		q = `
			SELECT * FROM (
				SELECT id, coaching_thread_id, role, content, created_at, updated_at
				FROM coaching_messages
				WHERE coaching_thread_id = $1
				ORDER BY created_at DESC
				LIMIT $2
			) recent ORDER BY created_at`
		args = append(args, limit)
	}

	rows, err := r.db.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("querying messages: %w", err)
	}
	defer rows.Close()

	out := make([]*domain.Message, 0)
	for rows.Next() {
		var (
			id, tid, role        string
			content              []byte
			createdAt, updatedAt time.Time
		)
		if err := rows.Scan(&id, &tid, &role, &content, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scanning a message: %w", err)
		}

		parsedID, err := domain.ParseID(id)
		if err != nil {
			return nil, fmt.Errorf("stored message id is not a uuid: %w", err)
		}
		parsedThread, err := domain.ParseID(tid)
		if err != nil {
			return nil, fmt.Errorf("stored thread id is not a uuid: %w", err)
		}
		decoded, err := decodeJSON(content)
		if err != nil {
			return nil, fmt.Errorf("stored message content is not readable: %w", err)
		}

		out = append(out, &domain.Message{
			ID:        parsedID,
			ThreadID:  parsedThread,
			Role:      domain.Role(role),
			Content:   decoded,
			CreatedAt: createdAt,
			UpdatedAt: updatedAt,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating messages: %w", err)
	}
	return out, nil
}

func scanThread(row pgx.Row) (*domain.Thread, error) {
	var (
		id, programID, slug, title string
		createdAt, updatedAt       time.Time
	)
	if err := row.Scan(&id, &programID, &slug, &title, &createdAt, &updatedAt); err != nil {
		return nil, err
	}

	parsedID, err := domain.ParseID(id)
	if err != nil {
		return nil, fmt.Errorf("stored thread id is not a uuid: %w", err)
	}
	parsedProgram, err := domain.ParseID(programID)
	if err != nil {
		return nil, fmt.Errorf("stored program id is not a uuid: %w", err)
	}

	return &domain.Thread{
		ID:        parsedID,
		ProgramID: parsedProgram,
		Slug:      slug,
		Title:     title,
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
	}, nil
}
