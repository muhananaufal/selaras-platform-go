// Package deletion menjalankan sisi unit dari saga penghapusan akun.
//
// Protokolnya sama di keenam unit: baca permintaan, hapus data milik pengguna
// itu, umumkan konfirmasinya. Yang berbeda hanya penghapusannya sendiri, dan
// itulah satu-satunya yang diserahkan pemanggil.
//
// Ditulis sekali di sini, bukan disalin enam kali, dan alasannya bukan
// keringkasan: enam salinan berarti enam kesempatan salah satunya berhenti
// mengonfirmasi setelah gagal, atau mengonfirmasi berhasil padahal gagal. Yang
// pertama membuat setiap saga menggantung; yang kedua membuat akun dinyatakan
// terhapus sementara datanya masih ada.
package deletion

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	eventsv1 "github.com/muhananaufal/selaras-platform-go/gen/events/v1"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/kafka"
	"github.com/muhananaufal/selaras-platform-go/internal/platform/outbox"
	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

// Eraser menghapus seluruh data satu pengguna di sebuah unit.
//
// Ia menerima Querier, bukan kolam koneksi: penghapusan dan konfirmasinya
// ditulis dalam SATU transaksi. Kalau keduanya bisa terpisah, unit bisa
// mengonfirmasi berhasil sementara penghapusannya batal - dan akun dinyatakan
// terhapus dengan datanya masih utuh.
//
// userProfileID BOLEH kosong: profil yang tidak pernah dibuat adalah keadaan
// yang sah (B7), dan unit yang datanya berkunci profil harus menanganinya
// dengan tidak menghapus apa-apa alih-alih gagal.
//
// Ia HARUS idempoten. Relay outbox bersifat at-least-once, dan permintaan yang
// sama bisa tiba dua kali.
type Eraser func(ctx context.Context, q pg.Querier, userID, userProfileID string) error

// Consumer membaca user.deletion dan menghapus data unit ini.
type Consumer struct {
	client  *kgo.Client
	pool    pg.Beginner
	service string
	erase   Eraser
	log     *slog.Logger
}

// NewConsumer merakit konsumen penghapusan untuk satu unit.
//
// service adalah nama unit ini, dan ia HARUS sama persis dengan salah satu
// nama di identity/domain.DeletionParticipants. Nama yang tidak cocok membuat
// konfirmasinya ditolak identity-svc, dan saga menggantung selamanya menunggu
// unit yang sebenarnya sudah selesai.
func NewConsumer(
	client *kgo.Client, pool pg.Beginner, service string, erase Eraser, log *slog.Logger,
) (*Consumer, error) {
	switch {
	case client == nil:
		return nil, errors.New("nil kafka client")
	case pool == nil:
		return nil, errors.New("nil connection pool")
	case service == "":
		return nil, errors.New("a deletion consumer needs the name of its own unit")
	case erase == nil:
		return nil, errors.New("nil eraser")
	case log == nil:
		return nil, errors.New("nil logger")
	}
	return &Consumer{client: client, pool: pool, service: service, erase: erase, log: log}, nil
}

// Run membaca sampai ctx selesai.
func (c *Consumer) Run(ctx context.Context) error {
	c.log.InfoContext(ctx, "deletion consumer started", "service", c.service)

	for {
		if ctx.Err() != nil {
			c.log.InfoContext(ctx, "deletion consumer stopped", "service", c.service)
			//nolint:nilerr // Penghentian yang diminta bukan kegagalan.
			return nil
		}

		fetches := c.client.PollFetches(ctx)
		if ctx.Err() != nil {
			c.log.InfoContext(ctx, "deletion consumer stopped", "service", c.service)
			//nolint:nilerr // Idem.
			return nil
		}

		if errs := fetches.Errors(); len(errs) > 0 {
			for _, e := range errs {
				c.log.ErrorContext(ctx, "fetching deletion requests failed",
					"service", c.service, "topic", e.Topic, "error", e.Err)
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
			if err := c.handle(ctx, rec); err != nil {
				c.log.ErrorContext(ctx, "handling a deletion request failed",
					"service", c.service, "offset", rec.Offset, "error", err)
				rewinder.Failed(rec)
			}
			handled++
		})

		if handled == 0 {
			continue
		}
		if rewinder.Any() {
			// Konsumen DIMUNDURKAN ke permintaan yang gagal, bukan sekadar
			// tidak dikomit.
			//
			// Tidak mengomit saja TIDAK cukup: franz-go tidak mengirim ulang
			// apa pun di dalam sesi yang sama, jadi batch berikutnya akan
			// datang, berhasil, lalu mengomit seluruh yang sudah dikonsumsi -
			// termasuk permintaan yang gagal tadi. Itu benar-benar terjadi:
			// permintaan penghapusan terlewati diam-diam oleh lima konfirmasi
			// yang lewat di topic yang sama.
			c.log.WarnContext(ctx, "rewinding so failed deletions are redelivered",
				"service", c.service, "handled", handled)
			rewinder.Rewind(c.client)

			// Jeda sebentar supaya kegagalan yang terus berulang tidak menjadi
			// putaran ketat yang membanjiri log dan broker.
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(time.Second):
			}
			continue
		}

		if err := c.client.CommitUncommittedOffsets(ctx); err != nil {
			c.log.ErrorContext(ctx, "committing offsets failed",
				"service", c.service, "error", err)
		}
	}
}

// handle memproses satu permintaan penghapusan.
func (c *Consumer) handle(ctx context.Context, rec *kgo.Record) error {
	var env eventsv1.Envelope
	if err := proto.Unmarshal(rec.Value, &env); err != nil {
		c.log.ErrorContext(ctx, "a deletion request could not be decoded and was skipped",
			"service", c.service, "offset", rec.Offset, "error", err)
		return nil
	}

	req := env.GetUserDeletionRequested()
	if req == nil {
		// Event lain di topic yang sama bukan urusan konsumen ini - konfirmasi
		// dari unit lain lewat di sini juga.
		return nil
	}

	if req.GetSagaId() == "" || req.GetUserId() == "" {
		// Tanpa keduanya, tidak ada yang bisa dihapus dan tidak ada yang bisa
		// dilapori. Ia dicatat, bukan ditebak.
		c.log.ErrorContext(ctx, "a deletion request named no saga or no user",
			"service", c.service, "event_id", env.GetEventId())
		return nil
	}

	// Penghapusan DAN konfirmasinya dalam satu transaksi.
	//
	// Ini inti keandalan saga ini. Konfirmasi yang commit tanpa penghapusannya
	// membuat akun dinyatakan terhapus dengan datanya masih utuh - kebohongan
	// yang tidak akan pernah terlihat, karena tidak ada lagi yang mencarinya.
	err := pg.InTx(ctx, c.pool, func(q pg.Querier) error {
		if err := c.erase(ctx, q, req.GetUserId(), req.GetUserProfileId()); err != nil {
			return err
		}
		return outbox.NewWriter(q).Write(ctx, "user", req.GetUserId(),
			confirmed(req.GetSagaId(), c.service, nil))
	})
	if err == nil {
		c.log.InfoContext(ctx, "deleted this unit's data for a user",
			"service", c.service, "saga_id", req.GetSagaId())
		return nil
	}

	// Kegagalan penghapusan TETAP dikonfirmasi - sebagai kegagalan.
	//
	// Diam adalah pilihan terburuk: saga menggantung tanpa ada yang tahu unit
	// mana yang bermasalah, dan yang tersisa hanya satu baris log yang harus
	// kebetulan dibaca seseorang. Konfirmasi gagal membuat sagalnya berakhir
	// dengan status failed, akunnya ditahan, dan sebabnya tercatat di tempat
	// yang memang dibaca saat menyelesaikannya.
	c.log.ErrorContext(ctx, "could not delete this unit's data; reporting the failure",
		"service", c.service, "saga_id", req.GetSagaId(), "error", err)

	reportErr := pg.InTx(ctx, c.pool, func(q pg.Querier) error {
		return outbox.NewWriter(q).Write(ctx, "user", req.GetUserId(),
			confirmed(req.GetSagaId(), c.service, err))
	})
	if reportErr != nil {
		// Bahkan melaporkan kegagalan pun gagal. Offset ditahan supaya
		// permintaannya datang lagi - itu satu-satunya jalan yang tersisa.
		return fmt.Errorf("deletion failed (%w) and reporting it also failed: %w", err, reportErr)
	}
	return nil
}

// confirmed menyusun konfirmasi, berhasil maupun gagal.
func confirmed(sagaID, service string, cause error) *eventsv1.Envelope {
	payload := &eventsv1.UserDeletionConfirmed{
		SagaId:    sagaID,
		Service:   service,
		Succeeded: cause == nil,
	}
	if cause != nil {
		// Alasannya dipotong: ia masuk ke sebuah kolom yang dibaca manusia,
		// bukan ke tempat penyimpanan galat. Galat pgx yang panjang bisa
		// membawa seluruh pernyataan SQL beserta parameternya - dan parameter
		// di jalur ini adalah id pengguna.
		reason := truncate(cause.Error(), 500)
		payload.FailureReason = &reason
	}

	return &eventsv1.Envelope{
		EventId:       uuid.NewString(),
		OccurredAt:    timestamppb.Now(),
		SchemaVersion: 1,
		Payload:       &eventsv1.Envelope_UserDeletionConfirmed{UserDeletionConfirmed: payload},
	}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
