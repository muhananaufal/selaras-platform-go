package kafka_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/muhananaufal/selaras-platform-go/internal/platform/kafka"
)

// TestAConsumerSurvivesATopicBeingRecreated is B26.
//
// The flow is exactly the real incident: a consumer is reading a topic,
// that topic is deleted and created again under the same name (new id), and
// then a producer writes to the new topic. franz-go gives up with
// UNKNOWN_TOPIC_ID forever; RecoverRecreatedTopics has to make the consumer
// read that new record without a restart.
func TestAConsumerSurvivesATopicBeingRecreated(t *testing.T) {
	addr := brokers(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	admin, err := kafka.NewProducer(kafka.Config{Brokers: addr, ClientID: "test-recreate-admin"})
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()

	topic := kafka.Topic{Name: "test-recreate-" + uuid.NewString()[:8], Partitions: 1}
	if _, err := kafka.EnsureTopics(ctx, admin, []kafka.Topic{topic}, 1); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = kafka.DeleteTopics(context.Background(), admin, topic.Name)
	})

	consumer, err := kafka.NewConsumer(
		kafka.Config{Brokers: addr, ClientID: "test-recreate-consumer"},
		"test-"+uuid.NewString(), topic.Name)
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Close()

	// produce writes one record; a producer still holding the old topic id
	// fails once with UNKNOWN_TOPIC_ID, forgets the topic, and then succeeds -
	// exactly the path the outbox relay takes.
	produce := func(value string) (forgot bool) {
		t.Helper()
		for attempt := 0; attempt < 2; attempt++ {
			err := admin.ProduceSync(ctx, &kgo.Record{
				Topic: topic.Name, Key: []byte("k"), Value: []byte(value),
			}).FirstErr()
			if err == nil {
				return forgot
			}
			if !kafka.ForgetRecreatedTopic(admin, topic.Name, err) {
				t.Fatalf("producing %q: %v", value, err)
			}
			forgot = true
		}
		t.Fatalf("producing %q still fails after forgetting the recreated topic", value)
		return forgot
	}

	// poll reads until a record with the requested value arrives, invoking the
	// recovery on every fetch error. It returns how many times the topic was
	// resubscribed.
	poll := func(want string, timeout time.Duration) (recoveries int) {
		t.Helper()
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			pctx, pcancel := context.WithTimeout(ctx, 2*time.Second)
			fetches := consumer.PollFetches(pctx)
			pcancel()
			if errs := fetches.Errors(); len(errs) > 0 {
				if recovered := kafka.RecoverRecreatedTopics(consumer, errs); len(recovered) > 0 {
					recoveries++
				}
			}
			var found bool
			fetches.EachRecord(func(r *kgo.Record) {
				if string(r.Value) == want {
					found = true
				}
			})
			if found {
				return recoveries
			}
		}
		t.Fatalf("record %q never arrived within %v (recoveries=%d)", want, timeout, recoveries)
		return recoveries
	}

	// 1. The normal state: one record read and its offset committed.
	produce("before")
	poll("before", 30*time.Second)
	if err := consumer.CommitUncommittedOffsets(ctx); err != nil {
		t.Fatal(err)
	}

	// 2. The topic is recreated under the consumer's feet.
	if err := kafka.DeleteTopics(ctx, admin, topic.Name); err != nil {
		t.Fatal(err)
	}
	if _, err := kafka.EnsureTopics(ctx, admin, []kafka.Topic{topic}, 1); err != nil {
		t.Fatal(err)
	}

	// 3. A new record on the new topic. The old offset (1) may skip the new
	//    topic's first record; what is demanded is the record written AFTER
	//    the recovery.
	produce("after-recreate-0")
	produce("after-recreate-1")
	recoveries := poll("after-recreate-1", 60*time.Second)
	if recoveries == 0 {
		t.Fatal("the record arrived without any recovery; the scenario did not exercise B26")
	}
}
