package llmworker

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrContractViolation marks an answer that does not follow the structure
// its prompt demands. The model's output is untrusted input: an answer that
// breaks the contract is a failed attempt and is tried again, unlike a
// truncated one - the same prompt can well be obeyed the next time.
var ErrContractViolation = errors.New("the answer breaks the prompt's contract")

// curriculumAnswer is the part of the curriculum answer the contract checks,
// decoded into types rather than a map: a field the prompt demands and the
// answer lacks shows up as a zero value or a nil, not as a type assertion
// that happens to fail somewhere downstream.
type curriculumAnswer struct {
	ProgramTitle string           `json:"program_title"`
	Weeks        []curriculumWeek `json:"weeks"`
}

type curriculumWeek struct {
	WeekNumber int              `json:"week_number"`
	Tasks      []curriculumTask `json:"tasks"`
}

type curriculumTask struct {
	TaskDate    string             `json:"task_date"`
	MainMission *curriculumMission `json:"main_mission"`
}

type curriculumMission struct {
	Title string `json:"title"`
}

// daysPerWeek is what curriculum.v1 section 4.4 demands of every week.
const daysPerWeek = 7

// checkCurriculum enforces the rules curriculum.v1 states explicitly and a
// machine can check: exactly `weeks` weeks numbered 1..weeks with no gap and
// no repeat (4.3), seven tasks per week on consecutive dates (4.4), and one
// titled main mission per day (4.5). The number of bonus challenges is not
// checked: the prompt ties it to the difficulty only for the first week, so
// any stricter rule here would be invented, not enforced.
func checkCurriculum(text string, weeks int) error {
	var a curriculumAnswer
	if err := json.Unmarshal([]byte(text), &a); err != nil {
		return fmt.Errorf("%w: not the JSON the prompt asks for: %w", ErrContractViolation, err)
	}
	if strings.TrimSpace(a.ProgramTitle) == "" {
		return fmt.Errorf("%w: no program_title", ErrContractViolation)
	}
	if len(a.Weeks) != weeks {
		return fmt.Errorf("%w: %d weeks, the prompt asks for exactly %d", ErrContractViolation, len(a.Weeks), weeks)
	}

	seen := make(map[int]bool, weeks)
	var previous time.Time
	for i, w := range a.Weeks {
		if w.WeekNumber < 1 || w.WeekNumber > weeks || seen[w.WeekNumber] {
			return fmt.Errorf("%w: week %d is numbered %d; weeks run 1 to %d without a gap or a repeat",
				ErrContractViolation, i+1, w.WeekNumber, weeks)
		}
		seen[w.WeekNumber] = true

		if len(w.Tasks) != daysPerWeek {
			return fmt.Errorf("%w: week %d has %d days, the prompt asks for %d",
				ErrContractViolation, w.WeekNumber, len(w.Tasks), daysPerWeek)
		}
		for d, task := range w.Tasks {
			date, err := time.Parse(time.DateOnly, task.TaskDate)
			if err != nil {
				return fmt.Errorf("%w: week %d day %d has the date %q, not YYYY-MM-DD",
					ErrContractViolation, w.WeekNumber, d+1, task.TaskDate)
			}
			if !previous.IsZero() && !date.Equal(previous.AddDate(0, 0, 1)) {
				return fmt.Errorf("%w: week %d day %d is %s, the day after %s was expected",
					ErrContractViolation, w.WeekNumber, d+1, task.TaskDate, previous.Format(time.DateOnly))
			}
			previous = date
			if task.MainMission == nil || strings.TrimSpace(task.MainMission.Title) == "" {
				return fmt.Errorf("%w: week %d day %d has no titled main mission",
					ErrContractViolation, w.WeekNumber, d+1)
			}
		}
	}
	return nil
}
