package app

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/muhananaufal/selaras-platform-go/internal/assessment/domain"
)

var (
	// ErrInvalidPageToken marks a page token this service did not issue, or
	// one that was changed on the way.
	ErrInvalidPageToken = errors.New("invalid page token")

	// ErrInvalidPageSize marks a negative page size. AIP-158 treats it as the
	// caller's mistake; zero means "the default" and too large means "the
	// maximum", so those two are not errors.
	ErrInvalidPageSize = errors.New("the page size must not be negative")
)

// pageToken is what a page token carries: the position the page ended on.
//
// The encoding is base64url of JSON, so the token is opaque to clients (AIP-158)
// while staying readable to whoever debugs it. It is not signed: a crafted
// token only moves the start inside the caller's own history, because the
// profile always comes from the authenticated user, never from the token.
type pageToken struct {
	CreatedAt string `json:"t"`
	ID        string `json:"i"`
}

func encodePageToken(c domain.HistoryCursor) (string, error) {
	raw, err := json.Marshal(pageToken{
		// RFC 3339 with nanoseconds loses nothing: Postgres keeps microseconds,
		// and the cursor has to match the stored value exactly or the next
		// page would repeat or skip the row it ended on.
		CreatedAt: c.CreatedAt.UTC().Format(time.RFC3339Nano),
		ID:        c.ID.String(),
	})
	if err != nil {
		return "", fmt.Errorf("encoding page token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func decodePageToken(token string) (*domain.HistoryCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil, fmt.Errorf("%w: not base64url", ErrInvalidPageToken)
	}

	// Unmarshal, not a Decoder: it checks the whole input is one JSON value,
	// so a token with anything appended is refused rather than half-read.
	var t pageToken
	if err := json.Unmarshal(raw, &t); err != nil {
		return nil, fmt.Errorf("%w: not a page position", ErrInvalidPageToken)
	}

	createdAt, err := time.Parse(time.RFC3339Nano, t.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("%w: unreadable position", ErrInvalidPageToken)
	}
	id, err := domain.ParseID(t.ID)
	if err != nil {
		return nil, fmt.Errorf("%w: unreadable position", ErrInvalidPageToken)
	}
	return &domain.HistoryCursor{CreatedAt: createdAt, ID: id}, nil
}
