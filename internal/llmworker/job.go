// Package llmworker mengerjakan permintaan LLM yang datang lewat Kafka.
//
// Ia berdiri di antara tiga hal yang bisa gagal sendiri-sendiri: broker,
// penyedia model, dan basis data. Yang menjaga ketiganya tetap konsisten adalah
// dua mekanisme yang sudah ada - idempotensi di sisi masuk, outbox di sisi
// keluar - dan paket ini yang merangkainya.
package llmworker

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	pg "github.com/muhananaufal/selaras-platform-go/internal/platform/postgres"
)

// Job adalah satu pekerjaan yang tersimpan.
type Job struct {
	ID            uuid.UUID
	CreatedAt     time.Time
	Key           string
	Kind          string
	AggregateType string
	AggregateID   string
	Status        string
	PromptVersion string
	Model         string
	Result        []byte
	Attempts      int
}

// Status yang mungkin. Nilainya ditegakkan batasan CHECK di basis data, dan
// dinamai di sini supaya tidak ada literal yang menyimpang dari sana.
const (
	StatusPending   = "pending"
	StatusRunning   = "running"
	StatusCompleted = "completed"
	StatusFailed    = "failed"
	StatusDead      = "dead"
)

// Jenis pekerjaan.
const (
	KindPersonalization = "personalization"
)

// Repository menyimpan pekerjaan.
//
// Ia menerima Querier di setiap metode, bukan menyimpan kolam koneksi, karena
// setiap penulisan di sini harus bisa ikut ke dalam transaksi yang sama dengan
// klaim idempotensi dan baris outbox-nya.
type Repository struct{}

func NewRepository() *Repository { return &Repository{} }

// Create menyimpan pekerjaan baru berstatus pending.
func (r *Repository) Create(ctx context.Context, q pg.Querier, job *Job) error {
	if job == nil {
		return errors.New("nil job")
	}
	if job.Key == "" {
		return errors.New("a job without an idempotency key cannot be deduplicated")
	}
	if job.Kind == "" || job.AggregateType == "" || job.AggregateID == "" {
		return errors.New("a job needs a kind and an aggregate to answer to")
	}

	if job.ID == uuid.Nil {
		id, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("generating a job id: %w", err)
		}
		job.ID = id
	}
	if job.CreatedAt.IsZero() {
		job.CreatedAt = time.Now()
	}

	const query = `
		INSERT INTO llm_jobs (id, created_at, idempotency_key, kind, aggregate_type, aggregate_id, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`

	if _, err := q.Exec(ctx, query,
		job.ID, job.CreatedAt, job.Key, job.Kind,
		job.AggregateType, job.AggregateID, StatusPending,
	); err != nil {
		return fmt.Errorf("creating the job: %w", err)
	}
	job.Status = StatusPending
	return nil
}

// Complete menyimpan hasilnya.
//
// Ia menolak hasil kosong. Batasan CHECK di basis data juga menolaknya, dan
// keduanya disengaja: yang di sini memberi pesan yang bisa dibaca, yang di sana
// menjamin tidak ada jalur lain yang bisa melewatinya.
func (r *Repository) Complete(
	ctx context.Context, q pg.Querier,
	id uuid.UUID, createdAt time.Time,
	result []byte, promptVersion, model string,
) error {
	if len(result) == 0 {
		return errors.New("a completed job must carry its result")
	}
	if promptVersion == "" || model == "" {
		return errors.New("a completed job must record which prompt and model produced it")
	}

	const query = `
		UPDATE llm_jobs
		SET status = $3, result = $4, prompt_version = $5, model = $6, finished_at = now()
		WHERE id = $1 AND created_at = $2`

	tag, err := q.Exec(ctx, query, id, createdAt, StatusCompleted, result, promptVersion, model)
	if err != nil {
		return fmt.Errorf("completing the job: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("no job %s exists to complete", id)
	}
	return nil
}

// Fail mencatat percobaan yang gagal.
//
// dead menandai pekerjaan yang tidak akan dicoba lagi. Membedakannya dari
// failed penting: yang pertama menunggu manusia, yang kedua menunggu percobaan
// berikutnya, dan menyamakannya berarti salah satunya diperlakukan keliru.
func (r *Repository) Fail(
	ctx context.Context, q pg.Querier,
	id uuid.UUID, createdAt time.Time,
	cause string, dead bool,
) error {
	status := StatusFailed
	if dead {
		status = StatusDead
	}

	const query = `
		UPDATE llm_jobs
		SET status = $3, attempts = attempts + 1, last_error = $4, finished_at = now()
		WHERE id = $1 AND created_at = $2`

	if _, err := q.Exec(ctx, query, id, createdAt, status, truncate(cause, 1000)); err != nil {
		return fmt.Errorf("recording the job failure: %w", err)
	}
	return nil
}

// ByKey mencari pekerjaan lewat kunci idempotensinya.
//
// found bernilai false kalau belum ada. Ia dipakai untuk menjawab permintaan
// ulang dengan hasil yang sama persis, alih-alih hanya "sudah pernah".
func (r *Repository) ByKey(ctx context.Context, q pg.Querier, key string) (*Job, bool, error) {
	const query = `
		SELECT id, created_at, idempotency_key, kind, aggregate_type, aggregate_id,
		       status, coalesce(prompt_version, ''), coalesce(model, ''), result, attempts
		FROM llm_jobs
		WHERE idempotency_key = $1
		ORDER BY created_at DESC
		LIMIT 1`

	var job Job
	err := q.QueryRow(ctx, query, key).Scan(
		&job.ID, &job.CreatedAt, &job.Key, &job.Kind, &job.AggregateType,
		&job.AggregateID, &job.Status, &job.PromptVersion, &job.Model,
		&job.Result, &job.Attempts,
	)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, false, nil
	case err != nil:
		return nil, false, fmt.Errorf("looking up the job: %w", err)
	}
	return &job, true, nil
}

// truncate menjaga pesan galat tetap masuk akal ukurannya.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
