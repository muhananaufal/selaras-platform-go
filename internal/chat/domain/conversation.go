// Package domain memuat aturan percakapan asisten umum.
//
// Ia tidak mengimpor apa pun dari adapter, dan itu dijaga test batas.
package domain

import (
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
)

// Galat yang dikenali pemanggil.
var (
	ErrConversationNotFound = errors.New("conversation not found")
	ErrInvalidID            = errors.New("invalid id")
	ErrInvalidRole          = errors.New("invalid message role")
	ErrEmptyMessage         = errors.New("an empty message says nothing")
	ErrMessageTooLong       = errors.New("message is too long")
	ErrTitleTooLong         = errors.New("conversation title is too long")
	ErrBlankTitle           = errors.New("a conversation title cannot be blank")
)

// DefaultTitle adalah judul percakapan yang belum diberi nama.
//
// Percakapan yang dibuat lewat tombol "mulai baru" belum punya pesan, jadi
// judulnya belum bisa diturunkan dari apa pun.
const DefaultTitle = "Percakapan Baru"

// derivedTitleRunes adalah panjang judul yang diturunkan dari pesan pertama.
//
// 45, sama dengan coaching dan dengan sistem lama (D12). Angkanya sengaja sama
// di kedua tempat: pengguna melihat dua daftar percakapan di aplikasi yang
// sama, dan judul yang dipotong berbeda panjang terlihat seperti kekeliruan.
const derivedTitleRunes = 45

// truncationSuffix mengikuti Str::limit di sistem lama.
const truncationSuffix = "..."

// maxTitle membatasi judul yang dikirim pengguna.
const maxTitle = 100

// maxMessageBytes membatasi satu pesan.
//
// Ia juga batas biaya: pesan yang panjang menjadi prompt yang panjang, dan
// prompt yang panjang dibayar per token.
const maxMessageBytes = 16 * 1024

// ContextWindow adalah jumlah pesan yang ikut ke prompt (D8).
//
// Dua puluh, sama dengan sistem lama.
const ContextWindow = 20

// ID adalah kunci internal. Slug yang muncul di API publik.
type ID struct{ v uuid.UUID }

func NewID() (ID, error) {
	v, err := uuid.NewV7()
	if err != nil {
		return ID{}, fmt.Errorf("generating a chat id: %w", err)
	}
	return ID{v: v}, nil
}

func ParseID(raw string) (ID, error) {
	v, err := uuid.Parse(raw)
	if err != nil {
		return ID{}, fmt.Errorf("%w: %q", ErrInvalidID, raw)
	}
	return ID{v: v}, nil
}

func (id ID) String() string { return id.v.String() }
func (id ID) IsZero() bool   { return id.v == uuid.Nil }

// UserID menunjuk ke identity.users.
type UserID struct{ v uuid.UUID }

func ParseUserID(raw string) (UserID, error) {
	v, err := uuid.Parse(raw)
	if err != nil {
		return UserID{}, fmt.Errorf("%w: user %q", ErrInvalidID, raw)
	}
	return UserID{v: v}, nil
}

func (id UserID) String() string { return id.v.String() }
func (id UserID) IsZero() bool   { return id.v == uuid.Nil }

// slugBytes adalah 10 byte, 80 bit.
const slugBytes = 10

var slugEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// NewSlug menghasilkan id publik baru.
func NewSlug() (string, error) {
	raw := make([]byte, slugBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generating slug: %w", err)
	}
	return slugEncoding.EncodeToString(raw), nil
}

// NormaliseSlug membersihkan slug yang datang dari URL.
func NormaliseSlug(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

// Conversation adalah satu percakapan.
type Conversation struct {
	ID     ID
	UserID UserID
	Slug   string
	Title  string

	CreatedAt time.Time
	UpdatedAt time.Time
}

// NewConversation membuat percakapan baru.
//
// firstMessage boleh kosong: percakapan bisa dibuat sebelum ada pesan. Judulnya
// diturunkan darinya bila ada, dan jatuh ke DefaultTitle bila tidak (D12).
func NewConversation(userID UserID, title, firstMessage string, now time.Time) (*Conversation, error) {
	if userID.IsZero() {
		return nil, fmt.Errorf("%w: a conversation needs an owner", ErrInvalidID)
	}

	title = strings.TrimSpace(title)
	if title == "" {
		title = DeriveTitle(firstMessage)
	}
	if len([]rune(title)) > maxTitle {
		return nil, fmt.Errorf("%w: %d runes, limit %d", ErrTitleTooLong, len([]rune(title)), maxTitle)
	}

	id, err := NewID()
	if err != nil {
		return nil, err
	}
	slug, err := NewSlug()
	if err != nil {
		return nil, err
	}

	return &Conversation{
		ID: id, UserID: userID, Slug: slug, Title: title,
		CreatedAt: now, UpdatedAt: now,
	}, nil
}

// DeriveTitle membuat judul dari pesan pertama (D12).
//
// Memotong per RUNE, bukan per byte: memotong per byte akan memutus karakter
// multi-byte dan menghasilkan judul yang berakhir dengan byte rusak.
func DeriveTitle(message string) string {
	trimmed := strings.TrimSpace(message)
	if trimmed == "" {
		return DefaultTitle
	}

	// Judul adalah satu baris. Pesan berparagraf yang masuk apa adanya akan
	// merusak tata letak daftar percakapan.
	trimmed = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, trimmed)
	trimmed = strings.Join(strings.Fields(trimmed), " ")

	runes := []rune(trimmed)
	if len(runes) <= derivedTitleRunes {
		return trimmed
	}
	return strings.TrimSpace(string(runes[:derivedTitleRunes])) + truncationSuffix
}

// BelongsTo menyatakan kepemilikan.
//
// Dipakai untuk menjawab 404, BUKAN 403: membedakan "tidak ada" dari "milik
// orang lain" memberi tahu penanya bahwa slug itu ada (S9).
func (c *Conversation) BelongsTo(userID UserID) bool {
	return !c.UserID.IsZero() && c.UserID == userID
}

// Rename mengubah judul percakapan.
func (c *Conversation) Rename(title string, now time.Time) error {
	title = strings.TrimSpace(title)
	if title == "" {
		return ErrBlankTitle
	}
	if len([]rune(title)) > maxTitle {
		return fmt.Errorf("%w: %d runes, limit %d", ErrTitleTooLong, len([]rune(title)), maxTitle)
	}
	c.Title = title
	c.UpdatedAt = now
	return nil
}

// Touch menandai percakapan baru saja dipakai.
//
// Daftar percakapan diurutkan menurut updated_at, jadi tanpa ini percakapan
// yang aktif akan tenggelam di bawah percakapan lama yang baru saja diganti
// judulnya.
func (c *Conversation) Touch(now time.Time) { c.UpdatedAt = now }

// Role adalah peran pengirim pesan.
type Role string

const (
	RoleUser  Role = "user"
	RoleModel Role = "model"
)

// NewRole memeriksa nilai yang datang dari luar.
func NewRole(raw string) (Role, error) {
	switch Role(raw) {
	case RoleUser, RoleModel:
		return Role(raw), nil
	default:
		return "", fmt.Errorf("%w: %q", ErrInvalidRole, raw)
	}
}

// Message adalah satu pesan dalam percakapan.
type Message struct {
	ID             ID
	ConversationID ID
	Role           Role
	Content        string

	CreatedAt time.Time
	UpdatedAt time.Time
}

// NewMessage membuat pesan baru.
func NewMessage(conversationID ID, role Role, content string, now time.Time) (*Message, error) {
	if conversationID.IsZero() {
		return nil, fmt.Errorf("%w: a message needs a conversation", ErrInvalidID)
	}

	content = strings.TrimSpace(content)
	if content == "" {
		return nil, ErrEmptyMessage
	}
	if len(content) > maxMessageBytes {
		return nil, fmt.Errorf("%w: %d bytes, limit %d",
			ErrMessageTooLong, len(content), maxMessageBytes)
	}
	if role != RoleUser && role != RoleModel {
		return nil, fmt.Errorf("%w: %q", ErrInvalidRole, role)
	}

	id, err := NewID()
	if err != nil {
		return nil, err
	}

	return &Message{
		ID: id, ConversationID: conversationID, Role: role, Content: content,
		CreatedAt: now, UpdatedAt: now,
	}, nil
}
