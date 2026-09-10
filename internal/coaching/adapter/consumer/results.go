// Package consumer reads the results of coaching's LLM jobs.
package consumer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"

	eventsv1 "github.com/muhananaufal/selaras-platform-go/gen/events/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/coaching/app"
	"github.com/muhananaufal/selaras-platform-go/internal/coaching/domain"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/telemetry"
)

// Scope is the idempotency scope of this consumer.
//
// Distinct from llm-worker's and from assessment's: two consumers processing
// the same event must not cancel each other out.
const Scope = "coaching-results"

// Results reads llm.results and stores the results.
type Results struct {
	client *kgo.Client
	svc    *app.Service
	log    *slog.Logger
}

func NewResults(client *kgo.Client, svc *app.Service, log *slog.Logger) (*Results, error) {
	switch {
	case client == nil:
		return nil, errors.New("nil kafka client")
	case svc == nil:
		return nil, errors.New("nil coaching service")
	case log == nil:
		return nil, errors.New("nil logger")
	}
	return &Results{client: client, svc: svc, log: log}, nil
}

// Run reads until ctx is done.
func (r *Results) Run(ctx context.Context) error {
	return loop(ctx, r.client, r.log, "coaching result", r.handle)
}

// aggregateTypeOf reads the aggregate kind from the message header.
//
// The outbox relay fills it from the aggregate_type column of the outbox
// row. It is what lets a consumer tell its own messages from another
// service's WITHOUT unpacking the content.
func aggregateTypeOf(rec *kgo.Record) string {
	for _, h := range rec.Headers {
		if h.Key == "aggregate_type" {
			return string(h.Value)
		}
	}
	return ""
}

// isMine says this message belongs to coaching.
//
// The llm.results and llm.dlq topics are SHARED by every service that uses
// llm-worker. Without this filter, the coaching consumer would try to mark
// an assessment as a program - fail, hold the offset, and clog the queue for
// everyone. This really happened.
func isMine(rec *kgo.Record) bool {
	switch aggregateTypeOf(rec) {
	case "coaching_program", "coaching_thread":
		return true
	default:
		return false
	}
}

// handle processes one result.
func (r *Results) handle(ctx context.Context, rec *kgo.Record) (err error) {
	// Filtered first, before anything is unpacked: another service's message
	// is not a failure, and treating it as one would hold the offset and clog
	// the queue for everyone.
	if !isMine(rec) {
		return nil
	}

	var env eventsv1.Envelope
	if err := proto.Unmarshal(rec.Value, &env); err != nil {
		r.log.ErrorContext(ctx, "a coaching result could not be decoded and was skipped",
			"offset", rec.Offset, "error", err)
		return nil
	}

	// The consumer span becomes a child of the request that wrote this event
	// (F9-05); an error returned by the handler is recorded on its span.
	ctx, span := telemetry.StartConsumerSpan(ctx, &env, rec)
	defer func() { telemetry.End(span, err) }()

	err = r.dispatch(ctx, &env, rec)
	if terminal(err) {
		// A result for a program or thread that no longer exists. Retrying it
		// will never succeed, and holding the offset for it means this consumer
		// rewinds itself every second, forever - that really happened to
		// nutrition and chat after a test account was deleted, and the trace is
		// what exposed it.
		r.log.WarnContext(ctx, "a result arrived for a program or thread that no longer exists and was dropped",
			"event_id", env.GetEventId(), "error", err)
		return nil
	}
	return err
}

// terminal says an error will not heal by retrying.
//
// Only a missing owner. Other errors - Postgres unreachable, a transaction
// conflict - are still returned so the offset is held and the result comes
// back.
func terminal(err error) bool {
	return errors.Is(err, domain.ErrProgramNotFound) || errors.Is(err, domain.ErrThreadNotFound)
}

// dispatch routes one event to its handling.
func (r *Results) dispatch(ctx context.Context, env *eventsv1.Envelope, rec *kgo.Record) error {
	switch payload := env.GetPayload().(type) {
	case *eventsv1.Envelope_CurriculumCompleted:
		return r.storeCurriculumOrReport(ctx, payload.CurriculumCompleted)

	case *eventsv1.Envelope_ChatReplyCompleted:
		return r.storeReply(ctx, env, payload.ChatReplyCompleted, rec)

	case *eventsv1.Envelope_LlmJobFailed:
		return r.markFailed(ctx, payload.LlmJobFailed, rec)

	default:
		// Other events on this topic are not this consumer's business. They are
		// skipped, not failed - failing them would clog the queue with messages
		// that were never its own.
		return nil
	}
}

// storeCurriculumOrReport tells a curriculum apart from a graduation
// report.
//
// Both arrive through the same message; what tells them apart is the SHAPE
// of the content. A curriculum has "weeks", a report does not - and
// guessing it from anything else would store a report as an empty
// curriculum.
func (r *Results) storeCurriculumOrReport(
	ctx context.Context, done *eventsv1.CurriculumCompleted,
) error {
	if done.GetProgramId() == "" {
		r.log.ErrorContext(ctx, "a curriculum result named no program")
		return nil
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(done.GetCurriculumJson()), &payload); err != nil {
		r.log.ErrorContext(ctx, "a curriculum result was not valid JSON",
			"program_id", done.GetProgramId(), "error", err)
		return r.svc.FailCurriculum(ctx, done.GetProgramId(),
			"the worker returned a curriculum that is not valid JSON")
	}

	if _, isCurriculum := payload["weeks"]; !isCurriculum {
		return r.svc.StoreGraduationReport(ctx, done.GetProgramId(), payload)
	}

	curriculum, err := curriculumFrom(payload)
	if err != nil {
		r.log.ErrorContext(ctx, "a curriculum result was unusable",
			"program_id", done.GetProgramId(), "error", err)
		return r.svc.FailCurriculum(ctx, done.GetProgramId(), err.Error())
	}
	return r.svc.StoreCurriculum(ctx, done.GetProgramId(), curriculum)
}

// storeReply stores the model's reply into its thread.
func (r *Results) storeReply(
	ctx context.Context, env *eventsv1.Envelope,
	done *eventsv1.ChatReplyCompleted, rec *kgo.Record,
) error {
	// ChatReplyCompleted does not carry the thread id; the message's partition
	// key does, filled by the relay from the aggregate_id of the outbox row.
	threadID := string(rec.Key)
	if threadID == "" {
		r.log.ErrorContext(ctx, "a chat reply carried no thread key",
			"event_id", env.GetEventId(), "job_id", done.GetJobId())
		return nil
	}

	var content map[string]any
	if err := json.Unmarshal([]byte(done.GetReplyJson()), &content); err != nil {
		// A reply that cannot be read is not stored as a reply. Storing it as-is
		// would show raw JSON to the user.
		r.log.ErrorContext(ctx, "a chat reply was not valid JSON",
			"thread_id", threadID, "error", err)
		return nil
	}

	return r.svc.StoreReply(ctx, threadID, content)
}

// markFailed marks a job that has given up.
func (r *Results) markFailed(
	ctx context.Context, failed *eventsv1.LlmJobFailed, rec *kgo.Record,
) error {
	programID := string(rec.Key)
	if programID == "" {
		r.log.ErrorContext(ctx, "a coaching failure carried no key", "job_id", failed.GetJobId())
		return nil
	}

	r.log.WarnContext(ctx, "a coaching job gave up",
		"program_id", programID, "job_id", failed.GetJobId(), "reason", failed.GetReason())

	return r.svc.FailCurriculum(ctx, programID, failed.GetReason())
}

// curriculumFrom reads a curriculum from the JSON shape the model returns.
//
// The shape follows the legacy system EXACTLY - main_mission and
// bonus_challenges per day - because the prompt was lifted from there as
// well. Reading a different shape would mean changing the prompt and its
// reader together, and one of them would fall behind.
func curriculumFrom(payload map[string]any) (*domain.Curriculum, error) {
	c := &domain.Curriculum{
		Title:       stringOf(payload, "program_title"),
		Description: stringOf(payload, "program_description"),
	}

	rawWeeks, ok := payload["weeks"].([]any)
	if !ok || len(rawWeeks) == 0 {
		return nil, domain.ErrEmptyCurriculum
	}

	for _, rw := range rawWeeks {
		weekMap, ok := rw.(map[string]any)
		if !ok {
			return nil, errors.New("a week in the curriculum is not an object")
		}

		week := &domain.Week{
			WeekNumber:  intOf(weekMap, "week_number"),
			Title:       stringOf(weekMap, "title"),
			Description: stringOf(weekMap, "description"),
		}

		for _, rd := range sliceOf(weekMap, "tasks") {
			dayMap, ok := rd.(map[string]any)
			if !ok {
				continue
			}

			date, err := time.Parse(time.DateOnly, stringOf(dayMap, "task_date"))
			if err != nil {
				return nil, fmt.Errorf("week %d has a task with an unreadable date: %w",
					week.WeekNumber, err)
			}

			if main, ok := dayMap["main_mission"].(map[string]any); ok {
				task, err := taskFrom(main, date, domain.TaskMainMission)
				if err != nil {
					return nil, err
				}
				week.Tasks = append(week.Tasks, task)
			}

			for _, rb := range sliceOf(dayMap, "bonus_challenges") {
				bonus, ok := rb.(map[string]any)
				if !ok {
					continue
				}
				task, err := taskFrom(bonus, date, domain.TaskBonusChallenge)
				if err != nil {
					return nil, err
				}
				week.Tasks = append(week.Tasks, task)
			}
		}
		c.Weeks = append(c.Weeks, week)
	}

	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}

func taskFrom(raw map[string]any, date time.Time, kind domain.TaskType) (*domain.Task, error) {
	id, err := domain.NewID()
	if err != nil {
		return nil, err
	}
	return &domain.Task{
		ID:          id,
		TaskDate:    date,
		TaskType:    kind,
		Title:       stringOf(raw, "title"),
		Description: stringOf(raw, "description"),
	}, nil
}

// stringOf reads a string, or an empty string if the field is missing or not
// a string.
//
// A value of the wrong type is treated the same as a missing one: neither
// gives anything, and the curriculum validation that refuses an empty title
// will catch both with the same message.
func stringOf(m map[string]any, key string) string {
	if s, ok := m[key].(string); ok {
		return s
	}
	return ""
}

func sliceOf(m map[string]any, key string) []any {
	if s, ok := m[key].([]any); ok {
		return s
	}
	return nil
}

// intOf reads a number from JSON.
//
// JSON always yields float64, and a direct conversion to int would truncate a
// value like 2.9999999 to 2. Rounding is more honest for a week number.
func intOf(m map[string]any, key string) int {
	switch v := m[key].(type) {
	case float64:
		return int(v + 0.5)
	case int:
		return v
	default:
		return 0
	}
}
