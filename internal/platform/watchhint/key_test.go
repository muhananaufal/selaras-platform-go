package watchhint

import (
	"testing"

	"github.com/twmb/franz-go/pkg/kgo"
)

func TestAKeySurvivesTheWire(t *testing.T) {
	k := Key{Type: "assessment", ID: "018f4c1e-0000-7000-8000-00000000aaaa"}
	got, ok := parseKey(k.String())
	if !ok || got != k {
		t.Fatalf("parseKey(%q) = %v, %v; want %v", k.String(), got, ok, k)
	}
}

func TestAMalformedHintIsNotAKey(t *testing.T) {
	for _, raw := range []string{"", "assessment", ":id", "assessment:", "a:b:c"} {
		if k, ok := parseKey(raw); ok {
			t.Errorf("parseKey(%q) accepted it as %v", raw, k)
		}
	}
}

// Wakes coalesce: a watcher that has not yet read its last wake does not
// need a second one - it is about to read the state anyway - and the hub
// must never block on a slow stream.
func TestWakesCoalesceWithoutBlocking(t *testing.T) {
	s := newSubscription(nil, Key{Type: "guide", ID: "g"})
	s.wake(ReasonHint)
	s.wake(ReasonResubscribe)
	s.wake(ReasonHint)

	if got := <-s.C(); got != ReasonHint {
		t.Errorf("first wake is %q; want the one that came first", got)
	}
	select {
	case extra := <-s.C():
		t.Errorf("a second wake (%q) was queued behind an unread one", extra)
	default:
	}
}

func TestOnlyWatchedAggregatesHaveAKey(t *testing.T) {
	record := func(aggregateType, id string) *kgo.Record {
		return &kgo.Record{
			Key:     []byte(id),
			Headers: []kgo.RecordHeader{{Key: "aggregate_type", Value: []byte(aggregateType)}},
		}
	}

	for _, typ := range []string{TypeAssessment, TypeConversation, TypeCoachingProgram, TypeCoachingThread, TypeMealGuide} {
		got, ok := KeyOf(record(typ, "018f4c1e-0000-7000-8000-00000000f001"))
		if want := (Key{Type: typ, ID: "018f4c1e-0000-7000-8000-00000000f001"}); !ok || got != want {
			t.Errorf("KeyOf(%s) = %v, %v; want %v", typ, got, ok, want)
		}
	}

	// A profile update passes through two of the result consumers, but no
	// stream waits on it.
	if k, ok := KeyOf(record("user_profile", "018f4c1e-0000-7000-8000-00000000f002")); ok {
		t.Errorf("a profile record produced the key %v", k)
	}
	if k, ok := KeyOf(record(TypeConversation, "")); ok {
		t.Errorf("a record without a key produced %v", k)
	}
	if k, ok := KeyOf(&kgo.Record{Key: []byte("x")}); ok {
		t.Errorf("a record without aggregate_type produced %v", k)
	}
}
