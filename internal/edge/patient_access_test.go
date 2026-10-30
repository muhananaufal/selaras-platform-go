package edge_test

import (
	"slices"
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"

	edgev1 "github.com/muhananaufal/selaras-platform-go/gen/edge/v1"
)

// readsOfAnotherUser are the public procedures that read data belonging to
// someone other than the caller - a patient, under consent (ADR-030). Each
// one has passed the checks ADR-030 lists: OpenFGA with higher consistency,
// fail closed, the read recorded before data is returned, and e2e leak tests.
// Adding one here is a decision, not a formality.
var readsOfAnotherUser = []string{
	"edge.v1.Assessment.ListPatientAssessments",
	"edge.v1.Coaching.ListPatientProgress",
}

// The owner's rule is that chat is never visible to anyone else, and the
// general rule is that a procedure acts for its caller. The only way to name
// another user is a field for it, so every procedure whose request has one
// must be on the list above - and none of them may belong to chat.
func TestOnlyReviewedProceduresNameAnotherUser(t *testing.T) {
	files := []protoreflect.FileDescriptor{
		edgev1.File_edge_v1_auth_proto, edgev1.File_edge_v1_profile_proto,
		edgev1.File_edge_v1_assessment_proto, edgev1.File_edge_v1_coaching_proto,
		edgev1.File_edge_v1_chat_proto, edgev1.File_edge_v1_nutrition_proto,
		edgev1.File_edge_v1_dashboard_proto, edgev1.File_edge_v1_clinic_proto,
	}
	// Fields that name another user whose data is being read. Clinic
	// management names members and clinicians, but reads nobody's data.
	patientFields := map[protoreflect.Name]bool{"patient_user_id": true, "user_id": true, "patient_id": true}

	var found []string
	for _, f := range files {
		for i := range f.Services().Len() {
			svc := f.Services().Get(i)
			for j := range svc.Methods().Len() {
				m := svc.Methods().Get(j)
				fields := m.Input().Fields()
				for k := range fields.Len() {
					if patientFields[fields.Get(k).Name()] {
						found = append(found, string(m.FullName()))
					}
				}
			}
		}
	}
	for _, name := range found {
		if !slices.Contains(readsOfAnotherUser, name) {
			t.Errorf("%s names another user; it has not been reviewed as a read under consent (ADR-030)", name)
		}
	}
	for _, name := range readsOfAnotherUser {
		if !slices.Contains(found, name) {
			t.Errorf("%s is listed but no longer names another user; remove it from the list", name)
		}
	}
	if chat := edgev1.File_edge_v1_chat_proto.Services().Get(0); slices.ContainsFunc(found, func(n string) bool {
		return protoreflect.FullName(n).Parent() == chat.FullName()
	}) {
		t.Fatal("a chat procedure names another user: chat is never visible to anyone else")
	}
}
