package kafka

import (
	"github.com/twmb/franz-go/pkg/kgo"
)

// Rewinder remembers the record that failed to process and rewinds the consumer
// to it, so the message is genuinely redelivered.
//
// It exists because the "failed? don't commit" pattern is NOT enough, and that
// was proven by running it.
//
// What happens without it: one batch fails, its offset is not committed, the
// consumer keeps calling PollFetches and receives the NEXT batch - franz-go
// does not resend anything within the same session. The next batch succeeds,
// CommitUncommittedOffsets is called, and it commits EVERYTHING consumed so far
// - including the record that failed. That record is lost forever, and not a
// single error appears when it happens.
//
// That is exactly what happened to the account-deletion saga: the dashboard
// failed to write its confirmation, then five confirmations from other units
// passed on the same topic, that batch succeeded, and the offset jumped over
// the deletion request that had not been worked. The saga hung with nobody
// knowing why.
type Rewinder struct {
	// lowest keeps the LOWEST failed offset per topic and partition.
	//
	// Lowest, not last: one batch can hold several failures, and rewinding to
	// the last one would skip the ones before it.
	lowest map[string]map[int32]kgo.EpochOffset
}

func NewRewinder() *Rewinder {
	return &Rewinder{lowest: map[string]map[int32]kgo.EpochOffset{}}
}

// Failed records one record that failed to process.
func (r *Rewinder) Failed(rec *kgo.Record) {
	if rec == nil {
		return
	}

	partitions, ok := r.lowest[rec.Topic]
	if !ok {
		partitions = map[int32]kgo.EpochOffset{}
		r.lowest[rec.Topic] = partitions
	}

	// The offset of that record ITSELF, not one past it.
	//
	// EpochOffset.Offset is where the consumer starts reading; one past it is
	// what is used when committing. Using +1 here would skip precisely the
	// record meant to be retried.
	candidate := kgo.EpochOffset{Epoch: rec.LeaderEpoch, Offset: rec.Offset}

	if existing, seen := partitions[rec.Partition]; seen && existing.Less(candidate) {
		return
	}
	partitions[rec.Partition] = candidate
}

// Any reports whether any failure has been recorded.
func (r *Rewinder) Any() bool { return len(r.lowest) > 0 }

// Rewind moves the consumer back to the earliest failed record.
//
// It is called AFTER PollFetches returns and BEFORE the next poll, with no
// commit running concurrently - that is the condition franz-go states for
// using SetOffsets, and a single-goroutine loop satisfies it.
//
// Once called, the record is cleared: the next round records its own
// failures.
func (r *Rewinder) Rewind(client *kgo.Client) {
	if len(r.lowest) == 0 {
		return
	}
	client.SetOffsets(r.lowest)
	r.lowest = map[string]map[int32]kgo.EpochOffset{}
}
