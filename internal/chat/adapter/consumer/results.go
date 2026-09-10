// Package consumer membaca balasan model milik percakapan umum.
package consumer

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"

	eventsv1 "github.com/muhananaufal/selaras-platform-go/gen/events/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/chat/app"
	"github.com/muhananaufal/selaras-platform-go/internal/chat/domain"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/kafka"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/telemetry"
)

// Scope is the idempotency scope of this consumer.
const Scope = "chat-results"

// Results reads llm.results and stores the replies.
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
		return nil, errors.New("nil chat service")
	case log == nil:
		return nil, errors.New("nil logger")
	}
	return &Results{client: client, svc: svc, log: log}, nil
}

// isMine says this message belongs to chat.
//
// The llm.results and llm.dlq topics are SHARED by every service that uses
// llm-worker. Without this filter, the chat consumer would try to store a
// coaching curriculum as a conversation reply - fail, hold the offset, and
// clog the queue for everyone. That really happened when coaching was added.
//
// The kind is read from the aggregate_type header the outbox relay fills in,
// without unpacking the content and without guessing from its shape. A
// message without the header is skipped: accepting it means guessing.
func isMine(rec *kgo.Record) bool {
	for _, h := range rec.Headers {
		if h.Key == "aggregate_type" {
			return string(h.Value) == "conversation"
		}
	}
	return false
}

// Run membaca sampai ctx selesai.
func (r *Results) Run(ctx context.Context) error {
	r.log.InfoContext(ctx, "chat result consumer started", "scope", Scope)

	for {
		if ctx.Err() != nil {
			r.log.InfoContext(ctx, "chat result consumer stopped")
			//nolint:nilerr // A requested stop is not a failure.
			return nil
		}

		fetches := r.client.PollFetches(ctx)
		if ctx.Err() != nil {
			r.log.InfoContext(ctx, "chat result consumer stopped")
			//nolint:nilerr // Idem.
			return nil
		}

		if errs := fetches.Errors(); len(errs) > 0 {
			// A topic recreated on the broker (B26): resubscribed here, not through
			// a restart. franz-go deliberately does not recover on its own.
			if recovered := kafka.RecoverRecreatedTopics(r.client, errs); len(recovered) > 0 {
				r.log.WarnContext(ctx, "topics were recreated on the broker; subscribed again", "topics", recovered)
			}
			for _, e := range errs {
				r.log.ErrorContext(ctx, "fetching chat replies failed",
					"topic", e.Topic, "partition", e.Partition, "error", e.Err)
			}
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(time.Second):
			}
			continue
		}

		var handled int
		rewinder := kafka.NewRewinder()

		fetches.EachRecord(func(rec *kgo.Record) {
			if ctx.Err() != nil {
				return
			}
			if err := r.handle(ctx, rec); err != nil {
				r.log.ErrorContext(ctx, "handling a chat reply failed",
					"offset", rec.Offset, "partition", rec.Partition, "error", err)
				rewinder.Failed(rec)
			}
			handled++
		})

		if handled == 0 {
			continue
		}
		if rewinder.Any() {
			r.log.WarnContext(ctx, "holding offsets so failed replies are redelivered",
				"handled", handled)
			// Not committing alone is NOT enough: franz-go redelivers nothing within
			// the same session, so the next batch would arrive, succeed, and commit
			// EVERYTHING consumed so far - including the record that just failed.
			// The consumer is rewound to it instead.
			rewinder.Rewind(r.client)

			select {
			case <-ctx.Done():
				return nil
			case <-time.After(time.Second):
			}
			continue
		}

		if err := r.client.CommitUncommittedOffsets(ctx); err != nil {
			r.log.ErrorContext(ctx, "committing offsets failed", "error", err)
		}
	}
}

// handle processes one reply.
func (r *Results) handle(ctx context.Context, rec *kgo.Record) (err error) {
	if !isMine(rec) {
		return nil
	}

	var env eventsv1.Envelope
	if err := proto.Unmarshal(rec.Value, &env); err != nil {
		r.log.ErrorContext(ctx, "a chat reply could not be decoded and was skipped",
			"offset", rec.Offset, "error", err)
		return nil
	}

	// The consumer span becomes a child of the request that wrote this event
	// (F9-05); an error returned by the handler is recorded on its span.
	ctx, span := telemetry.StartConsumerSpan(ctx, &env, rec)
	defer func() { telemetry.End(span, err) }()

	done := env.GetChatReplyCompleted()
	if done == nil {
		// An LLM failure for a general conversation deliberately writes nothing
		// to the history: D9 in the legacy system answered an AI failure with a
		// friendly message, and that message is produced by the caller - not
		// stored as a model reply the model never said.
		return nil
	}

	// The conversation id comes from the partition key, which the relay fills
	// from the aggregate_id of the outbox row.
	conversationID := string(rec.Key)
	if conversationID == "" {
		r.log.ErrorContext(ctx, "a chat reply carried no conversation key",
			"event_id", env.GetEventId(), "job_id", done.GetJobId())
		return nil
	}

	text, err := replyTextOf(done.GetReplyJson())
	if err != nil {
		// An unreadable reply is NOT stored. Storing it as-is would show raw JSON
		// to the user as an answer.
		r.log.ErrorContext(ctx, "a chat reply was not usable",
			"conversation_id", conversationID, "error", err)
		return nil
	}

	if err = r.svc.StoreReply(ctx, conversationID, text); errors.Is(err, domain.ErrConversationNotFound) {
		// A reply for a conversation that no longer exists. Retrying it will
		// never succeed, and holding the offset for it means this consumer
		// rewinds itself every second, forever - that really happened after a
		// test account was deleted, and the trace is what exposed it.
		r.log.WarnContext(ctx, "a reply arrived for a conversation that no longer exists and was dropped",
			"conversation_id", conversationID, "event_id", env.GetEventId())
		return nil
	}
	return err
}

// replyTextOf takes the reply text from the JSON shape the model returns.
//
// The shape is {"text": ...}, the same as the chat_reply prompt asks for. Chat
// stores plain text, so only that part is used - the suggestions that come
// along have no place yet, and storing them as text would show them as part of
// the answer.
func replyTextOf(raw string) (string, error) {
	var payload struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return "", err
	}
	if payload.Text == "" {
		return "", errors.New("the reply carries no text")
	}
	return payload.Text, nil
}

// Handle processes one record - used by the rule checklist (D9) to inject
// events without a broker. It behaves exactly like the one Run calls.
func (r *Results) Handle(ctx context.Context, rec *kgo.Record) error {
	return r.handle(ctx, rec)
}
