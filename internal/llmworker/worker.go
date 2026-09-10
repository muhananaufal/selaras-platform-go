package llmworker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/twmb/franz-go/pkg/kgo"
	"go.opentelemetry.io/otel/attribute"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	eventsv1 "github.com/muhananaufal/selaras-platform-go/gen/events/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/llm"
	"github.com/muhananaufal/selaras-platform-go/internal/llm/prompt"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/idempotency"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/kafka"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/outbox"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/telemetry"
)

// Scope is the idempotency scope of this worker.
//
// It is fixed, and that matters: changing it makes every job ever done look
// as if it had never been done, and all of it is done again.
const Scope = "llm-worker"

// MaxAttempts is how many times a message is tried before it goes to the
// dead-letter queue.
const MaxAttempts = 3

// notYetInTheEvent marks a prompt field the event does not carry yet.
//
// It is stated as it is, not filled with a guess. A guess here would reach the
// model as a fact about a person, and the report it produces would look like an
// ordinary report. F3-10 completes the event.
const notYetInTheEvent = "not yet carried by the event"

// Consumer reads llm.jobs and works on them.
type Consumer struct {
	client   *kgo.Client
	pool     pg.Beginner
	provider llm.Provider
	prompts  *prompt.Library
	jobs     *Repository
	log      *slog.Logger

	// metrics may be nil. A worker without metrics records less, but does not
	// behave differently - and making it mandatory would force tests to set up
	// a meter that tests nothing.
	metrics *Metrics

	// cooldown determines how long the worker stops taking jobs after the
	// provider refuses on quota grounds (ADR-025). Its argument is the number
	// of consecutive refusals; zero after one successful answer.
	cooldown func(consecutive int) time.Duration

	// quotaHits counts consecutive quota refusals. Touched only by the Run
	// loop.
	quotaHits int
}

// DefaultQuotaCooldown is the default policy: one minute, doubling with
// every consecutive refusal, at most fifteen minutes.
//
// One minute because a per-minute quota recovers within a minute; fifteen
// minutes because a per-day quota does not recover however long one waits,
// and one failed request every fifteen minutes is a cheap price for jobs
// that never die.
func DefaultQuotaCooldown(consecutive int) time.Duration {
	const base, ceiling = time.Minute, 15 * time.Minute
	if consecutive <= 1 {
		return base
	}
	d := base << (consecutive - 1)
	if d > ceiling || d <= 0 {
		return ceiling
	}
	return d
}

// NewConsumer assembles the worker.
func NewConsumer(
	client *kgo.Client,
	pool pg.Beginner,
	provider llm.Provider,
	prompts *prompt.Library,
	log *slog.Logger,
) (*Consumer, error) {
	switch {
	case client == nil:
		return nil, errors.New("nil kafka client")
	case pool == nil:
		return nil, errors.New("nil pool")
	case provider == nil:
		return nil, errors.New("nil llm provider")
	case prompts == nil:
		return nil, errors.New("nil prompt library")
	case log == nil:
		return nil, errors.New("nil logger")
	}
	return &Consumer{
		client: client, pool: pool, provider: provider,
		prompts: prompts, jobs: NewRepository(), log: log,
		cooldown: DefaultQuotaCooldown,
	}, nil
}

// WithQuotaCooldown replaces the quota cooldown policy; used by tests so they
// need not wait a minute.
func (c *Consumer) WithQuotaCooldown(f func(consecutive int) time.Duration) *Consumer {
	if f != nil {
		c.cooldown = f
	}
	return c
}

// WithMetrics installs the queue instruments (F3-15).
//
// Separate from NewConsumer so tests need not set up a meter that tests
// nothing, and so the worker can still run when telemetry fails to set up -
// missing metrics are far lighter in consequence than a worker that refuses to
// start.
func (c *Consumer) WithMetrics(m *Metrics) *Consumer {
	c.metrics = m
	return c
}

// Run reads until ctx is done.
//
// Its shutdown is clean, and "clean" has a precise meaning here: the job in
// progress is finished, its offset is committed, AND ONLY THEN does the loop
// stop. Stopping midway without committing means a job already done is done
// again by the next process - not harmful, since idempotency holds it back, but
// a waste of paid provider time.
func (c *Consumer) Run(ctx context.Context) error {
	c.log.InfoContext(ctx, "llm worker started", "scope", Scope)

	for {
		if ctx.Err() != nil {
			c.log.InfoContext(ctx, "llm worker stopped")
			//nolint:nilerr // A requested stop is not a failure; see the Run comment.
			return nil
		}

		fetches := c.client.PollFetches(ctx)
		if ctx.Err() != nil {
			// Cancellation while waiting is not a failure. PollFetches returns the
			// context error here, and reporting it as breakage would make every
			// clean shutdown look bad.
			c.log.InfoContext(ctx, "llm worker stopped")
			//nolint:nilerr // Likewise: PollFetches returns the context error when cancelled.
			return nil
		}

		if errs := fetches.Errors(); len(errs) > 0 {
			// A topic recreated on the broker (B26): resubscribed here, not through
			// a restart. franz-go deliberately does not recover on its own.
			if recovered := kafka.RecoverRecreatedTopics(c.client, errs); len(recovered) > 0 {
				c.log.WarnContext(ctx, "topics were recreated on the broker; subscribed again", "topics", recovered)
			}
			for _, e := range errs {
				c.log.ErrorContext(ctx, "fetching from kafka failed",
					"topic", e.Topic, "partition", e.Partition, "error", e.Err)
			}
			// A broker electing a leader is a transient state. A short pause so the
			// loop does not spin flat out against the same error.
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(time.Second):
			}
			continue
		}

		var handled int
		rewinder := kafka.NewRewinder()
		var pause time.Duration
		fetches.EachRecord(func(rec *kgo.Record) {
			// ctx.Err() is checked PER MESSAGE, not only per iteration. One batch
			// can hold hundreds of messages each waiting tens of seconds on the
			// network; without this check, shutdown waits for the whole batch to
			// finish.
			if ctx.Err() != nil {
				return
			}
			err := c.handle(ctx, rec)
			var parked *parkedError
			switch {
			case err == nil:
			case errors.As(err, &parked):
				// The provider refused on quota grounds (ADR-025): the job is released,
				// its offset held, and the worker pauses briefly - rather than
				// recording a failure that brings it closer to dead.
				c.quotaHits++
				if d := c.cooldown(c.quotaHits); d > pause {
					pause = d
				}
				c.log.WarnContext(ctx, "the provider is out of quota; parking the queue",
					"consecutive", c.quotaHits, "cooldown", pause, "error", parked.cause)
				rewinder.Failed(rec)
			default:
				// Other errors - Postgres unreachable at claim time, say - hold the
				// offset so the message comes back. Before this, the offset was
				// committed anyway and the job was silently lost.
				c.log.ErrorContext(ctx, "handling a job failed",
					"offset", rec.Offset, "partition", rec.Partition, "error", err)
				rewinder.Failed(rec)
			}
			handled++
		})

		if handled == 0 {
			continue
		}
		if rewinder.Any() {
			// The same as the other consumers: not committing alone is not enough,
			// franz-go does not redeliver within the same session.
			rewinder.Rewind(c.client)
			if pause < time.Second {
				pause = time.Second
			}
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(pause):
			}
			continue
		}

		// The offset is committed AFTER the job is finished, not on a timer.
		//
		// Auto-commit marks messages done by the clock: a job that failed halfway
		// is still recorded as done, and its message never comes back. That is
		// why this client is built with DisableAutoCommit.
		if err := c.client.CommitUncommittedOffsets(ctx); err != nil {
			// A failed commit means the same message will come back. Idempotency
			// holds it back, so this is not breakage - but it has to be visible, not
			// lost.
			c.log.ErrorContext(ctx, "committing offsets failed", "error", err)
		}
	}
}

// handle works on one message.
//
// The flow: claim -> work -> store the result and its outgoing event, IN ONE
// transaction for the parts that touch the database. The call to the provider
// is deliberately OUTSIDE the transaction: it can wait tens of seconds, and a
// transaction gaping open that long holds a connection and locks for no
// reason.
func (c *Consumer) handle(ctx context.Context, rec *kgo.Record) (err error) {
	var env eventsv1.Envelope
	if err := proto.Unmarshal(rec.Value, &env); err != nil {
		// An unreadable message will never become readable. It is skipped, not
		// retried forever - and logged so it can be investigated.
		c.log.ErrorContext(ctx, "a message could not be decoded and was skipped",
			"offset", rec.Offset, "error", err)
		return nil
	}

	// The consumer span becomes a child of the request that wrote this event
	// (F9-05); an error returned by the handler is recorded on its span.
	ctx, span := telemetry.StartConsumerSpan(ctx, &env, rec)
	defer func() { telemetry.End(span, err) }()

	key := idempotencyKeyOf(&env)
	if key == "" {
		c.log.ErrorContext(ctx, "a message carried no idempotency key and was skipped",
			"event_id", env.GetEventId())
		return nil
	}

	req, err := requestOf(&env)
	if err != nil {
		// A message of an unrecognised kind can never be worked on. It is
		// skipped, not retried forever - but whoever is waiting for it is TOLD,
		// not left waiting without end.
		c.log.ErrorContext(ctx, "a message carried no usable LLM request",
			"event_id", env.GetEventId(), "error", err)
		if err := c.announceUnusable(ctx, &env, rec, err); err != nil {
			// Still skipped, not held: holding the offset for a message that belongs
			// to nobody clogs the queue forever (see its test). The failure to
			// announce it is logged, that is all.
			c.log.ErrorContext(ctx, "an unusable request could not be announced and was skipped",
				"event_id", env.GetEventId(), "error", err)
		}
		return nil
	}

	// Stage one: claim. If the key has been used before, the job has been done
	// and there is nothing to do - this is what holds back duplicates from the
	// at-least-once relay.
	claimed, job, err := c.claim(ctx, key, req)
	if err != nil {
		return err
	}
	if !claimed {
		c.log.InfoContext(ctx, "a job arrived again and was skipped",
			"idempotency_key", key, "event_id", env.GetEventId())
		c.metrics.Observe(ctx, OutcomeSkipped, 0)
		return nil
	}

	started := time.Now()

	// Stage two: work, with retries IN-PROCESS.
	//
	// This choice is deliberate, and the alternative was tried and discarded:
	// leaving the offset uncommitted does NOT make the broker send the message
	// again to the same consumer - it only takes effect after a rebalance or
	// restart. A failed job would stop forever in the failed status, the
	// three-attempt limit would never be reached, and the dead-letter queue would
	// never receive anything.
	//
	// Rewinding the offset through SetOffsets is possible, but franz-go itself
	// warns against using it inside the PollFetches loop as "prone to odd
	// interactions" [franz-go@v1.21.6/pkg/kgo/consumer.go:763-778].
	//
	// So the message is held here until it finishes or gives up. Its partition is
	// held along with it for that long - and that is precisely what keeps the
	// order per aggregate.
	return c.work(ctx, job, req, started)
}

// work tries the job until it succeeds or gives up.
func (c *Consumer) work(
	ctx context.Context, job *Job, req *Request, started time.Time,
) error {
	for attempt := job.Attempts; attempt < MaxAttempts; attempt++ {
		answer, genErr := c.generate(ctx, req)
		if genErr == nil {
			c.quotaHits = 0
			err := c.recordSuccess(ctx, job, req, answer)
			if err == nil {
				c.metrics.Observe(ctx, OutcomeCompleted, time.Since(started))
			}
			return err
		}

		if ctx.Err() != nil {
			// Shut down in the middle of an attempt. The claim is released so the
			// next delivery - after a restart, with the offset indeed uncommitted -
			// really works on it instead of skipping it as a duplicate.
			c.metrics.Observe(ctx, OutcomeAbandoned, time.Since(started))
			return c.abandon(ctx, job, genErr)
		}

		if errors.Is(genErr, llm.ErrRateLimited) {
			// Quota, not a job failure (ADR-025). The claim is released so the
			// redelivery works on it again, the attempt counter is untouched, and
			// the Run loop decides how long to stay quiet.
			c.metrics.Observe(ctx, OutcomeParked, time.Since(started))
			if err := c.release(ctx, job); err != nil {
				return err
			}
			return &parkedError{cause: genErr}
		}

		dead := attempt+1 >= MaxAttempts
		if err := c.recordFailure(ctx, job, req, genErr, dead); err != nil {
			return err
		}
		if dead {
			c.metrics.Observe(ctx, OutcomeDead, time.Since(started))
			return nil
		}
		c.metrics.Observe(ctx, OutcomeFailed, time.Since(started))

		select {
		case <-ctx.Done():
			c.metrics.Observe(ctx, OutcomeAbandoned, time.Since(started))
			return c.abandon(ctx, job, genErr)
		case <-time.After(retryDelay(attempt)):
		}
	}
	return nil
}

// retryDelay is the pause before the next attempt.
//
// Short, because the provider itself already retries with a longer backoff
// inside. What is handled here is the failures that got past that layer -
// and waiting long for them only holds the partition.
func retryDelay(attempt int) time.Duration {
	return time.Duration(attempt+1) * 500 * time.Millisecond
}

// abandon releases a job halted because the process was shut down.
//
// It runs with a separate context: the caller's ctx is already cancelled,
// and using it would make the release itself fail - leaving a claim that
// closes the key forever.
func (c *Consumer) abandon(ctx context.Context, job *Job, cause error) error {
	c.log.WarnContext(ctx, "a job was abandoned mid-flight and will be retried after restart",
		"job_id", job.ID, "attempts", job.Attempts, "error", cause)

	return c.release(ctx, job)
}

// release lets go of a job's claim so the next delivery works on it again,
// instead of skipping it as a duplicate.
//
// A separate context: the caller may arrive with an already cancelled ctx
// (shutdown), and a failed release leaves a claim that closes the key forever.
func (c *Consumer) release(ctx context.Context, job *Job) error {
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()

	return pg.InTx(releaseCtx, c.pool, func(q pg.Querier) error {
		guard, err := idempotency.NewGuard(q, Scope)
		if err != nil {
			return err
		}
		return guard.Release(releaseCtx, job.Key)
	})
}

// parkedError marks a job released because the provider's quota is
// exhausted.
type parkedError struct{ cause error }

func (e *parkedError) Error() string {
	return "parked until the provider quota recovers: " + e.cause.Error()
}

func (e *parkedError) Unwrap() error { return e.cause }

// claim creates a new job if its key has never been used.
func (c *Consumer) claim(
	ctx context.Context, key string, req *Request,
) (claimed bool, job *Job, err error) {
	err = pg.InTx(ctx, c.pool, func(q pg.Querier) error {
		guard, err := idempotency.NewGuard(q, Scope)
		if err != nil {
			return err
		}

		ok, err := guard.Claim(ctx, key)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}

		// An existing job with this key is reused, not created anew. It exists if
		// a previous attempt was halted midway - the process died while retrying
		// - and its attempt counter has to accumulate on the same row, otherwise
		// the limit of three is never reached.
		existing, found, err := c.jobs.ByKey(ctx, q, key)
		if err != nil {
			return err
		}
		if found {
			job = existing
			claimed = true
			return nil
		}

		job = &Job{
			Key:           key,
			Kind:          req.Kind,
			AggregateType: req.AggregateType,
			AggregateID:   req.AggregateID,
		}
		if err := c.jobs.Create(ctx, q, job); err != nil {
			return err
		}
		claimed = true
		return nil
	})
	if err != nil {
		return false, nil, fmt.Errorf("claiming the job: %w", err)
	}
	return claimed, job, nil
}

// generate memanggil penyedia.
func (c *Consumer) generate(
	ctx context.Context, req *Request,
) (*llm.Response, error) {
	// The template is chosen by the request, not fixed here: one worker
	// handles personalisation, curricula, graduation reports, and chat
	// replies, and each has its own prompt.
	tmpl, err := c.prompts.Latest(req.Template)
	if err != nil {
		return nil, err
	}

	// The prompt data is still thin: the event does not carry the profile and
	// history yet. Fields not yet present are stated AS THEY ARE instead of
	// being filled with a guess - a guess here would reach the model as a fact
	// about a person.
	rendered, err := tmpl.Render(req.Data)
	if err != nil {
		return nil, err
	}

	// The call to the provider is the longest part of any trace passing
	// through this worker; it gets its own span so its duration reads
	// separately from the claim and the storing of the result.
	ctx, span := telemetry.StartSpan(ctx, "llm.generate",
		attribute.String("selaras.llm.provider", c.provider.Name()),
		attribute.String("selaras.llm.template", tmpl.ID()))
	answer, err := c.provider.Generate(ctx, llm.Request{
		Prompt:        rendered,
		PromptVersion: tmpl.ID(),
		JSON:          true,
		Temperature:   0.7,
	})
	if err == nil {
		// Tokens are recorded in three places that each have their own reader:
		// the span (one trace), the metrics (the FinOps aggregate), and the log
		// (one job).
		span.SetAttributes(
			attribute.Int("selaras.llm.tokens.input", answer.Usage.InputTokens),
			attribute.Int("selaras.llm.tokens.output", answer.Usage.OutputTokens),
			attribute.Int("selaras.llm.tokens.thoughts", answer.Usage.ThoughtsTokens))
		c.metrics.ObserveUsage(ctx, c.provider.Name(), tmpl.ID(), answer.Usage)
		c.log.InfoContext(ctx, "llm answer received",
			"provider", c.provider.Name(), "model", answer.Model, "template", tmpl.ID(),
			"tokens_input", answer.Usage.InputTokens, "tokens_output", answer.Usage.OutputTokens,
			"tokens_thoughts", answer.Usage.ThoughtsTokens, "finish_reason", answer.FinishReason)
	}
	telemetry.End(span, err)
	return answer, err
}

// recordSuccess stores the result and publishes its completion event.
func (c *Consumer) recordSuccess(
	ctx context.Context, job *Job,
	req *Request, answer *llm.Response,
) error {
	if answer.Truncated() {
		// A truncated answer is not an answer. Storing it as a complete report
		// would display a half-finished analysis as if it were whole. A truncated
		// answer will not improve by retrying: the same prompt, the same model,
		// the same limit. It gives up immediately.
		return c.recordFailure(ctx, job, req,
			fmt.Errorf("%w: the provider stopped at %q", llm.ErrTruncated, answer.FinishReason), true)
	}

	return pg.InTx(ctx, c.pool, func(q pg.Querier) error {
		if err := c.jobs.Complete(ctx, q, job.ID, job.CreatedAt,
			[]byte(answer.Text), answer.PromptVersion, answer.Model); err != nil {
			return err
		}

		return outbox.NewWriter(q).Write(ctx, req.AggregateType, req.AggregateID,
			completionEvent(job, req, answer))
	})
}

// completionEvent composes the result event, according to the job kind.
//
// Different event kinds land on different topics (see outbox.TopicFor), and
// that is what spares the assessment consumer from filtering out coaching
// results and vice versa.
func completionEvent(job *Job, req *Request, answer *llm.Response) *eventsv1.Envelope {
	env := &eventsv1.Envelope{
		EventId:       job.ID.String(),
		OccurredAt:    timestamppb.Now(),
		SchemaVersion: 1,
	}

	switch req.Kind {
	case KindCurriculum, KindGraduation:
		env.Payload = &eventsv1.Envelope_CurriculumCompleted{
			CurriculumCompleted: &eventsv1.CurriculumCompleted{
				ProgramId:      req.AggregateID,
				JobId:          job.ID.String(),
				CurriculumJson: answer.Text,
				PromptVersion:  answer.PromptVersion,
			},
		}

	case KindMealGuide:
		env.Payload = &eventsv1.Envelope_MealGuideCompleted{
			MealGuideCompleted: &eventsv1.MealGuideCompleted{
				GuideId:       req.AggregateID,
				JobId:         job.ID.String(),
				GuideJson:     answer.Text,
				PromptVersion: answer.PromptVersion,
			},
		}

	case KindChatReply:
		env.Payload = &eventsv1.Envelope_ChatReplyCompleted{
			ChatReplyCompleted: &eventsv1.ChatReplyCompleted{
				JobId:         job.ID.String(),
				ReplyJson:     answer.Text,
				PromptVersion: answer.PromptVersion,
			},
		}

	default:
		env.Payload = &eventsv1.Envelope_PersonalizationCompleted{
			PersonalizationCompleted: &eventsv1.PersonalizationCompleted{
				AssessmentId:  req.AggregateID,
				JobId:         job.ID.String(),
				ReportJson:    answer.Text,
				PromptVersion: answer.PromptVersion,
			},
		}
	}
	return env
}

// recordFailure records a failure, and publishes the failure event once the
// job will not be tried again.
func (c *Consumer) recordFailure(
	ctx context.Context, job *Job,
	req *Request, cause error, dead bool,
) error {

	c.log.ErrorContext(ctx, "a job failed",
		"job_id", job.ID, "attempts", job.Attempts+1, "dead", dead, "error", cause)

	return pg.InTx(ctx, c.pool, func(q pg.Querier) error {
		if err := c.jobs.Fail(ctx, q, job.ID, job.CreatedAt, cause.Error(), dead); err != nil {
			return err
		}
		if !dead {
			return nil
		}

		// The failure event is published only when the job really stops being
		// tried. Publishing it on every failure would make callers think the job
		// has given up when it will still be retried.
		return outbox.NewWriter(q).Write(ctx, req.AggregateType, req.AggregateID, &eventsv1.Envelope{
			EventId:       job.ID.String(),
			OccurredAt:    timestamppb.Now(),
			SchemaVersion: 1,
			Payload: &eventsv1.Envelope_LlmJobFailed{
				LlmJobFailed: &eventsv1.LlmJobFailed{
					JobId:  job.ID.String(),
					Reason: truncate(cause.Error(), 500),
				},
			},
		})
	})
}

// idempotencyKeyOf picks the key used to hold back duplicates.
//
// A key sent by the caller wins; if there is none, the event_id is used. Both
// are needed: the first keeps a repeated request from the same user from
// producing two jobs, the second keeps a redelivery from the relay from
// producing two jobs.
func idempotencyKeyOf(env *eventsv1.Envelope) string {
	if key := env.GetIdempotencyKey().GetValue(); key != "" {
		return key
	}
	return env.GetEventId()
}

// announceUnusable tells whoever is waiting that the result will not come.
//
// A message of an unrecognised kind can never be worked on, and merely skipping
// it leaves the aggregate waiting for it waiting FOREVER - no error, no status
// change, nothing but one log line someone has to happen to read.
//
// That is not a theoretical possibility: it happened when nutrition-svc was
// started with an llm-worker that had not been rebuilt. The old worker did not
// know MealGuideRequested, skipped it, and that guide stayed pending forever.
// Version gaps like that happen in every rolling deployment.
//
// What is published is LlmJobFailed to the DLQ, with the aggregate taken from
// the message's header and partition key - exactly what every consumer already
// reads to recognise a failure. Without both, there is nobody to tell, and the
// message is only logged.
func (c *Consumer) announceUnusable(
	ctx context.Context, env *eventsv1.Envelope, rec *kgo.Record, cause error,
) error {
	// A message without an aggregate is NOT checked again here.
	//
	// outbox.Write already refuses it under exactly the same condition, and
	// copying that here only produces a second branch no test can distinguish
	// - I wrote it, and then a mutation proved that removing it changed
	// nothing. One place of enforcement, not two that will drift.
	//
	// What happens without an aggregate: Write returns an error, the error is
	// logged with the offset and event_id, and the offset still ADVANCES -
	// this worker does commit after every batch, because its retries are
	// in-process (F3-13), not through redelivery. The queue is not clogged.
	aggregateType := headerOf(rec, "aggregate_type")
	aggregateID := string(rec.Key)

	return pg.InTx(ctx, c.pool, func(q pg.Querier) error {
		return outbox.NewWriter(q).Write(ctx, aggregateType, aggregateID, &eventsv1.Envelope{
			EventId:       uuid.NewString(),
			OccurredAt:    timestamppb.Now(),
			SchemaVersion: 1,
			Payload: &eventsv1.Envelope_LlmJobFailed{
				LlmJobFailed: &eventsv1.LlmJobFailed{
					Reason: truncate("this worker does not understand the request: "+cause.Error(), 500),
				},
			},
		})
	})
}

// headerOf membaca satu header pesan.
func headerOf(rec *kgo.Record, key string) string {
	for _, h := range rec.Headers {
		if h.Key == key {
			return string(h.Value)
		}
	}
	return ""
}
