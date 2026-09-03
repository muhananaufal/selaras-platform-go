package llm_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/muhananaufal/selaras-platform-go/internal/llm"
)

func request() llm.Request {
	return llm.Request{
		System:        "you are a careful clinician",
		Prompt:        "explain this score",
		PromptVersion: "personalization@1",
		JSON:          true,
	}
}

// TestTheFakeIsDeterministic adalah alasan penyedia palsu ini ada.
//
// Test yang hasilnya berubah dari satu jalankan ke jalankan berikutnya tidak
// membuktikan apa-apa, dan test idempotensi kehilangan seluruh maknanya kalau
// dua pemanggilan menghasilkan jawaban berbeda.
func TestTheFakeIsDeterministic(t *testing.T) {
	ctx := context.Background()

	first, err := llm.NewFake().Generate(ctx, request())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	second, err := llm.NewFake().Generate(ctx, request())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if first.Text != second.Text {
		t.Fatalf("two identical requests answered differently:\n  %s\n  %s", first.Text, second.Text)
	}

	// Dan prompt yang berbeda menghasilkan jawaban yang berbeda - tanpa itu,
	// determinisme di atas bisa berarti "selalu jawaban yang sama apa pun
	// promptnya", yang tidak membuktikan apa pun.
	other := request()
	other.Prompt = "explain something else"
	third, err := llm.NewFake().Generate(ctx, other)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if third.Text == first.Text {
		t.Fatal("a different prompt produced the same answer")
	}
}

// TestTheFakeAnswersJSON menjaga bentuk jawabannya sama dengan yang sungguhan.
func TestTheFakeAnswersJSON(t *testing.T) {
	got, err := llm.NewFake().Generate(context.Background(), request())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal([]byte(got.Text), &decoded); err != nil {
		t.Fatalf("the answer is not JSON: %v", err)
	}
	if decoded["prompt_version"] != "personalization@1" {
		t.Fatalf("the answer carries prompt_version %v, want personalization@1", decoded["prompt_version"])
	}
	if got.PromptVersion != "personalization@1" {
		t.Fatalf("the response carries prompt version %q", got.PromptVersion)
	}
}

// TestARequestWithoutAPromptVersionIsRefused menjaga F3-09 dari hulunya.
func TestARequestWithoutAPromptVersionIsRefused(t *testing.T) {
	req := request()
	req.PromptVersion = ""

	if _, err := llm.NewFake().Generate(context.Background(), req); err == nil {
		t.Fatal("a request with no prompt version was accepted")
	}
}

// TestAnEmptyPromptIsRefused menjaga permintaan yang terbuang.
func TestAnEmptyPromptIsRefused(t *testing.T) {
	req := request()
	req.Prompt = "   \n\t "

	if _, err := llm.NewFake().Generate(context.Background(), req); err == nil {
		t.Fatal("a blank prompt was accepted")
	}
}

// TestAnOversizedAnswerIsRefused menjaga batas ukuran keluaran.
func TestAnOversizedAnswerIsRefused(t *testing.T) {
	fake := llm.NewFake()
	fake.Answer = strings.Repeat("x", 100)

	req := request()
	req.MaxOutputBytes = 50

	_, err := fake.Generate(context.Background(), req)
	if !errors.Is(err, llm.ErrTruncated) {
		t.Fatalf("Generate returned %v, want ErrTruncated", err)
	}
}

// TestACancelledContextStopsTheCall menjaga worker tetap bisa dimatikan.
func TestACancelledContextStopsTheCall(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := llm.NewFake().Generate(ctx, request()); !errors.Is(err, context.Canceled) {
		t.Fatalf("Generate returned %v, want context.Canceled", err)
	}
}

// TestTheFakeRecordsWhatItWasAsked membuat test lain bisa memeriksa promptnya.
func TestTheFakeRecordsWhatItWasAsked(t *testing.T) {
	fake := llm.NewFake()
	ctx := context.Background()

	for range 3 {
		if _, err := fake.Generate(ctx, request()); err != nil {
			t.Fatalf("Generate: %v", err)
		}
	}

	if fake.CallCount() != 3 {
		t.Fatalf("the fake recorded %d calls, want 3", fake.CallCount())
	}

	calls := fake.Calls()
	calls[0].Prompt = "tampered"
	if fake.Calls()[0].Prompt == "tampered" {
		t.Fatal("Calls handed out the live slice; a caller can rewrite the record")
	}
}

// TestAFailingProviderIsReported menjaga jalur kegagalan bisa diuji.
func TestAFailingProviderIsReported(t *testing.T) {
	fake := llm.NewFake()
	fake.Err = llm.ErrRateLimited

	if _, err := fake.Generate(context.Background(), request()); !errors.Is(err, llm.ErrRateLimited) {
		t.Fatalf("Generate returned %v, want ErrRateLimited", err)
	}
}

// TestATruncatedAnswerIsVisible menjaga laporan setengah jadi tidak tersimpan
// sebagai laporan utuh.
func TestATruncatedAnswerIsVisible(t *testing.T) {
	fake := llm.NewFake()
	fake.FinishReason = "MAX_TOKENS"

	got, err := fake.Generate(context.Background(), request())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !got.Truncated() {
		t.Fatal("an answer that stopped at MAX_TOKENS reports itself as complete")
	}

	fake.FinishReason = "STOP"
	got, err = fake.Generate(context.Background(), request())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got.Truncated() {
		t.Fatal("an answer that stopped normally reports itself as truncated")
	}
}

// TestTheFakeMatchesTheShapeItsPromptAsksFor menutup celah yang ditemukan
// dengan menjalankannya.
//
// Versi pertama penyedia palsu mengembalikan bentuk yang SAMA untuk setiap
// templat. Akibatnya nyata: konsumen coaching membedakan kurikulum dari laporan
// kelulusan lewat ada tidaknya "weeks", dan jawaban palsu yang tidak punya
// keduanya tersimpan sebagai laporan - program tetap menunggu kurikulum
// selamanya.
func TestTheFakeMatchesTheShapeItsPromptAsksFor(t *testing.T) {
	cases := map[string][]string{
		"curriculum@1": {"program_title", "weeks"},
		"graduation@1": {"headline", "completion", "next_step"},
		"chat_reply@1": {"text", "suggestions"},
	}

	for version, keys := range cases {
		req := request()
		req.PromptVersion = version

		got, err := llm.NewFake().Generate(context.Background(), req)
		if err != nil {
			t.Errorf("Generate for %s: %v", version, err)
			continue
		}

		var decoded map[string]any
		if err := json.Unmarshal([]byte(got.Text), &decoded); err != nil {
			t.Errorf("%s did not answer with JSON: %v", version, err)
			continue
		}
		for _, key := range keys {
			if _, ok := decoded[key]; !ok {
				t.Errorf("%s answered without %q: %v", version, key, decoded)
			}
		}
	}

	// Dan kurikulumnya benar-benar berisi pekan bernomor berurutan dengan
	// tanggal yang bisa dibaca - kerangka yang melanggar aturan prompt-nya
	// sendiri tidak membuktikan apa pun.
	req := request()
	req.PromptVersion = "curriculum@1"

	got, err := llm.NewFake().Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	var curriculum struct {
		Weeks []struct {
			WeekNumber int `json:"week_number"`
			Tasks      []struct {
				TaskDate string `json:"task_date"`
			} `json:"tasks"`
		} `json:"weeks"`
	}
	if err := json.Unmarshal([]byte(got.Text), &curriculum); err != nil {
		t.Fatalf("the curriculum is not readable: %v", err)
	}
	if len(curriculum.Weeks) != 4 {
		t.Fatalf("the fake curriculum has %d weeks, want 4", len(curriculum.Weeks))
	}

	var previous time.Time
	for i, w := range curriculum.Weeks {
		if w.WeekNumber != i+1 {
			t.Fatalf("week at position %d is numbered %d", i, w.WeekNumber)
		}
		for _, task := range w.Tasks {
			date, err := time.Parse(time.DateOnly, task.TaskDate)
			if err != nil {
				t.Fatalf("a task date is unreadable: %v", err)
			}
			if !previous.IsZero() && !date.After(previous) {
				t.Fatalf("task dates are not strictly increasing: %s after %s",
					task.TaskDate, previous.Format(time.DateOnly))
			}
			previous = date
		}
	}
}
