package domain

import "context"

// Page is a page request for the guide history.
//
// Offset-based. Someone's menu history numbers tens to hundreds, not
// millions, and a cursor adds a shape the client has to be told about
// without removing a problem that does not exist yet.
type Page struct {
	Number int
	Size   int
}

// Normalise clamps the page to a sensible range.
//
// The legacy system returned the WHOLE history in one call, cached forever.
// A history that grows every day would make one hub response grow without
// bound, and the ones who pay for it are the most loyal users.
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

// PreferencesRepository stores culinary preferences.
type PreferencesRepository interface {
	// FindByUser returns ErrPreferencesNotFound if the user has never touched
	// their preferences.
	//
	// The absence of preferences is NOT a failure, and callers handle it by
	// creating an empty set. A separate error is used so "not there yet" is not
	// disguised as "empty" - the two have to be told apart when writing: one is
	// an INSERT, the other an UPDATE.
	FindByUser(ctx context.Context, userID UserID) (*Preferences, error)

	Create(ctx context.Context, p *Preferences) error
	Update(ctx context.Context, p *Preferences) error
}

// GuideRepository stores daily menu guides.
type GuideRepository interface {
	Create(ctx context.Context, g *Guide) error

	FindByID(ctx context.Context, id ID) (*Guide, error)

	// ListForUser returns the guide history, newest first, together with the
	// total count.
	//
	// The count comes along because the client needs to know whether there is
	// a next page; counting by loading everything would defeat the point of
	// paging.
	ListForUser(ctx context.Context, userID UserID, page Page) (items []*Guide, total int, err error)

	// ListChosen returns the guides the user actually CHOSE, newest first, for
	// the learning history.
	//
	// It filters on chosen, rather than simply taking the last created as the
	// legacy system did (B17). The difference is not style: taking the last
	// created means feeding the model's own suggestions back to the model as
	// "menus the user likes", when the user may never have eaten them. Until
	// something marks them, this list is indeed empty - and empty is the right
	// answer.
	ListChosen(ctx context.Context, userID UserID, limit int) ([]*Guide, error)

	Update(ctx context.Context, g *Guide) error
}
