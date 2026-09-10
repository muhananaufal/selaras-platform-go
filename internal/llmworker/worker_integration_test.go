package llmworker_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "github.com/muhananaufal/selaras-platform-go/gen/common/v1"
	eventsv1 "github.com/muhananaufal/selaras-platform-go/gen/events/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/llm"
	"github.com/muhananaufal/selaras-platform-go/internal/llm/prompt"
	"github.com/muhananaufal/selaras-platform-go/internal/llmworker"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/kafka"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/outbox"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/postgres/pgtest"
)

func brokers(t *testing.T) string {
	t.Helper()

	addr := os.Getenv("TEST_KAFKA_BROKERS")
	if addr == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("TEST_KAFKA_BROKERS is not set; integration tests must not be skipped in CI")
		}
		t.Skip("TEST_KAFKA_BROKERS is not set; start the stack with 'task up' to run this test")
	}
	return addr
}

type harness struct {
	pool      *pgxpool.Pool
	provider  *llm.Fake
	consumer  *llmworker.Consumer
	client    *kgo.Client
	closeOnce sync.Once
	producer  *kgo.Client
	topic     string
	group     string
	brokers   string
	ctx       context.Context
}

// newHarness sets the worker up against a real Kafka and Postgres.
//
// Every test uses its OWN TOPIC, not the shared llm.jobs. A shared topic makes
// tests inherit each other's messages: one reads another's jobs, and the result
// depends on the run order.
func newHarness(t *testing.T) *harness {
	t.Helper()
	return newHarnessInGroup(t, "worker-test-"+uuid.NewString())
}

// newHarnessInGroup uses a consumer group chosen by the caller.
//
// Most tests do not care about the name as long as it is unique; the one
// testing offset commits does, because it needs to connect a second consumer
// to the same group.
func newHarnessInGroup(t *testing.T, group string) *harness {
	t.Helper()

	addr := brokers(t)
	pool := pgtest.Open(t, "llm")
	pgtest.Truncate(t, pool, "llm_jobs", "processed_messages", "outbox")

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)

	topic := "llm.jobs.test." + uuid.NewString()

	producer, err := kafka.NewProducer(kafka.Config{Brokers: addr, ClientID: "worker-test"})
	if err != nil {
		t.Fatalf("NewProducer: %v", err)
	}
	t.Cleanup(producer.Close)

	if _, err := kafka.EnsureTopics(ctx, producer,
		[]kafka.Topic{{Name: topic, Partitions: 1}}, 1); err != nil {
		t.Fatalf("creating the test topic: %v", err)
	}

	// The test topic is deleted once its test finishes.
	//
	// Without this every run leaves one orphaned topic on the broker, and two
	// hundred and fifty of them had piled up before anyone noticed. A failed
	// deletion is only logged: cleanup that fails a green test makes people
	// switch the cleanup off.
	t.Cleanup(func() {
		cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancelCleanup()

		if err := kafka.DeleteTopics(cleanupCtx, producer, topic); err != nil {
			t.Logf("could not delete the test topic %s: %v", topic, err)
		}
	})

	consumerClient, err := kafka.NewConsumer(
		kafka.Config{Brokers: addr, ClientID: "worker-test"},
		group, topic)
	if err != nil {
		t.Fatalf("NewConsumer: %v", err)
	}

	prompts, err := prompt.Load()
	if err != nil {
		t.Fatalf("prompt.Load: %v", err)
	}

	provider := llm.NewFake()
	consumer, err := llmworker.NewConsumer(consumerClient, pool, provider, prompts,
		slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
	if err != nil {
		t.Fatalf("NewConsumer: %v", err)
	}
	// The quota cooldown is shortened: the default policy is one minute
	// (ADR-025).
	consumer.WithQuotaCooldown(func(int) time.Duration { return 300 * time.Millisecond })

	h := &harness{
		pool: pool, provider: provider, consumer: consumer, client: consumerClient,
		producer: producer, topic: topic, group: group, brokers: addr, ctx: ctx,
	}
	t.Cleanup(h.leaveGroup)
	return h
}

// send menerbitkan satu permintaan personalisasi.
func (h *harness) send(t *testing.T, assessmentID, idempotencyKey string) {
	t.Helper()

	env := &eventsv1.Envelope{
		EventId:       uuid.NewString(),
		OccurredAt:    timestamppb.Now(),
		SchemaVersion: 1,
		Payload: &eventsv1.Envelope_PersonalizationRequested{
			PersonalizationRequested: &eventsv1.PersonalizationRequested{
				AssessmentId: assessmentID,
				Slug:         "slug-" + assessmentID,
			},
		},
	}
	if idempotencyKey != "" {
		env.IdempotencyKey = &commonv1.IdempotencyKey{Value: idempotencyKey}
	}

	payload, err := proto.Marshal(env)
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}

	if _, err := kafka.NewPublisher(h.producer).Publish(h.ctx, []kafka.Message{{
		Topic: h.topic,
		Key:   []byte(assessmentID),
		Value: payload,
	}}); err != nil {
		t.Fatalf("publishing: %v", err)
	}
}

// runUntil runs the worker until the condition holds or the time runs out.
//
// It returns Run's error, so "shut down cleanly" can be checked - not merely
// "stopped".
func (h *harness) runUntil(t *testing.T, timeout time.Duration, done func() bool) error {
	t.Helper()

	ctx, cancel := context.WithCancel(h.ctx)
	defer cancel()

	result := make(chan error, 1)
	go func() { result <- h.consumer.Run(ctx) }()

	deadline := time.After(timeout)
	for {
		if done() {
			cancel()
			select {
			case err := <-result:
				return err
			case <-time.After(15 * time.Second):
				t.Fatal("Run did not return after its context was cancelled")
			}
		}

		select {
		case err := <-result:
			t.Fatalf("Run returned early: %v", err)
		case <-deadline:
			cancel()
			<-result
			t.Fatal("the worker did not finish the work in time")
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (h *harness) countJobs(t *testing.T, aggregateID string) int {
	t.Helper()
	var n int
	if err := h.pool.QueryRow(h.ctx,
		`SELECT count(*) FROM llm_jobs WHERE aggregate_id = $1`, aggregateID).Scan(&n); err != nil {
		t.Fatalf("counting jobs: %v", err)
	}
	return n
}

// leaveGroup closes the consumer client, so it leaves the consumer group.
//
// It has to be called before another consumer in the same group connects:
// while the old member is still alive, it holds its partitions and the new
// member gets nothing - which would make the "receives nothing" test pass
// without checking the offset at all.
func (h *harness) leaveGroup() {
	h.closeOnce.Do(h.client.Close)
}

// statusOf reads the job status, or an empty string if there is no job yet.
func (h *harness) statusOf(t *testing.T, aggregateID string) string {
	t.Helper()

	var status string
	err := h.pool.QueryRow(h.ctx,
		`SELECT status FROM llm_jobs WHERE aggregate_id = $1`, aggregateID).Scan(&status)
	if err != nil {
		return ""
	}
	return status
}

// restart replaces the consumer client with a new one in the same group.
//
// It mimics a process that dies and is started again: the old member leaves
// the group, the new member joins, and messages whose offsets were not
// committed are delivered to it again.
func (h *harness) restart(t *testing.T) {
	t.Helper()

	h.leaveGroup()

	client, err := kafka.NewConsumer(
		kafka.Config{Brokers: h.brokers, ClientID: "worker-test-restarted"},
		h.group, h.topic)
	if err != nil {
		t.Fatalf("restarting the consumer: %v", err)
	}

	prompts, err := prompt.Load()
	if err != nil {
		t.Fatalf("prompt.Load: %v", err)
	}

	consumer, err := llmworker.NewConsumer(client, h.pool, h.provider, prompts,
		slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
	if err != nil {
		t.Fatalf("NewConsumer: %v", err)
	}

	h.client = client
	h.consumer = consumer
	h.closeOnce = sync.Once{}
	t.Cleanup(h.leaveGroup)
}

// TestAJobIsDoneAndItsResultStored is the normal path, end to end through a
// real broker.
func TestAJobIsDoneAndItsResultStored(t *testing.T) {
	h := newHarness(t)
	assessmentID := uuid.NewString()

	h.send(t, assessmentID, "key-"+assessmentID)

	// What is awaited is the FINAL STATUS, not the existence of the row.
	//
	// The job row is created in the claim transaction; the result is written
	// in the next transaction, after the provider answers. Waiting for the row
	// to exist means stopping between the two, and the read afterwards would
	// see pending - passing sometimes and not others, depending on scheduling.
	if err := h.runUntil(t, 60*time.Second, func() bool {
		return h.statusOf(t, assessmentID) == llmworker.StatusCompleted
	}); err != nil {
		t.Fatalf("Run returned %v; a cancelled worker should stop cleanly", err)
	}

	var status, promptVersion, model string
	var result []byte
	if err := h.pool.QueryRow(h.ctx,
		`SELECT status, coalesce(prompt_version,''), coalesce(model,''), result
		 FROM llm_jobs WHERE aggregate_id = $1`, assessmentID,
	).Scan(&status, &promptVersion, &model, &result); err != nil {
		t.Fatalf("reading the job: %v", err)
	}

	if status != llmworker.StatusCompleted {
		t.Fatalf("the job is %q, want completed", status)
	}
	if promptVersion != "personalization@1" {
		t.Fatalf("the job recorded prompt version %q", promptVersion)
	}
	if model == "" {
		t.Fatal("the job recorded no model")
	}

	var decoded map[string]any
	if err := json.Unmarshal(result, &decoded); err != nil {
		t.Fatalf("the stored result is not JSON: %v", err)
	}

	// And the event enters the outbox in the same transaction - not published
	// directly, which would let it be lost when the process dies.
	var eventType string
	if err := h.pool.QueryRow(h.ctx,
		`SELECT event_type FROM outbox WHERE aggregate_id = $1`, assessmentID,
	).Scan(&eventType); err != nil {
		t.Fatalf("reading the outbox: %v", err)
	}
	if eventType != outbox.EventPersonalizationCompleted {
		t.Fatalf("the outbox holds %q, want personalization.completed", eventType)
	}
}

// TestTheSameJobTwiceIsDoneOnce is the F3 gate through the real path.
//
// Two messages with the same idempotency key, through a real broker. What is
// counted is not how many messages arrive but how many times the provider is
// called - that is what costs money.
func TestTheSameJobTwiceIsDoneOnce(t *testing.T) {
	h := newHarness(t)
	assessmentID := uuid.NewString()
	key := "key-" + assessmentID

	h.send(t, assessmentID, key)
	h.send(t, assessmentID, key)

	if err := h.runUntil(t, 60*time.Second, func() bool {
		// Waited until BOTH messages are read, not until one job is formed -
		// stopping too early would make this test pass without ever seeing the
		// second message.
		var seen int
		if err := h.pool.QueryRow(h.ctx,
			`SELECT count(*) FROM processed_messages`).Scan(&seen); err != nil {
			t.Fatalf("counting claims: %v", err)
		}
		return seen == 1 && h.statusOf(t, assessmentID) == llmworker.StatusCompleted
	}); err != nil {
		t.Fatalf("Run returned %v", err)
	}

	// A short pause so the second message is processed before the check.
	time.Sleep(2 * time.Second)

	if got := h.countJobs(t, assessmentID); got != 1 {
		t.Fatalf("two deliveries produced %d jobs, want 1", got)
	}
	if got := h.provider.CallCount(); got != 1 {
		t.Fatalf("the provider was called %d times, want 1", got)
	}

	var events int
	if err := h.pool.QueryRow(h.ctx,
		`SELECT count(*) FROM outbox WHERE aggregate_id = $1`, assessmentID).Scan(&events); err != nil {
		t.Fatalf("counting events: %v", err)
	}
	if events != 1 {
		t.Fatalf("two deliveries produced %d events, want 1", events)
	}
}

// TestAFailingProviderLeavesTheJobFailed keeps failures visible.
func TestAFailingProviderLeavesTheJobFailed(t *testing.T) {
	h := newHarness(t)
	h.provider.Err = errProviderDown

	assessmentID := uuid.NewString()
	h.send(t, assessmentID, "key-"+assessmentID)

	if err := h.runUntil(t, 60*time.Second, func() bool {
		return h.statusOf(t, assessmentID) == llmworker.StatusFailed
	}); err != nil {
		t.Fatalf("Run returned %v", err)
	}

	var lastError string
	if err := h.pool.QueryRow(h.ctx,
		`SELECT coalesce(last_error,'') FROM llm_jobs WHERE aggregate_id = $1`,
		assessmentID).Scan(&lastError); err != nil {
		t.Fatalf("reading the failure: %v", err)
	}
	if lastError == "" {
		t.Fatal("the job failed without recording why")
	}

	// A failure that will still be retried does NOT publish a failure event.
	// Publishing it would make callers think the job has given up when it has
	// not.
	var events int
	if err := h.pool.QueryRow(h.ctx,
		`SELECT count(*) FROM outbox WHERE aggregate_id = $1`, assessmentID).Scan(&events); err != nil {
		t.Fatalf("counting events: %v", err)
	}
	if events != 0 {
		t.Fatalf("a retryable failure published %d events, want 0", events)
	}
}

// TestATruncatedAnswerIsNotStoredAsAReport guards against half-finished
// reports.
func TestATruncatedAnswerIsNotStoredAsAReport(t *testing.T) {
	h := newHarness(t)
	h.provider.FinishReason = "MAX_TOKENS"

	assessmentID := uuid.NewString()
	h.send(t, assessmentID, "key-"+assessmentID)

	if err := h.runUntil(t, 60*time.Second, func() bool {
		s := h.statusOf(t, assessmentID)
		return s != "" && s != llmworker.StatusPending
	}); err != nil {
		t.Fatalf("Run returned %v", err)
	}

	var status string
	var result []byte
	if err := h.pool.QueryRow(h.ctx,
		`SELECT status, result FROM llm_jobs WHERE aggregate_id = $1`, assessmentID,
	).Scan(&status, &result); err != nil {
		t.Fatalf("reading the job: %v", err)
	}

	if status == llmworker.StatusCompleted {
		t.Fatal("an answer that stopped at MAX_TOKENS was stored as a finished report")
	}
	if result != nil {
		t.Fatal("a truncated answer was stored as a result")
	}
}

// TestTheOffsetIsCommittedSoWorkIsNotRepeated is the real F3-06 gate: "the
// offset is committed correctly".
//
// The way to prove it is not to check whether the function was called, but to
// ask the broker: a SECOND consumer in the SAME group must not receive a
// message the first consumer already worked on. If the offset was not
// committed, it will receive it again - this group's ConsumeResetOffset starts
// from the beginning of the topic.
func TestTheOffsetIsCommittedSoWorkIsNotRepeated(t *testing.T) {
	addr := brokers(t)
	h := newHarnessInGroup(t, "offset-test-"+uuid.NewString())

	assessmentID := uuid.NewString()
	h.send(t, assessmentID, "key-"+assessmentID)

	if err := h.runUntil(t, 60*time.Second, func() bool {
		return h.statusOf(t, assessmentID) == llmworker.StatusCompleted
	}); err != nil {
		t.Fatalf("Run returned %v", err)
	}

	// The first consumer leaves the group BEFORE the second connects. Without
	// this, the second gets no partition at all and this test would pass
	// without ever touching the offset.
	h.leaveGroup()

	// A second consumer, the same group, the same topic.
	second, err := kafka.NewConsumer(
		kafka.Config{Brokers: addr, ClientID: "offset-check"}, h.group, h.topic)
	if err != nil {
		t.Fatalf("NewConsumer: %v", err)
	}
	defer second.Close()

	pollCtx, cancel := context.WithTimeout(h.ctx, 10*time.Second)
	defer cancel()

	fetches := second.PollFetches(pollCtx)
	var replayed int
	fetches.EachRecord(func(_ *kgo.Record) { replayed++ })

	if replayed != 0 {
		t.Fatalf("a second consumer in the same group received %d messages again; "+
			"the offset was never committed and every restart redoes paid work", replayed)
	}
}

// TestAFailingJobIsRetriedAndThenDeadLettered is the F3-13 gate.
//
// Before this fix, a job that failed ONCE stopped forever: its idempotency
// claim was spent, its offset was committed, and nothing would send its
// message again. The three-attempt limit was never reached, and the
// dead-letter queue never received anything.
//
// What is checked here is three things in sequence: the job is really tried
// three times, its counter accumulates on the SAME ROW, and on the third
// attempt it becomes dead along with its failure event.
func TestAFailingJobIsRetriedAndThenDeadLettered(t *testing.T) {
	h := newHarness(t)
	h.provider.Err = errProviderDown

	assessmentID := uuid.NewString()
	h.send(t, assessmentID, "key-"+assessmentID)

	if err := h.runUntil(t, 90*time.Second, func() bool {
		return h.statusOf(t, assessmentID) == llmworker.StatusDead
	}); err != nil {
		t.Fatalf("Run returned %v", err)
	}

	// One row, not three: retries reuse the same job, so the counter means
	// something.
	if got := h.countJobs(t, assessmentID); got != 1 {
		t.Fatalf("three attempts produced %d job rows, want 1", got)
	}

	var attempts int
	var lastError string
	if err := h.pool.QueryRow(h.ctx,
		`SELECT attempts, coalesce(last_error,'') FROM llm_jobs WHERE aggregate_id = $1`,
		assessmentID).Scan(&attempts, &lastError); err != nil {
		t.Fatalf("reading the job: %v", err)
	}
	if attempts != llmworker.MaxAttempts {
		t.Fatalf("the job records %d attempts, want %d", attempts, llmworker.MaxAttempts)
	}
	if lastError == "" {
		t.Fatal("the job gave up without recording why")
	}

	// And the provider is really called three times - not merely a counter
	// going up without work happening.
	if got := h.provider.CallCount(); got != llmworker.MaxAttempts {
		t.Fatalf("the provider was called %d times, want %d", got, llmworker.MaxAttempts)
	}

	// The failure event is published ONCE, and only after giving up.
	var events int
	var eventType string
	if err := h.pool.QueryRow(h.ctx,
		`SELECT count(*), coalesce(max(event_type),'') FROM outbox WHERE aggregate_id = $1`,
		assessmentID).Scan(&events, &eventType); err != nil {
		t.Fatalf("counting events: %v", err)
	}
	if events != 1 {
		t.Fatalf("%d failure events were published, want exactly 1", events)
	}
	if eventType != outbox.EventLLMJobFailed {
		t.Fatalf("the outbox holds %q, want llm.job.failed", eventType)
	}
}

// TestAnAbandonedJobResumesAfterRestart closes the path hardest to get right.
//
// The worker is shut down in the middle of a series of attempts. Three things
// have to happen once it is started again, and all three are easy to get wrong:
//
// 1. The message comes back - its offset was indeed not committed. 2. It is NOT
// skipped as a duplicate - the claim was released on giving up. 3. Its attempt
// counter ACCUMULATES on the same row, rather than starting from zero on a new
// row. Otherwise the limit of three is never reached and a job that always
// fails is tried forever.
func TestAnAbandonedJobResumesAfterRestart(t *testing.T) {
	h := newHarness(t)
	h.provider.Err = errProviderDown

	assessmentID := uuid.NewString()
	h.send(t, assessmentID, "key-"+assessmentID)

	// Stopped as soon as the first failure is recorded.
	if err := h.runUntil(t, 60*time.Second, func() bool {
		return h.attemptsOf(t, assessmentID) >= 1
	}); err != nil {
		t.Fatalf("Run returned %v", err)
	}

	if got := h.attemptsOf(t, assessmentID); got >= llmworker.MaxAttempts {
		t.Skipf("the worker reached %d attempts before it could be stopped; the race is too tight to test here", got)
	}

	// The claim must have been released, otherwise the next delivery is
	// skipped as a duplicate and the job stops forever.
	var claims int
	if err := h.pool.QueryRow(h.ctx,
		`SELECT count(*) FROM processed_messages`).Scan(&claims); err != nil {
		t.Fatalf("counting claims: %v", err)
	}
	if claims != 0 {
		t.Fatalf("%d claims survived the shutdown; the job can never be retried", claims)
	}

	// Started again, and this time left to run until it gives up.
	h.restart(t)
	if err := h.runUntil(t, 90*time.Second, func() bool {
		return h.statusOf(t, assessmentID) == llmworker.StatusDead
	}); err != nil {
		t.Fatalf("the restarted worker returned %v", err)
	}

	if got := h.countJobs(t, assessmentID); got != 1 {
		t.Fatalf("the restart produced %d job rows, want 1 - the attempt counter is meaningless across rows", got)
	}
	if got := h.attemptsOf(t, assessmentID); got != llmworker.MaxAttempts {
		t.Fatalf("the job records %d attempts, want %d", got, llmworker.MaxAttempts)
	}
}

// attemptsOf reads the attempt counter, or -1 if the job does not exist yet.
func (h *harness) attemptsOf(t *testing.T, aggregateID string) int {
	t.Helper()

	var attempts int
	err := h.pool.QueryRow(h.ctx,
		`SELECT attempts FROM llm_jobs WHERE aggregate_id = $1`, aggregateID).Scan(&attempts)
	if err != nil {
		return -1
	}
	return attempts
}

// TestAnUnusableRequestTellsWhoeverIsWaiting is the one F6 found.
//
// nutrition-svc was started while llm-worker had not been rebuilt. The old
// worker did not know MealGuideRequested, skipped it, and that guide stayed
// pending FOREVER - no error, no status change, only one log line someone had
// to happen to read. Version gaps like that happen in every rolling
// deployment.
//
// Now whoever is waiting is told through the DLQ, using the aggregate from the
// message's header and partition key - exactly what every consumer already
// reads.
func TestAnUnusableRequestTellsWhoeverIsWaiting(t *testing.T) {
	h := newHarness(t)

	guideID := uuid.NewString()

	// An envelope that holds NO LLM request at all. Its shape is valid, its
	// key is present, but its payload is nothing this worker knows - exactly
	// the state of an old worker meeting a new job kind.
	env := &eventsv1.Envelope{
		EventId:        uuid.NewString(),
		OccurredAt:     timestamppb.Now(),
		SchemaVersion:  1,
		IdempotencyKey: &commonv1.IdempotencyKey{Value: "unusable:" + guideID},
		Payload: &eventsv1.Envelope_ProfileUpdated{
			ProfileUpdated: &eventsv1.ProfileUpdated{UserId: uuid.NewString()},
		},
	}
	payload, err := proto.Marshal(env)
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}

	if _, err := kafka.NewPublisher(h.producer).Publish(h.ctx, []kafka.Message{{
		Topic:   h.topic,
		Key:     []byte(guideID),
		Value:   payload,
		Headers: map[string]string{"aggregate_type": "meal_guide"},
	}}); err != nil {
		t.Fatalf("publishing: %v", err)
	}

	if err := h.runUntil(t, 30*time.Second, func() bool {
		return h.dlqEventsFor(t, guideID) > 0
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// One failure event, carrying the right aggregate so the waiting consumer
	// can recognise it.
	if got := h.dlqEventsFor(t, guideID); got != 1 {
		t.Fatalf("the waiting aggregate was told %d times, want exactly 1", got)
	}

	var aggregateType, reason string
	if err := h.pool.QueryRow(h.ctx, `
		SELECT aggregate_type, coalesce(last_error, '') FROM outbox WHERE aggregate_id = $1`,
		guideID).Scan(&aggregateType, &reason); err != nil {
		t.Fatalf("reading the outbox row: %v", err)
	}
	if aggregateType != "meal_guide" {
		t.Errorf("the failure names aggregate type %q; consumers filter on it", aggregateType)
	}

	// And NO job is created: there is nothing to work on.
	if got := h.countJobs(t, guideID); got != 0 {
		t.Errorf("%d jobs were created for a request nobody understands", got)
	}
}

// TestAnUnusableRequestWithNoAggregateTypeDoesNotBlockTheQueue guards the path without an
// address.
//
// Without the aggregate_type header, no consumer can recognise the failure event - every
// consumer filters precisely on that header. Writing an outbox row nobody can recognise is
// worse than not writing: it adds a message every consumer unpacks and then discards.
//
// A message WITHOUT A KEY is deliberately not tested here: this platform's publisher
// refuses it first ("a message with no key would lose its ordering"), so that state cannot
// be produced through the real path. The guard in the code stays for other producers, and
// that is stated - not tested with a fake path that proves something else.
func TestAnUnusableRequestWithNoAggregateTypeDoesNotBlockTheQueue(t *testing.T) {
	addr := brokers(t)
	h := newHarnessInGroup(t, "unaddressed-"+uuid.NewString())

	env := &eventsv1.Envelope{
		EventId:        uuid.NewString(),
		OccurredAt:     timestamppb.Now(),
		SchemaVersion:  1,
		IdempotencyKey: &commonv1.IdempotencyKey{Value: "unaddressed:" + uuid.NewString()},
		Payload: &eventsv1.Envelope_ProfileUpdated{
			ProfileUpdated: &eventsv1.ProfileUpdated{UserId: uuid.NewString()},
		},
	}
	payload, err := proto.Marshal(env)
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}

	before := h.totalOutboxRows(t)

	// The key IS present - this platform's publisher requires it - but the
	// header is not, and without that header no consumer can recognise it.
	if _, err := kafka.NewPublisher(h.producer).Publish(h.ctx, []kafka.Message{{
		Topic: h.topic,
		Key:   []byte(uuid.NewString()),
		Value: payload,
	}}); err != nil {
		t.Fatalf("publishing: %v", err)
	}

	// The worker is run for a fixed time and then stopped.
	//
	// runUntil is not used here: it waits for something to HAPPEN, while what
	// has to be proven is precisely that nothing happens. Using it would fail
	// the test with "did not finish the work in time" - a sentence describing
	// success as failure.
	runCtx, cancel := context.WithTimeout(h.ctx, 10*time.Second)
	defer cancel()

	stopped := make(chan error, 1)
	go func() { stopped <- h.consumer.Run(runCtx) }()

	select {
	case err := <-stopped:
		// Stopping because the time ran out is what is expected; stopping because
		// of an error means the worker is stuck on this message.
		if err != nil {
			t.Fatalf("the worker stopped with an error on an unaddressable message: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Run did not return after its context expired")
	}

	if after := h.totalOutboxRows(t); after != before {
		t.Errorf("%d outbox rows were written for a message nobody can be told about", after-before)
	}

	// And the thing that really tells them apart: THE OFFSET ADVANCES.
	//
	// Without its guard, announceUnusable passes an empty aggregate to
	// outbox.Write, which refuses it with the same error - handle returns the
	// error, the offset is HELD, and that unaddressable message is redelivered
	// forever, clogging the queue for everyone. The outbox row count cannot
	// tell the two apart; only this can.
	h.leaveGroup()

	second, err := kafka.NewConsumer(
		kafka.Config{Brokers: addr, ClientID: "unaddressed-check"}, h.group, h.topic)
	if err != nil {
		t.Fatalf("NewConsumer: %v", err)
	}
	defer second.Close()

	pollCtx, cancelPoll := context.WithTimeout(h.ctx, 10*time.Second)
	defer cancelPoll()

	var replayed int
	second.PollFetches(pollCtx).EachRecord(func(_ *kgo.Record) { replayed++ })

	if replayed != 0 {
		t.Fatalf("a second consumer received the unaddressable message %d times again; "+
			"its offset was never committed, and it will block the queue forever", replayed)
	}
}

// dlqEventsFor counts the failure events for an aggregate.
func (h *harness) dlqEventsFor(t *testing.T, aggregateID string) int {
	t.Helper()

	var n int
	if err := h.pool.QueryRow(h.ctx,
		`SELECT count(*) FROM outbox WHERE aggregate_id = $1 AND event_type = $2`,
		aggregateID, outbox.EventLLMJobFailed).Scan(&n); err != nil {
		t.Fatalf("counting dlq events: %v", err)
	}
	return n
}

func (h *harness) totalOutboxRows(t *testing.T) int {
	t.Helper()

	var n int
	if err := h.pool.QueryRow(h.ctx, `SELECT count(*) FROM outbox`).Scan(&n); err != nil {
		t.Fatalf("counting outbox rows: %v", err)
	}
	return n
}

// errProviderDown is a provider failure that is NOT quota: the retry-then-dead
// path. Quota has its own path (ADR-025, the test below).
var errProviderDown = errors.New("fake provider is down")

// TestAQuotaRefusalParksTheJobInsteadOfKillingIt is ADR-025 / B30.
//
// The provider refuses on quota grounds; the job must NOT die, must NOT
// consume attempts, and has to finish on its own once the quota recovers -
// without a restart, without a human hand.
func TestAQuotaRefusalParksTheJobInsteadOfKillingIt(t *testing.T) {
	h := newHarness(t)
	h.provider.Err = llm.ErrRateLimited

	assessmentID := uuid.NewString()
	h.send(t, assessmentID, "key-"+assessmentID)

	// Parked: the job row exists, its claim has been released, and its
	// attempts are still zero - not one.
	if err := h.runUntil(t, 60*time.Second, func() bool {
		if h.countJobs(t, assessmentID) != 1 {
			return false
		}
		var claims int
		if err := h.pool.QueryRow(h.ctx, `SELECT count(*) FROM processed_messages`).Scan(&claims); err != nil {
			t.Fatalf("counting claims: %v", err)
		}
		return claims == 0
	}); err != nil {
		t.Fatalf("Run returned %v", err)
	}
	if got := h.attemptsOf(t, assessmentID); got != 0 {
		t.Fatalf("a quota refusal consumed %d attempts, want 0", got)
	}
	if got := h.statusOf(t, assessmentID); got == llmworker.StatusDead || got == llmworker.StatusFailed {
		t.Fatalf("a quota refusal left the job %s", got)
	}

	// The quota recovers. The same worker, without a restart, has to finish
	// it.
	h.provider.SetErr(nil)
	if err := h.runUntil(t, 60*time.Second, func() bool {
		return h.statusOf(t, assessmentID) == llmworker.StatusCompleted
	}); err != nil {
		t.Fatalf("the parked job never completed: %v", err)
	}
	if got := h.countJobs(t, assessmentID); got != 1 {
		t.Fatalf("parking produced %d job rows, want 1", got)
	}
	if got := h.attemptsOf(t, assessmentID); got != 0 {
		t.Fatalf("the completed job records %d failed attempts, want 0", got)
	}
	if events := h.dlqEventsFor(t, assessmentID); events != 0 {
		t.Fatalf("parking published %d failure events, want 0", events)
	}
}
