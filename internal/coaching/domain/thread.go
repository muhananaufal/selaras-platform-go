package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
)

// Thread and message errors.
var (
	ErrThreadNotFound  = errors.New("coaching thread not found")
	ErrInvalidRole     = errors.New("invalid message role")
	ErrEmptyMessage    = errors.New("an empty message says nothing")
	ErrTitleTooLong    = errors.New("thread title is too long")
	ErrMessageTooLong  = errors.New("message is too long")
	ErrNoMessageAtAll  = errors.New("a thread needs a first message")
	ErrThreadNotInProg = errors.New("this thread belongs to another program")
)

// DefaultThreadTitle follows the default of the legacy system.
const DefaultThreadTitle = "Diskusi Program"

// derivedTitleRunes is the length of a title derived from the first message.
//
// 45, the same as `Str::limit($userMessage, 45)` in the legacy system
// [CoachingController.php:239] (D12). The number is kept not because it is
// ideal, but because a title that changes length would look like a data
// change to users who already have threads.
const derivedTitleRunes = 45

// truncationSuffix follows the default of Str::limit.
//
// It is kept as well: a title cut off without a marker reads like a title
// that simply ends there.
const truncationSuffix = "..."

// maxThreadTitle bounds a title sent by the user.
//
// One hundred, the same as `max:100` in the legacy system
// [CoachingController.php:230]. The column is TEXT and bounds nothing, so the
// bound has to be here - without it, one megabyte-long title would end up in
// every thread list ever read.
const maxThreadTitle = 100

// maxMessageBytes bounds a single message.
//
// It is also a cost bound: a long message becomes a long prompt, and a long
// prompt is paid for per token.
const maxMessageBytes = 16 * 1024

// Thread is one discussion thread within a program.
type Thread struct {
	ID        ID
	ProgramID ID
	Slug      string
	Title     string

	CreatedAt time.Time
	UpdatedAt time.Time
}

// NewThread creates a new thread.
//
// Its title is derived from the first message when none is given (D12). That
// is the legacy behaviour, and it is worth keeping: a thread list where every
// title reads "Diskusi Program" helps nobody find their conversation again.
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

// DeriveTitle builds a title from the first message (D12).
//
// It cuts by RUNE, not by byte. Cutting by byte would split a multi-byte
// character in the middle and produce a title ending in a broken byte - which
// renders as an empty box, and can make its JSON invalid.
//
// The legacy system cut by display WIDTH (Str::limit uses mb_strimwidth), not
// by rune. The difference only shows on wide characters - CJK and emoji count
// as two - so a title containing them will be slightly longer here. That
// deviation is deliberate: display width depends on the reader's font, and
// cutting by rune never splits a character.
func DeriveTitle(message string) string {
	trimmed := strings.TrimSpace(message)
	if trimmed == "" {
		return DefaultThreadTitle
	}

	// Newlines become spaces: a title is a single line, and a message with
	// paragraphs would produce a title that breaks the layout.
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

// Rename changes the title of a thread.
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

// BelongsToProgram says this thread is part of that program.
//
// It is checked separately from user ownership: a thread of another program
// that happens to belong to the same user must still not be reachable through
// this program's slug.
func (t *Thread) BelongsToProgram(programID ID) bool {
	return !t.ProgramID.IsZero() && t.ProgramID == programID
}

// Role is the role of a message's sender.
type Role string

const (
	RoleUser  Role = "user"
	RoleModel Role = "model"
)

// NewRole checks a value that comes from outside.
//
// Only two, and the database enforces that as well. A third role that slipped
// in would be sent to the LLM provider as a role it does not recognise.
func NewRole(raw string) (Role, error) {
	switch Role(raw) {
	case RoleUser, RoleModel:
		return Role(raw), nil
	default:
		return "", fmt.Errorf("%w: %q", ErrInvalidRole, raw)
	}
}

// Message is one message in a thread.
type Message struct {
	ID       ID
	ThreadID ID
	Role     Role

	// Content is a JSON shape, following the legacy system.
	//
	// It is not a plain string because the model's reply carries structure -
	// suggestions, references, and markers - that would be lost if flattened
	// into text.
	Content map[string]any

	CreatedAt time.Time
	UpdatedAt time.Time
}

// NewMessage creates a new message.
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

// NewUserMessage creates a text message from the user.
//
// It checks the length here, not in the handler: a bound that lives in the
// handler is lost as soon as a second path writes messages.
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

// Text returns the text content of a message, if any.
func (m *Message) Text() (string, bool) {
	text, ok := m.Content["text"].(string)
	return text, ok
}
