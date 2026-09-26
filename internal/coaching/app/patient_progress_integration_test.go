package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	coachingpg "github.com/muhananaufal/selaras-platform-go/internal/coaching/adapter/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/coaching/app"
	"github.com/muhananaufal/selaras-platform-go/internal/coaching/domain"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/outbox"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

// fakeAccess answers Check from a set of allowed (user, relation, object)
// triples, or fails.
type fakeAccess struct {
	allowed map[[3]string]bool
	fail    error
	asked   [][3]string
}

func (f *fakeAccess) Check(_ context.Context, user, relation, object string) (bool, error) {
	f.asked = append(f.asked, [3]string{user, relation, object})
	if f.fail != nil {
		return false, f.fail
	}
	return f.allowed[[3]string{user, relation, object}], nil
}

func consentedTo(clinician, patient string) *fakeAccess {
	return &fakeAccess{allowed: map[[3]string]bool{
		{"user:" + clinician, "can_view_coaching_progress", "patient:" + patient}: true,
	}}
}

// countingPrograms counts the reads of a user's programs, so a test can
// prove nothing was read when the answer was no.
type countingPrograms struct {
	domain.ProgramRepository
	lists int
}

func (c *countingPrograms) ListForUser(
	ctx context.Context, userID domain.UserID, limit int, after *domain.ProgramCursor,
) ([]*domain.Program, error) {
	c.lists++
	return c.ProgramRepository.ListForUser(ctx, userID, limit, after)
}

// clinicianService is the harness's service with a counting program
// repository, and optionally no outbox at all.
func (h *harness) clinicianService(t *testing.T, withOutbox bool) (*app.Service, *countingPrograms) {
	t.Helper()
	var events app.EventWriterFor
	if withOutbox {
		events = func(q pg.Querier) app.EventWriter { return outbox.NewWriter(q) }
	}
	programs := &countingPrograms{ProgramRepository: coachingpg.NewProgramRepository(h.pool)}
	svc, err := app.NewService(programs,
		coachingpg.NewCurriculumRepository(h.pool),
		coachingpg.NewThreadRepository(h.pool),
		coachingpg.NewUnitOfWork(h.pool, events),
		func() time.Time { return h.now })
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc, programs
}

// A consented clinician reads the patient's progress - and the read is
// recorded, as the patient's, before anything is read (ADR-030).
func TestAConsentedClinicianReadsProgressAndTheReadIsRecorded(t *testing.T) {
	h := setup(t)
	patient, clinician := h.user(), h.user()
	program := h.start(t, patient).Program
	if err := h.svc.StoreCurriculum(h.ctx, program.ID.String(), sampleCurriculum()); err != nil {
		t.Fatalf("StoreCurriculum: %v", err)
	}
	view, err := h.svc.ShowProgram(h.ctx, program.Slug, patient)
	if err != nil {
		t.Fatalf("ShowProgram: %v", err)
	}
	if _, err := h.svc.ToggleTaskStatus(h.ctx, view.Weeks[1].Tasks[0].ID.String(), patient); err != nil {
		t.Fatalf("ToggleTaskStatus: %v", err)
	}
	h.start(t, h.user()) // someone else's program
	before := len(h.events(t))

	svc, _ := h.clinicianService(t, true)
	page, err := svc.WithAccessChecker(consentedTo(clinician, patient)).
		PatientProgress(h.ctx, clinician, patient, 0, "")
	if err != nil {
		t.Fatalf("PatientProgress: %v", err)
	}

	if len(page.Programs) != 1 || page.Programs[0].Program.ID != program.ID {
		t.Fatalf("%d programs; want only the patient's one", len(page.Programs))
	}
	got := page.Programs[0]
	want := []domain.WeekProgress{{WeekNumber: 1, Total: 2, Completed: 0}, {WeekNumber: 2, Total: 2, Completed: 1}}
	if len(got.Weeks) != 2 || got.Weeks[0] != want[0] || got.Weeks[1] != want[1] {
		t.Fatalf("weeks are %v; want %v", got.Weeks, want)
	}
	if got.TasksTotal != 4 || got.TasksCompleted != 1 {
		t.Fatalf("totals are %d/%d; want 1/4", got.TasksCompleted, got.TasksTotal)
	}

	events := h.events(t)[before:]
	if len(events) != 1 {
		t.Fatalf("%d events recorded; want the one access record", len(events))
	}
	rec := events[0].GetClinicianAccessRecorded()
	if rec.GetClinicianUserId() != clinician || rec.GetPatientUserId() != patient || rec.GetResource() != "coaching_progress" {
		t.Fatalf("the record is %v", rec)
	}
}

// Everything that is not a yes is a no, and nothing is read: no consent, a
// check that fails (fail closed), no checker at all, and a read whose record
// cannot be written because the service runs without an outbox.
func TestEveryDoubtRefusesTheProgressRead(t *testing.T) {
	h := setup(t)
	patient, clinician := h.user(), h.user()
	h.start(t, patient)

	for name, tc := range map[string]struct {
		access     *fakeAccess
		withOutbox bool
		want       error
	}{
		"no consent":                {&fakeAccess{}, true, app.ErrNotPermitted},
		"OpenFGA unreachable":       {&fakeAccess{fail: errors.New("connection refused")}, true, app.ErrAccessUnavailable},
		"no checker configured":     {nil, true, app.ErrAccessUnavailable},
		"the record cannot be kept": {consentedTo(clinician, patient), false, app.ErrAccessUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			svc, programs := h.clinicianService(t, tc.withOutbox)
			if tc.access != nil {
				svc = svc.WithAccessChecker(tc.access)
			}
			before := len(h.events(t))

			page, err := svc.PatientProgress(h.ctx, clinician, patient, 0, "")
			if !errors.Is(err, tc.want) {
				t.Fatalf("PatientProgress returned %v; want %v", err, tc.want)
			}
			if len(page.Programs) != 0 || programs.lists != 0 {
				t.Fatal("the programs were read although the answer was no")
			}
			if after := len(h.events(t)); after != before {
				t.Fatalf("a refused read left %d event(s)", after-before)
			}
		})
	}
}

// The check names the caller, the progress relation and the patient named,
// and nothing else: consent to one patient opens no other.
func TestTheProgressCheckNamesTheCallerAndThePatient(t *testing.T) {
	h := setup(t)
	patient, other, clinician := h.user(), h.user(), h.user()
	access := consentedTo(clinician, patient)
	svc, _ := h.clinicianService(t, true)

	_, err := svc.WithAccessChecker(access).PatientProgress(h.ctx, clinician, other, 0, "")
	if !errors.Is(err, app.ErrNotPermitted) {
		t.Fatalf("reading another patient returned %v; want ErrNotPermitted", err)
	}
	want := [3]string{"user:" + clinician, "can_view_coaching_progress", "patient:" + other}
	if len(access.asked) != 1 || access.asked[0] != want {
		t.Fatalf("asked %v; want exactly %v", access.asked, want)
	}
}

func TestMalformedProgressRequestsAreRefusedBeforeAnythingIsChecked(t *testing.T) {
	h := setup(t)
	patient, clinician := h.user(), h.user()
	access := consentedTo(clinician, patient)
	svc, _ := h.clinicianService(t, true)
	svc = svc.WithAccessChecker(access)

	for name, tc := range map[string]struct {
		clinician, patient string
		size               int
		token              string
		want               error
	}{
		"a malformed patient id":   {clinician, "x", 0, "", domain.ErrInvalidID},
		"a malformed clinician id": {"x", patient, 0, "", domain.ErrInvalidID},
		"a negative page size":     {clinician, patient, -1, "", app.ErrInvalidPageSize},
		"a forged page token":      {clinician, patient, 0, "%%%", app.ErrInvalidPageToken},
	} {
		if _, err := svc.PatientProgress(h.ctx, tc.clinician, tc.patient, tc.size, tc.token); !errors.Is(err, tc.want) {
			t.Errorf("%s returned %v; want %v", name, err, tc.want)
		}
	}
	if len(access.asked) != 0 {
		t.Fatalf("OpenFGA was asked %d time(s) for malformed input", len(access.asked))
	}
}

// Pages follow AIP-158: a token while more remains, none on the last page,
// and a program without a curriculum yet shows no weeks rather than failing.
func TestProgressPagesAcrossThePatientsPrograms(t *testing.T) {
	h := setup(t)
	patient, clinician := h.user(), h.user()
	for range 3 {
		// Starting a program ends the previous active one (D2). All three share
		// the harness clock, so the id tie-break orders them.
		h.start(t, patient)
	}
	svc, _ := h.clinicianService(t, true)
	svc = svc.WithAccessChecker(consentedTo(clinician, patient))

	first, err := svc.PatientProgress(h.ctx, clinician, patient, 2, "")
	if err != nil {
		t.Fatalf("PatientProgress: %v", err)
	}
	if len(first.Programs) != 2 || first.NextPageToken == "" {
		t.Fatalf("first page: %d programs, token %q; want 2 and a token", len(first.Programs), first.NextPageToken)
	}
	second, err := svc.PatientProgress(h.ctx, clinician, patient, 2, first.NextPageToken)
	if err != nil {
		t.Fatalf("PatientProgress: %v", err)
	}
	if len(second.Programs) != 1 || second.NextPageToken != "" {
		t.Fatalf("last page: %d programs, token %q; want 1 and no token", len(second.Programs), second.NextPageToken)
	}
	seen := map[domain.ID]bool{}
	for _, p := range append(first.Programs, second.Programs...) {
		if seen[p.Program.ID] {
			t.Fatalf("program %s came back twice", p.Program.ID)
		}
		seen[p.Program.ID] = true
		if len(p.Weeks) != 0 || p.TasksTotal != 0 {
			t.Fatalf("a program without a curriculum shows %v", p.Weeks)
		}
	}
}
