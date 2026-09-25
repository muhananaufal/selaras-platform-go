package main

import (
	"math"
	"testing"
)

// TestTheReplicationFactorIsBoundedBeforeItIsNarrowed pins the flag check:
// Kafka takes the factor as an int16, and a plain conversion of a larger int
// wraps around silently (40000 would become -25536).
func TestTheReplicationFactorIsBoundedBeforeItIsNarrowed(t *testing.T) {
	for _, n := range []int{1, 3, math.MaxInt16} {
		got, err := replicationFactor(n)
		if err != nil || int(got) != n {
			t.Errorf("replicationFactor(%d) = %d, %v; want %d, nil", n, got, err, n)
		}
	}
	for _, n := range []int{0, -1, math.MaxInt16 + 1, 40000} {
		if got, err := replicationFactor(n); err == nil {
			t.Errorf("replicationFactor(%d) = %d, nil; want an error", n, got)
		}
	}
}
