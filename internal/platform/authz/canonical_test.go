package authz

import (
	"strings"
	"testing"
)

// The two shapes one model takes: as `fga model transform` writes it, and as
// OpenFGA v1.21.0 returns it after storing it - with every empty default
// filled in. Captured from a real server; they must compare equal.
const (
	asWritten = `{"schema_version":"1.1","type_definitions":[{"type":"user"},` +
		`{"metadata":{"relations":{"admin":{"directly_related_user_types":[{"type":"user"}]}}},` +
		`"relations":{"admin":{"union":{"child":[{"this":{}},{"computedUserset":{"relation":"owner"}}]}}},"type":"clinic"}]}`
	asStored = `{"schema_version":"1.1","type_definitions":[{"type":"user","relations":{},"metadata":null},` +
		`{"type":"clinic","relations":{"admin":{"union":{"child":[{"this":{}},{"computedUserset":{"object":"","relation":"owner"}}]}}},` +
		`"metadata":{"relations":{"admin":{"directly_related_user_types":[{"type":"user","condition":""}],"module":"","source_info":null}},` +
		`"module":"","source_info":null}}],"conditions":{}}`
)

func TestAStoredModelEqualsTheOneWritten(t *testing.T) {
	same, err := sameJSON([]byte(asWritten), []byte(asStored))
	if err != nil || !same {
		t.Fatalf("sameJSON = %v, %v; want the stored defaults to compare equal to the written model", same, err)
	}
}

// Empty defaults are ignored, but "this" is an empty object that MEANS
// something - a direct relation - and must not be dropped with them.
func TestADifferentModelIsDifferent(t *testing.T) {
	for name, other := range map[string]string{
		"a relation renamed": `{"schema_version":"1.1","type_definitions":[{"type":"user"},` +
			`{"relations":{"admin":{"union":{"child":[{"this":{}},{"computedUserset":{"relation":"clinician"}}]}}},"type":"clinic"}]}`,
		// asWritten with only {"this":{}} taken out.
		"a direct relation removed": strings.Replace(asWritten, `{"this":{}},`, "", 1),
		"another schema version":    `{"schema_version":"1.2","type_definitions":[{"type":"user"}]}`,
	} {
		same, err := sameJSON([]byte(asWritten), []byte(other))
		if err != nil || same {
			t.Errorf("%s: sameJSON = %v, %v; want different", name, same, err)
		}
	}
}
