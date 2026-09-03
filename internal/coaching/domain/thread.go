package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
)

// Galat thread dan pesan.
var (
	ErrThreadNotFound  = errors.New("coaching thread not found")
	ErrInvalidRole     = errors.New("invalid message role")
	ErrEmptyMessage    = errors.New("an empty message says nothing")
	ErrTitleTooLong    = errors.New("thread title is too long")
	ErrMessageTooLong  = errors.New("message is too long")
	ErrNoMessageAtAll  = errors.New("a thread needs a first message")
	ErrThreadNotInProg = errors.New("this thread belongs to another program")
)

// DefaultThreadTitle mengikuti nilai bawaan sistem lama.
const DefaultThreadTitle = "Diskusi Program"

// derivedTitleRunes adalah panjang judul yang diturunkan dari pesan pertama.
//
// 45, sama dengan `Str::limit($userMessage, 45)` di sistem lama
// [CoachingController.php:239] (D12). Angkanya dipertahankan bukan karena ia
// ideal, melainkan karena judul yang berubah panjang akan terlihat sebagai
// perubahan data bagi pengguna yang sudah punya thread.
const derivedTitleRunes = 45

// truncationSuffix mengikuti nilai bawaan Str::limit.
//
// Ia ikut dipertahankan: judul yang terpotong tanpa penanda terbaca seperti
// judul yang memang berakhir di situ.
const truncationSuffix = "..."

// maxThreadTitle membatasi judul yang dikirim pengguna.
//
// Seratus, sama dengan `max:100` di sistem lama
// [CoachingController.php:230]. Kolomnya TEXT dan tidak membatasi apa pun,
// jadi batasnya harus di sini - tanpa itu, satu judul sepanjang megabyte akan
// masuk ke setiap daftar thread yang pernah dibaca.
const maxThreadTitle = 100

// maxMessageBytes membatasi satu pesan.
//
// Ia juga batas biaya: pesan yang panjang menjadi prompt yang panjang, dan
// prompt yang panjang dibayar per token.
const maxMessageBytes = 16 * 1024

// Thread adalah satu utas diskusi dalam program.
type Thread struct {
	ID        ID
	ProgramID ID
	Slug      string
	Title     string

	CreatedAt time.Time
	UpdatedAt time.Time
}

// NewThread membuat utas baru.
//
// Judulnya diturunkan dari pesan pertama bila tidak diberikan (D12). Itu
// perilaku sistem lama, dan ia layak dipertahankan: daftar thread yang seluruh
// judulnya "Diskusi Program" tidak menolong siapa pun menemukan percakapannya
// kembali.
func NewThread(programID ID, title, firstMessage string, now time.Time) (*Thread, error) {
	if programID.IsZero() {
		return nil, fmt.Errorf("%w: a thread needs a program", ErrInvalidID)
	}

	title = strings.TrimSpace(title)
	if title == "" {
		title = DeriveTitle(firstMessage)
	}
	if len([]rune(title)) > maxThreadTitle {
		return nil, fmt.Errorf("%w: %d runes, limit %d",
			ErrTitleTooLong, len([]rune(title)), maxThreadTitle)
	}

	id, err := NewID()
	if err != nil {
		return nil, err
	}
	slug, err := NewSlug()
	if err != nil {
		return nil, err
	}

	return &Thread{
		ID:        id,
		ProgramID: programID,
		Slug:      slug,
		Title:     title,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

// DeriveTitle membuat judul dari pesan pertama (D12).
//
// Ia memotong per RUNE, bukan per byte. Memotong per byte akan memutus karakter
// multi-byte di tengah dan menghasilkan judul yang berakhir dengan byte rusak -
// yang tampil sebagai kotak kosong, dan bisa membuat JSON-nya tidak sah.
//
// Sistem lama memotong per LEBAR tampilan (Str::limit memakai mb_strimwidth),
// bukan per rune. Bedanya hanya muncul pada karakter lebar - CJK dan emoji
// dihitung dua - sehingga judul yang memuatnya akan sedikit lebih panjang di
// sini. Penyimpangan itu disengaja: lebar tampilan bergantung pada font
// pembacanya, dan memotong per rune tidak pernah memutus karakter.
func DeriveTitle(message string) string {
	trimmed := strings.TrimSpace(message)
	if trimmed == "" {
		return DefaultThreadTitle
	}

	// Baris baru diganti spasi: judul adalah satu baris, dan pesan yang
	// berparagraf akan menghasilkan judul yang merusak tata letak.
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

// Rename mengubah judul thread.
func (t *Thread) Rename(title string, now time.Time) error {
	title = strings.TrimSpace(title)
	if title == "" {
		return errors.New("a thread title cannot be blank")
	}
	if len([]rune(title)) > maxThreadTitle {
		return fmt.Errorf("%w: %d runes, limit %d",
			ErrTitleTooLong, len([]rune(title)), maxThreadTitle)
	}
	t.Title = title
	t.UpdatedAt = now
	return nil
}

// BelongsToProgram menyatakan thread ini bagian dari program tersebut.
//
// Ia diperiksa terpisah dari kepemilikan pengguna: thread milik program lain
// yang kebetulan milik pengguna yang sama tetap tidak boleh diakses lewat slug
// program ini.
func (t *Thread) BelongsToProgram(programID ID) bool {
	return !t.ProgramID.IsZero() && t.ProgramID == programID
}

// Role adalah peran pengirim pesan.
type Role string

const (
	RoleUser  Role = "user"
	RoleModel Role = "model"
)

// NewRole memeriksa nilai yang datang dari luar.
//
// Hanya dua, dan itu ditegakkan basis data juga. Peran ketiga yang menyelinap
// masuk akan dikirim ke penyedia LLM sebagai peran yang tidak dikenalnya.
func NewRole(raw string) (Role, error) {
	switch Role(raw) {
	case RoleUser, RoleModel:
		return Role(raw), nil
	default:
		return "", fmt.Errorf("%w: %q", ErrInvalidRole, raw)
	}
}

// Message adalah satu pesan dalam thread.
type Message struct {
	ID       ID
	ThreadID ID
	Role     Role

	// Content adalah bentuk JSON, mengikuti sistem lama.
	//
	// Ia bukan string biasa karena balasan model membawa struktur - saran,
	// rujukan, dan penanda - yang akan hilang kalau dipadatkan menjadi teks.
	Content map[string]any

	CreatedAt time.Time
	UpdatedAt time.Time
}

// NewMessage membuat pesan baru.
func NewMessage(threadID ID, role Role, content map[string]any, now time.Time) (*Message, error) {
	if threadID.IsZero() {
		return nil, fmt.Errorf("%w: a message needs a thread", ErrInvalidID)
	}
	if len(content) == 0 {
		return nil, ErrEmptyMessage
	}

	id, err := NewID()
	if err != nil {
		return nil, err
	}

	return &Message{
		ID:        id,
		ThreadID:  threadID,
		Role:      role,
		Content:   content,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

// NewUserMessage membuat pesan teks dari pengguna.
//
// Ia memeriksa panjangnya di sini, bukan di handler: batas yang hidup di
// handler akan hilang begitu ada jalur kedua yang menulis pesan.
func NewUserMessage(threadID ID, text string, now time.Time) (*Message, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, ErrEmptyMessage
	}
	if len(text) > maxMessageBytes {
		return nil, fmt.Errorf("%w: %d bytes, limit %d",
			ErrMessageTooLong, len(text), maxMessageBytes)
	}
	return NewMessage(threadID, RoleUser, map[string]any{"text": text}, now)
}

// Text mengambil isi teks sebuah pesan, bila ada.
func (m *Message) Text() (string, bool) {
	text, ok := m.Content["text"].(string)
	return text, ok
}
