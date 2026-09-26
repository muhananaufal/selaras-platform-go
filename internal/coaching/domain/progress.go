package domain

import "time"

// ProgramCursor is the position after which the next page of a user's
// programs starts: the (created_at, id) of the last program returned.
//
// Both, not created_at alone: two programs created in the same instant
// would be skipped or repeated at a page boundary.
type ProgramCursor struct {
	CreatedAt time.Time
	ID        ID
}

// WeekProgress is how far a user is through one week: counts only.
//
// It is what a consented clinician sees of a curriculum (ADR-030). The
// tasks' wording is not in it, by design: progress answers "how much is
// done", and the rest is the patient's own.
type WeekProgress struct {
	WeekNumber int
	Total      int
	Completed  int
}
