package app_test

import (
	"context"
	"errors"
	"testing"

	"github.com/muhananaufal/selaras-platform-go/internal/assessment/app"
)

const clinicianID = "018f4c1e-0000-7000-8000-00000000cccc"

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

func consented() *fakeAccess {
	return &fakeAccess{allowed: map[[3]string]bool{
		{"user:" + clinicianID, "can_view_assessments", "patient:" + mineID}: true,
	}}
}

// A clinician with the patient's consent reads the history - and the read is
// recorded, as the patient's, before the data is returned (ADR-030).
func TestAConsentedClinicianReadsAndTheReadIsRecorded(t *testing.T) {
	svc, _, _ := newService(t)
	seedAssessment(t, svc)
	access := consented()
	svc = svc.WithAccessChecker(access)
	writer, uow := &recordingWriter{}, &directUOW{}

	page, err := svc.PatientHistory(context.Background(), uow, writerFor(writer), clinicianID, mineID, 0, "")
	if err != nil {
		t.Fatalf("PatientHistory: %v", err)
	}
	if len(page.Assessments) != 1 {
		t.Fatalf("%d assessments; want the patient's 1", len(page.Assessments))
	}
	if len(writer.written) != 1 || writer.keys[0] != mineID {
		t.Fatalf("recorded %d events keyed %v; want one, keyed by the patient", len(writer.written), writer.keys)
	}
	rec := writer.written[0].GetClinicianAccessRecorded()
	if rec.GetClinicianUserId() != clinicianID || rec.GetPatientUserId() != mineID || rec.GetResource() != "risk_assessments" {
		t.Fatalf("the record is %v", rec)
	}
}

// Everything that is not a yes is a no, and nothing is read or recorded:
// no consent, a check that fails (fail closed), no checker at all, and a
// read whose record cannot be written.
func TestEveryDoubtRefusesTheRead(t *testing.T) {
	for name, tc := range map[string]struct {
		access *fakeAccess
		uowErr error
		want   error
	}{
		"no consent":                {&fakeAccess{}, nil, app.ErrNotPermitted},
		"OpenFGA unreachable":       {&fakeAccess{fail: errors.New("connection refused")}, nil, app.ErrAccessUnavailable},
		"no checker configured":     {nil, nil, app.ErrAccessUnavailable},
		"the record cannot be kept": {consented(), errors.New("outbox full"), app.ErrAccessUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			svc, repo, _ := newService(t)
			seedAssessment(t, svc)
			if tc.access != nil {
				svc = svc.WithAccessChecker(tc.access)
			}
			writer, uow := &recordingWriter{}, &directUOW{fail: tc.uowErr}
			reads := repo.lastLimit

			page, err := svc.PatientHistory(context.Background(), uow, writerFor(writer), clinicianID, mineID, 0, "")
			if !errors.Is(err, tc.want) {
				t.Fatalf("PatientHistory returned %v; want %v", err, tc.want)
			}
			if len(page.Assessments) != 0 || repo.lastLimit != reads {
				t.Fatal("the history was read although the answer was no")
			}
			if tc.uowErr == nil && len(writer.written) != 0 {
				t.Fatalf("a refused read was recorded as an access: %v", writer.written)
			}
		})
	}
}

// The check is made for the caller and the patient named, and nothing
// else: consent to one patient opens no other.
func TestTheCheckNamesTheCallerAndThePatient(t *testing.T) {
	svc, _, _ := newService(t)
	access := consented()
	svc = svc.WithAccessChecker(access)

	_, err := svc.PatientHistory(context.Background(), &directUOW{}, writerFor(&recordingWriter{}), clinicianID, theirsID, 0, "")
	if !errors.Is(err, app.ErrNotPermitted) {
		t.Fatalf("reading another patient returned %v; want ErrNotPermitted", err)
	}
	want := [3]string{"user:" + clinicianID, "can_view_assessments", "patient:" + theirsID}
	if len(access.asked) != 1 || access.asked[0] != want {
		t.Fatalf("asked %v; want exactly %v", access.asked, want)
	}
}

func TestMalformedInputIsRefusedBeforeAnythingIsChecked(t *testing.T) {
	svc, _, _ := newService(t)
	access := consented()
	svc = svc.WithAccessChecker(access)
	for name, call := range map[string]func() error{
		"a malformed patient id": func() error {
			_, err := svc.PatientHistory(context.Background(), &directUOW{}, writerFor(&recordingWriter{}), clinicianID, "x", 0, "")
			return err
		},
		"a negative page size": func() error {
			_, err := svc.PatientHistory(context.Background(), &directUOW{}, writerFor(&recordingWriter{}), clinicianID, mineID, -1, "")
			return err
		},
		"a forged page token": func() error {
			_, err := svc.PatientHistory(context.Background(), &directUOW{}, writerFor(&recordingWriter{}), clinicianID, mineID, 0, "%%%")
			return err
		},
	} {
		if err := call(); err == nil || errors.Is(err, app.ErrNotPermitted) {
			t.Errorf("%s returned %v; want an input error", name, err)
		}
	}
	if len(access.asked) != 0 {
		t.Fatalf("OpenFGA was asked %d time(s) for malformed input", len(access.asked))
	}
}
