package watchhint

import "testing"

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
