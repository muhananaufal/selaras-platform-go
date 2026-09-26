package domain_test

import (
	"slices"
	"testing"

	"github.com/muhananaufal/selaras-platform-go/internal/clinic/domain"
)

func write(user, relation, object string) domain.TupleChange {
	return domain.TupleChange{Op: domain.OpWrite, User: user, Relation: relation, Object: object}
}

func del(user, relation, object string) domain.TupleChange {
	return domain.TupleChange{Op: domain.OpDelete, User: user, Relation: relation, Object: object}
}

func TestMembershipIsOneTuplePerRole(t *testing.T) {
	got := domain.MembershipChange(domain.OpWrite, "c1", "u1", domain.RoleClinician)
	if want := write("user:u1", "clinician", "clinic:c1"); got != want {
		t.Fatalf("MembershipChange = %+v; want %+v", got, want)
	}
	got = domain.MembershipChange(domain.OpDelete, "c1", "u1", domain.RoleAdmin)
	if want := del("user:u1", "admin", "clinic:c1"); got != want {
		t.Fatalf("MembershipChange = %+v; want %+v", got, want)
	}
}

// A grant opens the read through both halves of the model's intersection:
// the consent itself and the clinic that cares for the patient.
func TestAGrantWritesTheConsentAndTheCareClinic(t *testing.T) {
	got := domain.GrantChanges("ani", "c1", "rina")
	want := []domain.TupleChange{
		write("user:rina", "consented_clinician", "patient:ani"),
		write("clinic:c1", "care_clinic", "patient:ani"),
	}
	if !slices.Equal(got, want) {
		t.Fatalf("GrantChanges = %+v; want %+v", got, want)
	}
}

// The model keys consent by clinician, and care by clinic. A revocation
// deletes a tuple only when no consent still in force needs it: revoking
// (c1, rina) must not close a read that (c2, rina) still grants.
func TestARevocationKeepsWhatAnotherConsentStillNeeds(t *testing.T) {
	for _, tc := range []struct {
		name      string
		remaining []domain.Consent
		want      []domain.TupleChange
	}{
		{"the last consent", nil, []domain.TupleChange{
			del("user:rina", "consented_clinician", "patient:ani"),
			del("clinic:c1", "care_clinic", "patient:ani"),
		}},
		{"the same clinician in another clinic", []domain.Consent{{ClinicID: "c2", ClinicianUserID: "rina"}},
			[]domain.TupleChange{del("clinic:c1", "care_clinic", "patient:ani")}},
		{"another clinician in the same clinic", []domain.Consent{{ClinicID: "c1", ClinicianUserID: "budi"}},
			[]domain.TupleChange{del("user:rina", "consented_clinician", "patient:ani")}},
		{"both still needed", []domain.Consent{
			{ClinicID: "c2", ClinicianUserID: "rina"}, {ClinicID: "c1", ClinicianUserID: "budi"},
		}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := domain.RevokeChanges("ani", "c1", "rina", tc.remaining)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("RevokeChanges = %+v; want %+v", got, tc.want)
			}
		})
	}
}
