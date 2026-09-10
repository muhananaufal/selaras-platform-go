package kafka

import (
	"errors"
	"sort"

	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
)

// RecoverRecreatedTopics resubscribes to topics that were recreated on the
// broker.
//
// A Kafka topic has an id alongside its name. When a topic is deleted and
// created again under the same name (the broker lost its data - B23 - and
// `topics` created it back), a client still holding the old id receives
// UNKNOWN_TOPIC_ID on EVERY fetch. franz-go deliberately does not recover
// from this: after five consecutive failures it lets the error surface
// forever, "stall loudly" [franz-go@v1.21.6/pkg/kgo/source.go:1335-1352].
// Without handling, the only way out is a process restart - and that is what
// happened twice during F9 (B26).
//
// The recovery follows the client's documentation: PurgeTopicsFromClient
// discards all knowledge of the topic (including its old id), then
// AddConsumeTopics subscribes again with fresh metadata
// [franz-go@v1.21.6/pkg/kgo/client.go:680-693, consumer.go:872-880]. On a
// consumer group this triggers a rebalance; that is a far cheaper price than
// a restart.
//
// What is NOT recovered: the offsets already committed for that topic name.
// The new topic starts from zero while the group remembers the old topic's
// offsets; records below the old offset on the new topic are skipped. A
// restart would not change that either, and this scenario is not a production
// one anyway - the goal is a consumer that goes back to reading NEW records
// without intervention.
//
// Returned: the names of the topics that were resubscribed, sorted; nil when
// no error related to a recreated topic was found.
func RecoverRecreatedTopics(client *kgo.Client, errs []kgo.FetchError) []string {
	seen := map[string]struct{}{}
	for _, e := range errs {
		if errors.Is(e.Err, kerr.UnknownTopicID) && e.Topic != "" {
			seen[e.Topic] = struct{}{}
		}
	}
	if len(seen) == 0 {
		return nil
	}

	topics := make([]string, 0, len(seen))
	for t := range seen {
		topics = append(topics, t)
	}
	sort.Strings(topics)

	client.PurgeTopicsFromClient(topics...)
	client.AddConsumeTopics(topics...)
	return topics
}

// ForgetRecreatedTopic is the PRODUCER side of the same recovery.
//
// A client that has produced to a topic keeps its id; after the topic is
// recreated, every produce fails with UNKNOWN_TOPIC_ID until that topic's
// metadata is discarded - and franz-go does not discard it on its own. The
// outbox relay calls this on that error so its next tick connects to the
// new topic; without it the relay repeats the same error every second,
// forever, as seen in B28.
//
// true when the error really was UNKNOWN_TOPIC_ID and the topic was
// forgotten.
func ForgetRecreatedTopic(client *kgo.Client, topic string, err error) bool {
	if topic == "" || !errors.Is(err, kerr.UnknownTopicID) {
		return false
	}
	client.PurgeTopicsFromClient(topic)
	return true
}
