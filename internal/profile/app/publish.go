package app

import (
	"context"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	eventsv1 "github.com/muhananaufal/selaras-platform-go/gen/events/v1"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/profile/domain"
)

// EventWriter writes events to the outbox.
type EventWriter interface {
	Write(ctx context.Context, aggregateType, aggregateID string, envelope *eventsv1.Envelope) error
}

// EventWriterFor creates an event writer ON a single transaction.
//
// A factory, not a ready-made writer: a writer built on the connection pool
// would commit on its own, and its event would survive even when the profile
// change was rolled back - announcing a change that never happened.
type EventWriterFor func(pg.Querier) EventWriter

// UnitOfWork runs a function inside one transaction.
type UnitOfWork interface {
	Do(ctx context.Context, fn func(pg.Querier) error) error
}

// ProfileRepositoryFor creates a repository on a single transaction.
type ProfileRepositoryFor func(pg.Querier) domain.ProfileRepository

// WithEvents installs event publishing on the service.
//
// Separate from NewService so profile-svc can still run without a broker, and
// so tests that only exercise the domain rules need not provide it. What must
// NOT happen is silently announcing only some: if any one of the three is
// missing, publishing is switched off entirely and that is stated in the log at
// start.
func (s *Service) WithEvents(uow UnitOfWork, repos ProfileRepositoryFor, events EventWriterFor) *Service {
	if uow == nil || repos == nil || events == nil {
		return s
	}
	s.uow = uow
	s.repos = repos
	s.events = events
	return s
}

// PublishesEvents says whether profile changes are announced.
func (s *Service) PublishesEvents() bool {
	return s.uow != nil && s.repos != nil && s.events != nil
}

// UpdateAndPublish applies changes and then announces them, in ONE transaction.
//
// It is separate from Update, and the separation is deliberate: Update is used
// by paths that need not announce anything - creating an empty profile at
// registration, say - and merging the two would make every caller decide
// something that is none of its business.
//
// Without publishing installed, it falls back to plain Update. That is not a
// silent mode: PublishesEvents states it, and main logs it at start.
func (s *Service) UpdateAndPublish(
	ctx context.Context, userID domain.UserID, changes domain.ProfileChanges,
) (*domain.Profile, error) {
	if !s.PublishesEvents() {
		return s.Update(ctx, userID, changes)
	}

	var updated *domain.Profile
	err := s.uow.Do(ctx, func(q pg.Querier) error {
		// A temporary service on top of this transaction. Using s.profiles here
		// would mean the profile change commits on its own, separate from its
		// event - and a process dying in between leaves a changed profile nobody
		// knows about.
		txService := &Service{profiles: s.repos(q), now: s.now}

		profile, err := txService.Update(ctx, userID, changes)
		if err != nil {
			return err
		}
		updated = profile

		return s.events(q).Write(ctx, "user_profile", profile.UserID().String(), envelopeFor(profile))
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// envelopeFor composes the event from the freshly stored profile.
func envelopeFor(p *domain.Profile) *eventsv1.Envelope {
	updated := &eventsv1.ProfileUpdated{
		UserProfileId: p.ID().String(),

		// user_id comes along, and that is mandatory: the consumer looks it up
		// through the identity verified on every request, not through the profile
		// id.
		UserId:   p.UserID().String(),
		Sex:      string(p.Sex()),
		Language: string(p.Language()),
	}

	// The date of birth and country may be empty (ADR-002 rule 2), and the
	// difference is real: an empty value that is sent is cached as "known to
	// be empty", while one not sent means "not filled in yet".
	if dob := p.DateOfBirth(); dob.IsStated() {
		iso := dob.String()
		updated.DateOfBirth = &iso
	}
	if country := p.CountryOfResidence(); country != "" {
		updated.CountryOfResidence = &country
	}

	return &eventsv1.Envelope{
		EventId:       uuid.NewString(),
		OccurredAt:    timestamppb.New(p.UpdatedAt()),
		SchemaVersion: 1,
		Payload:       &eventsv1.Envelope_ProfileUpdated{ProfileUpdated: updated},
	}
}
