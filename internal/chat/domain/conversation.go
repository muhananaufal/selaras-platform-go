// Package domain holds the rules of general-assistant conversations.
//
// It imports nothing from the adapters, and a boundary test guards that.
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

// Errors that callers recognise.
var (
	ErrConversationNotFound = errors.New("conversation not found")
	ErrInvalidID            = errors.New("invalid id")
	ErrInvalidRole          = errors.New("invalid message role")
	ErrEmptyMessage         = errors.New("an empty message says nothing")
	ErrMessageTooLong       = errors.New("message is too long")
	ErrTitleTooLong         = errors.New("conversation title is too long")
	ErrBlankTitle           = errors.New("a conversation title cannot be blank")
)

// DefaultTitle is the title of a conversation not yet named.
//
// A conversation created through the "start new" button has no message yet,
// so its title cannot be derived from anything.
const DefaultTitle = "Percakapan Baru"

// derivedTitleRunes is the length of a title derived from the first message.
//
// 45, the same as coaching and as the legacy system (D12). The number is
// deliberately the same in both places: users see two conversation lists in
// the same app, and titles cut to different lengths look like a mistake.
const derivedTitleRunes = 45

// truncationSuffix follows Str::limit in the legacy system.
const truncationSuffix = "..."

// maxTitle bounds a title sent by the user.
const maxTitle = 100

// maxMessageBytes bounds a single message.
//
// It is also a cost bound: a long message becomes a long prompt, and a long
// prompt is paid for per token.
const maxMessageBytes = 16 * 1024

// ContextWindow is the number of messages that go into the prompt (D8).
//
// Twenty, the same as the legacy system.
const ContextWindow = 20

// ID is the internal key. The slug is what appears in the public API.
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

// UserID points at identity.users.
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

// slugBytes is 10 bytes, 80 bits.
const slugBytes = 10

var slugEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// NewSlug generates a new public id.
func NewSlug() (string, error) {
	raw := make([]byte, slugBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generating slug: %w", err)
	}
	return slugEncoding.EncodeToString(raw), nil
}

// NormaliseSlug cleans a slug that arrived from a URL.
func NormaliseSlug(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

// Conversation is one conversation.
type Conversation struct {
	ID     ID
	UserID UserID
	Slug   string
	Title  string

	CreatedAt time.Time
	UpdatedAt time.Time
}

// NewConversation creates a new conversation.
//
// firstMessage may be empty: a conversation can be created before there is a
// message. The title is derived from it when present, and falls back to
// DefaultTitle when not (D12).
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

// DeriveTitle builds a title from the first message (D12).
//
// Cut by RUNE, not by byte: cutting by byte would split a multi-byte
// character and produce a title ending in a broken byte.
func DeriveTitle(message string) string {
	trimmed := strings.TrimSpace(message)
	if trimmed == "" {
		return DefaultTitle
	}

	// A title is a single line. A message with paragraphs let through as-is
	// would break the layout of the conversation list.
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

// BelongsTo states ownership.
//
// Used to answer 404, NOT 403: telling "does not exist" from "someone
// else's" tells the asker that the slug exists (S9).
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

// Touch marks a conversation as just used.
//
// The conversation list is ordered by updated_at, so without this an active
// conversation would sink below an old one that was merely renamed.
func (c *Conversation) Touch(now time.Time) { c.UpdatedAt = now }

// Role is the role of a message's sender.
type Role string

const (
	RoleUser  Role = "user"
	RoleModel Role = "model"
)

// NewRole checks a value that comes from outside.
func NewRole(raw string) (Role, error) {
	switch Role(raw) {
	case RoleUser, RoleModel:
		return Role(raw), nil
	default:
		return "", fmt.Errorf("%w: %q", ErrInvalidRole, raw)
	}
}

// Message is one message in a conversation.
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
