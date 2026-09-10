// Package app composes the dashboard read-model into use cases.
package app

import (
	"context"
	"errors"
	"time"

	"github.com/muhananaufal/selaras-platform-go/internal/dashboard/domain"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

// ProjectionName is the name of this projection in projection_state.
//
// It is fixed. Changing it makes the recorded position belong to another
// projection, and the rebuild command would assume nothing has ever been
// built.
const ProjectionName = "dashboard"

// Repositories are the repositories that share one transaction.
type Repositories interface {
	Dashboards() domain.Repository
	State() domain.StateRepository
}

// UnitOfWork runs a function inside one transaction.
type UnitOfWork interface {
	Do(ctx context.Context, fn func(Repositories) error) error
}

// Service is the whole set of dashboard use cases.
type Service struct {
	dashboards domain.Repository
	state      domain.StateRepository
	uow        UnitOfWork
	now        func() time.Time
}

func NewService(
	dashboards domain.Repository,
	state domain.StateRepository,
	uow UnitOfWork,
	now func() time.Time,
) (*Service, error) {
	switch {
	case dashboards == nil:
		return nil, errors.New("nil dashboard repository")
	case state == nil:
		return nil, errors.New("nil projection state repository")
	case uow == nil:
		return nil, errors.New("nil unit of work")
	case now == nil:
		return nil, errors.New("nil clock")
	}
	return &Service{dashboards: dashboards, state: state, uow: uow, now: now}, nil
}

// View is the dashboard together with what is derived from it.
type View struct {
	Dashboard *domain.Dashboard

	// Lag is the delay between the last projected event and now. It is EXPOSED
	// through the API, not hidden: the read-model is eventually consistent,
	// and a hidden delay looks like a bug.
	Lag time.Duration
}

// Get reads a user's dashboard (F7-04).
//
// ONE query for the summary and one for the history - not four repositories
// calling each other as in the legacy system, and without a cache someone has
// to remember to clear.
//
// A user who has no projection yet gets an EMPTY dashboard, not an error. The
// page that welcomes a new user must not look broken, and the gateway
// translates the emptiness into a welcome message as the legacy system did.
func (s *Service) Get(ctx context.Context, userID string) (*View, error) {
	user, err := domain.ParseUserID(userID)
	if err != nil {
		return nil, err
	}

	dash, err := s.dashboards.Find(ctx, user)
	switch {
	case errors.Is(err, domain.ErrNoDashboard):
		dash = &domain.Dashboard{UserID: user, History: []*domain.Assessment{}}
	case err != nil:
		return nil, err
	}

	view := &View{Dashboard: dash}

	// Lag is read from the PROJECTION position, not from this user's row.
	//
	// A user's row that rarely changes would report hours of lag even though
	// the projection has just processed hundreds of other people's events - a
	// correct number about the row, but the wrong answer to "how far behind is
	// this dashboard".
	state, err := s.state.Get(ctx, ProjectionName)
	if err != nil {
		return nil, err
	}
	if !state.LastEventAt.IsZero() {
		view.Lag = s.now().Sub(state.LastEventAt)
	}
	return view, nil
}

// ProjectAssessment applies one assessment to the projection (F7-02).
//
// The projection and its position move in ONE transaction. If the two could
// be separated, the position could advance past an event not yet applied -
// and a rebuild would assume that event is in.
func (s *Service) ProjectAssessment(
	ctx context.Context, userID string, a *domain.Assessment, occurredAt time.Time,
) error {
	user, err := domain.ParseUserID(userID)
	if err != nil {
		return err
	}

	return s.uow.Do(ctx, func(r Repositories) error {
		if err := r.Dashboards().ApplyAssessment(ctx, user, a, occurredAt); err != nil {
			return err
		}
		return r.State().Advance(ctx, ProjectionName, occurredAt)
	})
}

// ProjectProgram copies the state of a coaching program into the
// projection.
func (s *Service) ProjectProgram(
	ctx context.Context, userID string, p *domain.Program, occurredAt time.Time,
) error {
	user, err := domain.ParseUserID(userID)
	if err != nil {
		return err
	}

	return s.uow.Do(ctx, func(r Repositories) error {
		if err := r.Dashboards().ApplyProgram(ctx, user, p, occurredAt); err != nil {
			return err
		}
		return r.State().Advance(ctx, ProjectionName, occurredAt)
	})
}

// Forget removes a user's projection.
func (s *Service) Forget(ctx context.Context, userID string) error {
	user, err := domain.ParseUserID(userID)
	if err != nil {
		return err
	}

	return s.uow.Do(ctx, func(r Repositories) error {
		return r.Dashboards().Forget(ctx, user)
	})
}

// State returns the projection position, for the rebuild command and
// measurement.
func (s *Service) State(ctx context.Context) (domain.ProjectionState, error) {
	return s.state.Get(ctx, ProjectionName)
}

// Querier is re-exported so the wiring in cmd need not import the platform
// package for a single type.
type Querier = pg.Querier
