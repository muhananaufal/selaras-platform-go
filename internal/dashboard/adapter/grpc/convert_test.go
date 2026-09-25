package grpc

import (
	"math"
	"testing"
)

// TestTheAssessmentTotalIsCheckedBeforeItIsNarrowed pins the conversion of
// the total to the contract's int32: the history has no upper bound, so a
// total beyond int32 must become an error, not a wrapped negative number.
func TestTheAssessmentTotalIsCheckedBeforeItIsNarrowed(t *testing.T) {
	for _, n := range []int{0, 1, math.MaxInt32} {
		got, err := totalOf(n)
		if err != nil || int(got) != n {
			t.Errorf("totalOf(%d) = %d, %v; want %d, nil", n, got, err, n)
		}
	}
	for _, n := range []int{-1, math.MaxInt32 + 1} {
		if got, err := totalOf(n); err == nil {
			t.Errorf("totalOf(%d) = %d, nil; want an error", n, got)
		}
	}
}
