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
)

// Scope adalah ruang lingkup idempotensi konsumen ini.
const Scope = "chat-results"

// Results membaca llm.results dan menyimpan balasannya.
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

// isMine menyatakan pesan ini milik chat.
//
// Topic llm.results dan llm.dlq dipakai BERSAMA seluruh service yang memakai
// llm-worker. Tanpa penyaringan ini, konsumen chat akan mencoba menyimpan
// kurikulum coaching sebagai balasan percakapan - gagal, menahan offset, dan
// menyumbat antrean untuk semua orang. Itu benar-benar terjadi saat coaching
// ditambahkan.
//
// Jenisnya dibaca dari header aggregate_type yang diisi relay outbox, tanpa
// membongkar isinya dan tanpa menebak dari bentuknya. Pesan tanpa header
// dilewati: menerimanya berarti menebak.
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
			//nolint:nilerr // Penghentian yang diminta bukan kegagalan.
			return nil
		}

		fetches := r.client.PollFetches(ctx)
		if ctx.Err() != nil {
			r.log.InfoContext(ctx, "chat result consumer stopped")
			//nolint:nilerr // Idem.
			return nil
		}

		if errs := fetches.Errors(); len(errs) > 0 {
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

		var handled, failed int
		fetches.EachRecord(func(rec *kgo.Record) {
			if ctx.Err() != nil {
				return
			}
			if err := r.handle(ctx, rec); err != nil {
				r.log.ErrorContext(ctx, "handling a chat reply failed",
					"offset", rec.Offset, "partition", rec.Partition, "error", err)
				failed++
			}
			handled++
		})

		if handled == 0 {
			continue
		}
		if failed > 0 {
			r.log.WarnContext(ctx, "holding offsets so failed replies are redelivered",
				"failed", failed, "handled", handled)
			continue
		}

		if err := r.client.CommitUncommittedOffsets(ctx); err != nil {
			r.log.ErrorContext(ctx, "committing offsets failed", "error", err)
		}
	}
}

// handle memproses satu balasan.
func (r *Results) handle(ctx context.Context, rec *kgo.Record) error {
	if !isMine(rec) {
		return nil
	}

	var env eventsv1.Envelope
	if err := proto.Unmarshal(rec.Value, &env); err != nil {
		r.log.ErrorContext(ctx, "a chat reply could not be decoded and was skipped",
			"offset", rec.Offset, "error", err)
		return nil
	}

	done := env.GetChatReplyCompleted()
	if done == nil {
		// Kegagalan LLM untuk percakapan umum sengaja tidak menulis apa pun ke
		// riwayat: D9 di sistem lama menjawab kegagalan AI dengan pesan ramah,
		// dan pesan itu dibuat pemanggilnya - bukan disimpan sebagai balasan
		// model yang tidak pernah dikatakan model.
		return nil
	}

	// Id percakapan datang dari kunci partisi, yang diisi relay dari
	// aggregate_id baris outbox.
	conversationID := string(rec.Key)
	if conversationID == "" {
		r.log.ErrorContext(ctx, "a chat reply carried no conversation key",
			"event_id", env.GetEventId(), "job_id", done.GetJobId())
		return nil
	}

	text, err := replyTextOf(done.GetReplyJson())
	if err != nil {
		// Balasan yang tidak bisa dibaca TIDAK disimpan. Menyimpannya apa
		// adanya akan menampilkan JSON mentah kepada pengguna sebagai jawaban.
		r.log.ErrorContext(ctx, "a chat reply was not usable",
			"conversation_id", conversationID, "error", err)
		return nil
	}

	return r.svc.StoreReply(ctx, conversationID, text)
}

// replyTextOf mengambil teks balasan dari bentuk JSON yang dikembalikan model.
//
// Bentuknya {"text": ...}, sama dengan yang diminta prompt chat_reply. Chat
// menyimpan teks biasa, jadi hanya bagian itu yang dipakai - saran yang ikut
// datang belum punya tempat, dan menyimpannya sebagai teks akan menampilkannya
// sebagai bagian dari jawaban.
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
