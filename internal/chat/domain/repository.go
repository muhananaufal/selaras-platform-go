package domain

import "context"

// Page is a page request.
//
// Offset-based, not cursor-based. A user's conversations number in the tens,
// not millions, and a cursor adds a shape the client has to be told about
// without removing a problem that does not exist yet.
type Page struct {
	Number int
	Size   int
}

// Normalise clamps the page to a sensible range.
//
// An unbounded size lets one request ask for the whole history, and that is
// not a choice for the caller to make.
func (p Page) Normalise() Page {
	if p.Number < 1 {
		p.Number = 1
	}
	if p.Size < 1 {
		p.Size = 20
	}
	if p.Size > 100 {
		p.Size = 100
	}
	return p
}

// Offset is the number of rows skipped.
func (p Page) Offset() int { return (p.Number - 1) * p.Size }

// ConversationRepository stores conversations and their messages.
type ConversationRepository interface {
	Create(ctx context.Context, c *Conversation) error

	// FindBySlug looks a conversation up by its public slug.
	FindBySlug(ctx context.Context, slug string) (*Conversation, error)

	// ListForUser returns a user's conversations, newest first, together with
	// the total count.
	//
	// The count comes along because the client needs to know how many pages
	// there are; counting by loading everything would defeat the point of
	// paging.
	ListForUser(ctx context.Context, userID UserID, page Page) (items []*Conversation, total int, err error)

	Update(ctx context.Context, c *Conversation) error

	// Delete removes a conversation together with its messages.
	//
	// Cascaded through ON DELETE CASCADE in the database, not by deleting one
	// by one in Go: the latter leaves remnants when the process dies halfway,
	// and nobody will ever find those remnants.
	Delete(ctx context.Context, id ID) error

	CreateMessage(ctx context.Context, m *Message) error

	// ListMessages reads a conversation, oldest first.
	//
	// Paged for display; the context window uses TailMessages.
	ListMessages(ctx context.Context, conversationID ID, page Page) (items []*Message, total int, err error)

	// TailMessages reads a number of the LAST messages, oldest first.
	//
	// It is separate from ListMessages because the question differs: one is
	// "which page", the other "what was just said". Taking the first page as
	// context would give the model the start of the conversation and skip what
	// is most relevant (D8).
	TailMessages(ctx context.Context, conversationID ID, limit int) ([]*Message, error)
}
