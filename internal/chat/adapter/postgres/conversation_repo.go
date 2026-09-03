// Package postgres menyimpan percakapan chat di Postgres.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/muhananaufal/selaras-platform-go/internal/chat/domain"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

// Repository memenuhi domain.ConversationRepository.
type Repository struct {
	db pg.Querier
}

func NewRepository(db pg.Querier) *Repository { return &Repository{db: db} }

var _ domain.ConversationRepository = (*Repository)(nil)

const conversationColumns = `id, user_id, slug, title, created_at, updated_at`

func (r *Repository) Create(ctx context.Context, c *domain.Conversation) error {
	const q = `
		INSERT INTO conversations (id, user_id, slug, title, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6)`

	if _, err := r.db.Exec(ctx, q,
		c.ID.String(), c.UserID.String(), c.Slug, c.Title, c.CreatedAt, c.UpdatedAt); err != nil {
		return fmt.Errorf("creating the conversation: %w", err)
	}
	return nil
}

func (r *Repository) FindBySlug(ctx context.Context, slug string) (*domain.Conversation, error) {
	const q = `SELECT ` + conversationColumns + ` FROM conversations WHERE slug = $1`

	c, err := scanConversation(r.db.QueryRow(ctx, q, domain.NormaliseSlug(slug)))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrConversationNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("querying the conversation: %w", err)
	}
	return c, nil
}

// ListForUser mengembalikan percakapan seorang pengguna, terbaru lebih dulu.
func (r *Repository) ListForUser(
	ctx context.Context, userID domain.UserID, page domain.Page,
) ([]*domain.Conversation, int, error) {
	page = page.Normalise()

	// Jumlah dihitung LEBIH DULU dan terpisah.
	//
	// Menggabungkannya dengan window function akan menghitung ulang untuk
	// setiap baris; dua kueri lebih murah dan jauh lebih mudah dibaca.
	var total int
	if err := r.db.QueryRow(ctx,
		`SELECT count(*) FROM conversations WHERE user_id = $1`, userID.String(),
	).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("counting conversations: %w", err)
	}

	const q = `
		SELECT ` + conversationColumns + `
		FROM conversations
		WHERE user_id = $1
		ORDER BY updated_at DESC, id DESC
		LIMIT $2 OFFSET $3`

	rows, err := r.db.Query(ctx, q, userID.String(), page.Size, page.Offset())
	if err != nil {
		return nil, 0, fmt.Errorf("querying conversations: %w", err)
	}
	defer rows.Close()

	// Slice kosong, bukan nil: nil menjadi `null` di JSON, dan klien yang
	// mengiterasi daftar akan gagal alih-alih menampilkan daftar kosong.
	out := make([]*domain.Conversation, 0, page.Size)
	for rows.Next() {
		c, err := scanConversation(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterating conversations: %w", err)
	}
	return out, total, nil
}

func (r *Repository) Update(ctx context.Context, c *domain.Conversation) error {
	const q = `UPDATE conversations SET title = $2, updated_at = $3 WHERE id = $1`

	tag, err := r.db.Exec(ctx, q, c.ID.String(), c.Title, c.UpdatedAt)
	if err != nil {
		return fmt.Errorf("updating the conversation: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrConversationNotFound
	}
	return nil
}

func (r *Repository) Delete(ctx context.Context, id domain.ID) error {
	// Pesannya ikut terhapus lewat ON DELETE CASCADE.
	const q = `DELETE FROM conversations WHERE id = $1`

	tag, err := r.db.Exec(ctx, q, id.String())
	if err != nil {
		return fmt.Errorf("deleting the conversation: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrConversationNotFound
	}
	return nil
}

func (r *Repository) CreateMessage(ctx context.Context, m *domain.Message) error {
	const q = `
		INSERT INTO chat_messages (id, conversation_id, role, content, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6)`

	if _, err := r.db.Exec(ctx, q,
		m.ID.String(), m.ConversationID.String(), string(m.Role), m.Content,
		m.CreatedAt, m.UpdatedAt); err != nil {
		return fmt.Errorf("creating the message: %w", err)
	}
	return nil
}

// ListMessages membaca percakapan berhalaman, terlama lebih dulu.
func (r *Repository) ListMessages(
	ctx context.Context, conversationID domain.ID, page domain.Page,
) ([]*domain.Message, int, error) {
	page = page.Normalise()

	var total int
	if err := r.db.QueryRow(ctx,
		`SELECT count(*) FROM chat_messages WHERE conversation_id = $1`,
		conversationID.String(),
	).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("counting messages: %w", err)
	}

	const q = `
		SELECT id, conversation_id, role, content, created_at, updated_at
		FROM chat_messages
		WHERE conversation_id = $1
		ORDER BY created_at, id
		LIMIT $2 OFFSET $3`

	rows, err := r.db.Query(ctx, q, conversationID.String(), page.Size, page.Offset())
	if err != nil {
		return nil, 0, fmt.Errorf("querying messages: %w", err)
	}
	defer rows.Close()

	out := make([]*domain.Message, 0, page.Size)
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterating messages: %w", err)
	}
	return out, total, nil
}

// TailMessages membaca sejumlah pesan TERAKHIR, terlama lebih dulu.
//
// Pembatasannya diterapkan pada yang terbaru lalu urutannya dibalik: mengambil
// yang pertama akan memberi model awal percakapan dan melewatkan yang baru saja
// dikatakan (D8).
func (r *Repository) TailMessages(
	ctx context.Context, conversationID domain.ID, limit int,
) ([]*domain.Message, error) {
	if limit < 1 {
		limit = domain.ContextWindow
	}

	const q = `
		SELECT * FROM (
			SELECT id, conversation_id, role, content, created_at, updated_at
			FROM chat_messages
			WHERE conversation_id = $1
			ORDER BY created_at DESC, id DESC
			LIMIT $2
		) recent ORDER BY created_at, id`

	rows, err := r.db.Query(ctx, q, conversationID.String(), limit)
	if err != nil {
		return nil, fmt.Errorf("querying the conversation tail: %w", err)
	}
	defer rows.Close()

	out := make([]*domain.Message, 0, limit)
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating the conversation tail: %w", err)
	}
	return out, nil
}

func scanConversation(row pgx.Row) (*domain.Conversation, error) {
	var (
		id, userID, slug, title string
		createdAt, updatedAt    time.Time
	)
	if err := row.Scan(&id, &userID, &slug, &title, &createdAt, &updatedAt); err != nil {
		return nil, err
	}

	parsedID, err := domain.ParseID(id)
	if err != nil {
		return nil, fmt.Errorf("stored conversation id is not a uuid: %w", err)
	}
	parsedUser, err := domain.ParseUserID(userID)
	if err != nil {
		return nil, fmt.Errorf("stored owner id is not a uuid: %w", err)
	}

	return &domain.Conversation{
		ID: parsedID, UserID: parsedUser, Slug: slug, Title: title,
		CreatedAt: createdAt, UpdatedAt: updatedAt,
	}, nil
}

func scanMessage(row pgx.Row) (*domain.Message, error) {
	var (
		id, conversationID, role, content string
		createdAt, updatedAt              time.Time
	)
	if err := row.Scan(&id, &conversationID, &role, &content,
		&createdAt, &updatedAt); err != nil {
		return nil, err
	}

	parsedID, err := domain.ParseID(id)
	if err != nil {
		return nil, fmt.Errorf("stored message id is not a uuid: %w", err)
	}
	parsedConversation, err := domain.ParseID(conversationID)
	if err != nil {
		return nil, fmt.Errorf("stored conversation id is not a uuid: %w", err)
	}

	return &domain.Message{
		ID: parsedID, ConversationID: parsedConversation,
		Role: domain.Role(role), Content: content,
		CreatedAt: createdAt, UpdatedAt: updatedAt,
	}, nil
}
