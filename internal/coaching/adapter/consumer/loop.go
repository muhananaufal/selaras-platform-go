package consumer

import (
	"context"
	"log/slog"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/muhananaufal/selaras-platform-go/internal/platform/kafka"
)

// loop membaca record sampai ctx selesai dan menyerahkan tiap record ke
// handle. Record yang gagal menahan offsetnya (Rewinder) supaya datang lagi.
//
// Satu putaran untuk kedua konsumen coaching: yang membedakan keduanya hanya
// topic yang dilanggani dan cara menangani satu record, bukan cara membaca,
// menahan, dan mengomit offset - dan itulah bagian yang pernah salah satu kali
// (franz-go tidak mengirim ulang di dalam sesi yang sama) dan tidak boleh
// salah dua kali.
func loop(
	ctx context.Context, client *kgo.Client, log *slog.Logger, name string,
	handle func(context.Context, *kgo.Record) error,
) error {
	log.InfoContext(ctx, name+" consumer started")

	for {
		if ctx.Err() != nil {
			log.InfoContext(ctx, name+" consumer stopped")
			//nolint:nilerr // Penghentian yang diminta bukan kegagalan.
			return nil
		}

		fetches := client.PollFetches(ctx)
		if ctx.Err() != nil {
			log.InfoContext(ctx, name+" consumer stopped")
			//nolint:nilerr // Idem.
			return nil
		}

		if errs := fetches.Errors(); len(errs) > 0 {
			// Topic yang dibuat ulang di broker (B26): dilanggani ulang di sini,
			// bukan lewat restart. franz-go sengaja tidak pulih sendiri.
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
			// Offset ditahan supaya pesan yang gagal datang lagi setelah
			// rebalance atau restart. Pesan lain di batch ikut terkirim ulang;
			// penyimpanannya idempoten, dan itu harga yang jauh lebih murah
			// daripada hasil yang hilang.
			log.WarnContext(ctx, "holding offsets so failed "+name+" records are redelivered",
				"handled", handled)
			// Tidak mengomit saja TIDAK cukup: franz-go tidak mengirim ulang
			// apa pun di dalam sesi yang sama, jadi batch berikutnya akan
			// datang, berhasil, lalu mengomit SELURUH yang sudah dikonsumsi -
			// termasuk record yang gagal tadi. Konsumen dimundurkan ke sana.
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
