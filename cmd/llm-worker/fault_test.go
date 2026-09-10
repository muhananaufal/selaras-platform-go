package main

import (
	"testing"
	"time"

	"github.com/muhananaufal/selaras-platform-go/internal/llm"
)

// applyFault is read from LLM_FAKE_FAULT for chaos F9-14. Its shape is
// deliberately narrow - three words, one argument - so a typo refuses the
// start instead of silently running the worker without the fault it is
// thought to be testing.
func TestApplyFaultConfiguresTheFake(t *testing.T) {
	cases := []struct {
		spec      string
		delay     time.Duration
		failFirst int
		always    bool
	}{
		{spec: "", delay: 0, failFirst: 0, always: false},
		{spec: "slow=3s", delay: 3 * time.Second},
		{spec: "flaky=2", failFirst: 2},
		{spec: "error", always: true},
	}
	for _, tc := range cases {
		fake := llm.NewFake()
		if err := applyFault(fake, tc.spec); err != nil {
			t.Fatalf("%q: %v", tc.spec, err)
		}
		if fake.Delay != tc.delay {
			t.Errorf("%q: delay = %s, want %s", tc.spec, fake.Delay, tc.delay)
		}
		if fake.FailFirst != tc.failFirst {
			t.Errorf("%q: failFirst = %d, want %d", tc.spec, fake.FailFirst, tc.failFirst)
		}
		if (fake.Err != nil) != tc.always {
			t.Errorf("%q: always failing = %v, want %v", tc.spec, fake.Err != nil, tc.always)
		}
	}
}

func TestApplyFaultRejectsWhatItDoesNotUnderstand(t *testing.T) {
	for _, spec := range []string{"slow", "slow=abc", "flaky=-1", "flaky=x", "crash", "error=now"} {
		if err := applyFault(llm.NewFake(), spec); err == nil {
			t.Errorf("%q should have been rejected", spec)
		}
	}
}
