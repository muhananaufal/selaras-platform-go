// Package app holds the assessment use cases.
package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "github.com/muhananaufal/selaras-platform-go/gen/common/v1"
	eventsv1 "github.com/muhananaufal/selaras-platform-go/gen/events/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/assessment/domain"
	"github.com/muhananaufal/selaras-platform-go/internal/assessment/domain/score"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

// ProfileSnapshot is the demographic data the risk engine needs.
//
// It comes from profile-svc. It deliberately holds only what the computation
// actually uses: name and language never enter the model, and carrying them
// would only widen what leaks if this snapshot is ever recorded somewhere.
type ProfileSnapshot struct {
	// UserProfileID is derived from the profile that was read, not accepted
	// from the caller (ADR-023). This is what is stored as the assessment's
	// owner.
	UserProfileID string

	Age                int
	Sex                string
	CountryOfResidence string
}

// ProfileSource fetches a profile snapshot.
//
// Called once per ASSESSMENT, not once per request. An assessment is a rare
// action - a few times a year for one user - so this call sits on no hot
// path, and ADR-007 is not violated. The event-fed cache (F2-16) is an
// optimisation that follows, not a prerequisite.
type ProfileSource interface {
	Snapshot(ctx context.Context, userID string) (ProfileSnapshot, error)
}

var (
	// ErrProfileIncomplete marks a profile that is not yet sufficient to
	// compute from.
	//
	// It is NOT a system failure: an unfilled profile is a valid state (B7).
	// What is wrong is asking for an assessment before filling it in, and the
	// message has to name what is missing.
	ErrProfileIncomplete = errors.New("the profile is missing values the risk model needs")

	// ErrNotYours is used internally. It must NOT reach the client as itself -
	// see Get.
	ErrNotYours = errors.New("this assessment belongs to someone else")
)

// Service serves the assessment flow.
type Service struct {
	assessments domain.Repository
	profiles    ProfileSource
	engine      *score.Engine
	now         func() time.Time

	// statusWriter is installed later through WithStatusWriter. Nil means this
	// service only serves reads and computations - no reason to fail, but no
	// reason to pretend to accept work that will never be recorded either.
	statusWriter StatusWriterFor

	// repoFor is installed later through WithRepositoryFor, for the same
	// reason as statusWriter: nil means this service serves reads and
	// computations without an outbox, rather than pretending to announce
	// anything.
	repoFor RepositoryFor
}

func NewService(
	assessments domain.Repository,
	profiles ProfileSource,
	engine *score.Engine,
	now func() time.Time,
) (*Service, error) {
	switch {
	case assessments == nil:
		return nil, errors.New("nil assessment repository")
	case profiles == nil:
		return nil, errors.New("nil profile source")
	case engine == nil:
		return nil, errors.New("nil risk engine")
	case now == nil:
		return nil, errors.New("nil clock")
	}
	return &Service{assessments: assessments, profiles: profiles, engine: engine, now: now}, nil
}

// StartCommand is the input of one assessment.
//
// It carries user_id, not user_profile_id. The profile id is derived from the
// profile that is read, not accepted from the caller (ADR-023).
type StartCommand struct {
	UserID  string
	Answers map[string]any
}

// Start computes the risk, stores the result, and announces it.
//
// uow and events may be nil: assessment-svc still serves computations
// without an outbox, just as it still serves reads. What is NOT allowed is
// storing an assessment without its event when both EXIST - the dashboard
// read-model would never learn the assessment happened, and the user would
// see a stale dashboard with nobody able to explain why.
func (s *Service) Start(
	ctx context.Context, uow UnitOfWork, events EventWriterFor, cmd StartCommand,
) (*domain.Assessment, error) {
	profile, err := s.profiles.Snapshot(ctx, cmd.UserID)
	if err != nil {
		return nil, fmt.Errorf("reading the profile: %w", err)
	}
	if err := validate(profile); err != nil {
		return nil, err
	}

	// The profile id comes from the profile just read, not from the request.
	// That is what keeps an assessment from being written to someone else's
	// profile by anything that can reach this service.
	profileID, err := domain.ParseProfileID(profile.UserProfileID)
	if err != nil {
		return nil, err
	}

	result, err := s.engine.Calculate(score.Request{
		Sex:                profile.Sex,
		CountryOfResidence: profile.CountryOfResidence,
		Age:                profile.Age,
		Answers:            cmd.Answers,
	})
	if err != nil {
		return nil, fmt.Errorf("calculating risk: %w", err)
	}

	assessment, err := domain.New(profileID, result, cmd.Answers, s.now())
	if err != nil {
		return nil, err
	}

	// Without an outbox, the assessment is still computed and stored. It just
	// is not announced - and that is stated in the log at start-up, not
	// silently.
	if uow == nil || events == nil || s.repoFor == nil {
		return assessment, s.store(ctx, s.assessments, assessment)
	}

	// The assessment and its event are written in ONE transaction (E10).
	//
	// Publishing after the commit leaves room for the process to die between
	// the two, and the dashboard would never learn the assessment exists.
	// Publishing before the commit is worse still: the dashboard shows an
	// assessment that was rolled back.
	announced := assessmentCompleted(assessment, cmd.UserID, result.Category, s.now())

	if err := uow.Do(ctx, func(q pg.Querier) error {
		if err := s.store(ctx, s.repoFor(q), assessment); err != nil {
			return err
		}
		return events(q).Write(ctx, "assessment", assessment.ID.String(), announced)
	}); err != nil {
		return nil, err
	}
	return assessment, nil
}

// store saves the assessment, trying a fresh slug if the first one
// collides.
//
// An 80-bit slug will practically never collide, but "practically never" is
// not "cannot". One retry turns a vanishingly small probability into a
// failure the user never sees.
func (s *Service) store(
	ctx context.Context, repo domain.Repository, assessment *domain.Assessment,
) error {
	if err := repo.Create(ctx, assessment); err != nil {
		if !errors.Is(err, domain.ErrSlugTaken) {
			return fmt.Errorf("storing the assessment: %w", err)
		}
		slug, err := domain.NewSlug()
		if err != nil {
			return err
		}
		assessment.Slug = slug
		if err := repo.Create(ctx, assessment); err != nil {
			return fmt.Errorf("storing the assessment: %w", err)
		}
	}
	return nil
}

// assessmentCompleted composes the event that tells the outside world.
//
// It carries user_id and the risk category, and both have a reason. user_id: the
// dashboard read-model keeps one row per user and has to know whose row to
// update. The category: it is COMPUTED here, not requested from the language
// model as in the legacy system (B19), so it exists as soon as the assessment
// does - rather than waiting for a personalisation that can fail.
func assessmentCompleted(
	a *domain.Assessment, userID string, category score.Category, now time.Time,
) *eventsv1.Envelope {
	return &eventsv1.Envelope{
		EventId:       uuid.NewString(),
		OccurredAt:    timestamppb.New(now),
		SchemaVersion: 1,

		// The idempotency key derives from the assessment. One assessment
		// produces one of these events, forever - a consumer that receives it
		// twice because the relay is at-least-once can recognise it.
		IdempotencyKey: &commonv1.IdempotencyKey{Value: "assessment-completed:" + a.ID.String()},

		Payload: &eventsv1.Envelope_AssessmentCompleted{
			AssessmentCompleted: &eventsv1.AssessmentCompleted{
				AssessmentId:   a.ID.String(),
				Slug:           a.Slug,
				RiskPercentage: a.RiskPercentage,
				ModelUsed:      a.ModelUsed,
				UserId:         userID,
				RiskCategory:   string(category),
			},
		},
	}
}

// resolveProfileID asks for a user's profile id.
//
// Used by the READ path. It is one extra call on every read, and that price
// is paid consciously (ADR-023): without it, someone else's profile id sent
// along would read someone else's assessments.
func (s *Service) resolveProfileID(ctx context.Context, userID string) (domain.ProfileID, error) {
	profile, err := s.profiles.Snapshot(ctx, userID)
	if err != nil {
		return domain.ProfileID{}, fmt.Errorf("reading the profile: %w", err)
	}
	return domain.ParseProfileID(profile.UserProfileID)
}

// Get fetches an assessment by its slug, for the owner naming themselves.
//
// Someone else's assessment yields ErrAssessmentNotFound, NOT an
// authorisation error. Telling "does not exist" apart from "not yours" tells
// the asker that the slug exists - and with it how many assessments have
// ever been made, and which ones to guess next. This is what F2-14 asks for,
// and it closes the same pattern as finding S9.
func (s *Service) Get(ctx context.Context, slug, userID string) (*domain.Assessment, error) {
	profileID, err := s.resolveProfileID(ctx, userID)
	if err != nil {
		return nil, err
	}

	assessment, err := s.assessments.FindBySlug(ctx, domain.NormaliseSlug(slug))
	if err != nil {
		return nil, err
	}
	if !assessment.BelongsTo(profileID) {
		return nil, domain.ErrAssessmentNotFound
	}
	return assessment, nil
}

// History returns the most recent assessments of one profile.
func (s *Service) History(ctx context.Context, userID string, limit int) ([]*domain.Assessment, error) {
	profileID, err := s.resolveProfileID(ctx, userID)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		// The upper bound is fixed, not left to the caller. An unbounded request
		// is the cheapest way to make the database send someone's entire history
		// in one answer.
		limit = 20
	}
	return s.assessments.ListForProfile(ctx, profileID, limit)
}

// validate checks the profile snapshot before anything is computed.
//
// The risk engine will compute whatever it is given: an age of zero yields
// a number, an empty sex yields an error, and an empty country silently
// becomes the "high" region. The third is the most dangerous because it
// does not fail - it is just wrong.
func validate(p ProfileSnapshot) error {
	var missing []string

	if p.Age <= 0 {
		missing = append(missing, "date_of_birth")
	}
	if p.Sex != score.SexMale && p.Sex != score.SexFemale {
		missing = append(missing, "sex")
	}
	if p.CountryOfResidence == "" {
		missing = append(missing, "country_of_residence")
	}

	if len(missing) > 0 {
		return fmt.Errorf("%w: %v", ErrProfileIncomplete, missing)
	}
	return nil
}

// WithStatusWriter installs the personalisation status writer.
//
// Separate from NewService so a read-only service - and tests that only
// exercise the computation - need not provide one.
func (s *Service) WithStatusWriter(w StatusWriterFor) *Service {
	s.statusWriter = w
	return s
}

// WithRepositoryFor installs the transactional repository factory.
func (s *Service) WithRepositoryFor(f RepositoryFor) *Service {
	s.repoFor = f
	return s
}
