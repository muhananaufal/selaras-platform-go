package consumer

import (
	"context"
	"log/slog"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/muhananaufal/selaras-platform-go/internal/platform/kafka"
)

// loop reads records until ctx is done and hands each record to handle. A
// failed record holds its offset (Rewinder) so it comes back.
//
// One loop for both coaching consumers: what sets them apart is only the topic
// subscribed to and how one record is handled, not how offsets are read, held,
// and committed - and that is the part that went wrong once (franz-go does not
// redeliver within the same session) and must not go wrong twice.
func loop(
	ctx context.Context, client *kgo.Client, log *slog.Logger, name string,
	handle func(context.Context, *kgo.Record) error,
) error {
	log.InfoContext(ctx, name+" consumer started")

	for {
		if ctx.Err() != nil {
			log.InfoContext(ctx, name+" consumer stopped")
			//nolint:nilerr // A requested stop is not a failure.
			return nil
		}

		fetches := client.PollFetches(ctx)
		if ctx.Err() != nil {
			log.InfoContext(ctx, name+" consumer stopped")
			//nolint:nilerr // Idem.
			return nil
		}

		if errs := fetches.Errors(); len(errs) > 0 {
			// A topic recreated on the broker (B26): resubscribed here, not through
			// a restart. franz-go deliberately does not recover on its own.
			if recovered := kafka.RecoverRecreatedTopics(client, errs); len(recovered) > 0 {
				log.WarnContext(ctx, "topics were recreated on the broker; subscribed again", "topics", recovered)
			}
			for _, e := range errs {
				log.ErrorContext(ctx, "fetching "+name+" records failed",
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
			if err := handle(ctx, rec); err != nil {
				log.ErrorContext(ctx, "handling a "+name+" record failed",
					"offset", rec.Offset, "partition", rec.Partition, "error", err)
				rewinder.Failed(rec)
			}
			handled++
		})

		if handled == 0 {
			continue
		}
		if rewinder.Any() {
			// The offset is held so the failed message comes back after a rebalance
			// or restart. Other messages in the batch are redelivered too; their
			// storage is idempotent, and that is a far cheaper price than a lost
			// result.
			log.WarnContext(ctx, "holding offsets so failed "+name+" records are redelivered",
				"handled", handled)
			// Not committing alone is NOT enough: franz-go redelivers nothing within
			// the same session, so the next batch would arrive, succeed, and commit
			// EVERYTHING consumed so far - including the record that just failed.
			// The consumer is rewound to it instead.
			rewinder.Rewind(client)

			select {
			case <-ctx.Done():
				return nil
			case <-time.After(time.Second):
			}
			continue
		}

		if err := client.CommitUncommittedOffsets(ctx); err != nil {
			log.ErrorContext(ctx, "committing offsets failed", "error", err)
		}
	}
}
