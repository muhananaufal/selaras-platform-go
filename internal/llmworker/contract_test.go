package llmworker

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/muhananaufal/selaras-platform-go/internal/llm"
)

// curriculumJSON builds a curriculum answer the way the prompt asks for it:
// the given number of weeks, numbered from one, seven consecutive days each.
// edit changes it before it is encoded, to break exactly one rule.
func curriculumJSON(t *testing.T, weeks int, edit func(*curriculumAnswer)) string {
	t.Helper()
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	answer := curriculumAnswer{ProgramTitle: "Langkah kecil untuk jantung"}
	day := 0
	for w := 1; w <= weeks; w++ {
		week := curriculumWeek{WeekNumber: w}
		for range 7 {
			week.Tasks = append(week.Tasks, curriculumTask{
				TaskDate:    start.AddDate(0, 0, day).Format(time.DateOnly),
				MainMission: &curriculumMission{Title: "Jalan kaki 20 menit"},
			})
			day++
		}
		answer.Weeks = append(answer.Weeks, week)
	}
	if edit != nil {
		edit(&answer)
	}
	raw, err := json.Marshal(answer)
	if err != nil {
		t.Fatalf("encoding the test answer: %v", err)
	}
	return string(raw)
}

func TestACurriculumThatFollowsThePromptIsAccepted(t *testing.T) {
	if err := checkCurriculum(curriculumJSON(t, 4, nil), 4); err != nil {
		t.Fatalf("a curriculum that follows every rule was refused: %v", err)
	}
}

func TestACurriculumThatBreaksThePromptIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name   string
		answer func(t *testing.T) string
	}{
		{"not JSON", func(*testing.T) string { return "Berikut kurikulumnya: ..." }},
		{"no program title", func(t *testing.T) string {
			return curriculumJSON(t, 4, func(a *curriculumAnswer) { a.ProgramTitle = "  " })
		}},
		{"one week short", func(t *testing.T) string { return curriculumJSON(t, 3, nil) }},
		{"one week too many", func(t *testing.T) string { return curriculumJSON(t, 5, nil) }},
		{"a gap in the numbering", func(t *testing.T) string {
			return curriculumJSON(t, 4, func(a *curriculumAnswer) { a.Weeks[3].WeekNumber = 5 })
		}},
		{"a week numbered twice", func(t *testing.T) string {
			return curriculumJSON(t, 4, func(a *curriculumAnswer) { a.Weeks[3].WeekNumber = 3 })
		}},
		{"a week of six days", func(t *testing.T) string {
			return curriculumJSON(t, 4, func(a *curriculumAnswer) { a.Weeks[1].Tasks = a.Weeks[1].Tasks[:6] })
		}},
		{"a skipped day", func(t *testing.T) string {
			return curriculumJSON(t, 4, func(a *curriculumAnswer) { a.Weeks[2].Tasks[3].TaskDate = "2026-10-30" })
		}},
		{"an unreadable date", func(t *testing.T) string {
			return curriculumJSON(t, 4, func(a *curriculumAnswer) { a.Weeks[0].Tasks[0].TaskDate = "1 Oktober 2026" })
		}},
		{"a day without a main mission", func(t *testing.T) string {
			return curriculumJSON(t, 4, func(a *curriculumAnswer) { a.Weeks[0].Tasks[2].MainMission = nil })
		}},
		{"a main mission without a title", func(t *testing.T) string {
			return curriculumJSON(t, 4, func(a *curriculumAnswer) { a.Weeks[0].Tasks[2].MainMission.Title = "" })
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := checkCurriculum(tc.answer(t), 4); !errors.Is(err, ErrContractViolation) {
				t.Fatalf("checkCurriculum returned %v, want ErrContractViolation", err)
			}
		})
	}
}

// The fake provider is what the stack and the end-to-end tests run on; if
// its curriculum broke the contract, every one of them would now fail.
func TestTheFakeProvidersCurriculumFollowsTheContract(t *testing.T) {
	answer := fakeCurriculumForTest(t)
	if err := checkCurriculum(answer, defaultCurriculumWeeks); err != nil {
		t.Fatalf("the fake provider's curriculum breaks the contract: %v", err)
	}
}

// fakeCurriculumForTest asks the fake provider for a curriculum; it chooses
// the shape of its answer from the template name in PromptVersion.
func fakeCurriculumForTest(t *testing.T) string {
	t.Helper()
	answer, err := llm.NewFake().Generate(context.Background(), llm.Request{
		System:        "test",
		Prompt:        "a curriculum, please",
		PromptVersion: "curriculum@1",
		JSON:          true,
	})
	if err != nil {
		t.Fatalf("the fake provider failed: %v", err)
	}
	return answer.Text
}
