-- The coaching-svc schema.
--
-- Five tables, the same as the legacy system. What changes is the key type,
-- the program owner, and how the domain rules are enforced - each explained
-- where it applies.

-- Program coaching.
CREATE TABLE coaching_programs (
    -- UUIDv7 in ALL tables, not bigint auto-increment as in the legacy system.
    --
    -- The legacy system used bigint everywhere EXCEPT coaching_tasks.id, which
    -- was a UUID - finding E16, "non-uniform ids". A client that treats one id
    -- as a number and another as a string gets one of them wrong. Uniform UUIDs
    -- close that, and ADR-005 already fixed every public id as a string.
    id UUID PRIMARY KEY,

    -- The owner is user_id, NOT user_profile_id as in the legacy system.
    --
    -- This is the identity verified on every request (ADR-023), and using it
    -- removes a translation step that adds nothing. It also closes half of
    -- finding S9: the legacy system used two identity patterns -
    -- CoachingController compared profile->id while ChatController compared
    -- user_id - and two patterns mean two places to get it wrong.
    user_id UUID NOT NULL,

    -- The public slug. Clients never see the internal id.
    slug TEXT NOT NULL UNIQUE,

    -- The analysis that triggered this program.
    --
    -- There is NO foreign key to the assessment schema (ADR-006, ADR-004
    -- coupling no. 2). A cross-schema FK would bring back the coupling the
    -- service split removed: one migration in assessment would hold a lock in
    -- coaching. Uniqueness is enforced here, and a snapshot of the data is
    -- stored so the program stays explainable even if the assessment is gone.
    risk_assessment_id UUID,

    -- The assessment snapshot at the time the program started.
    --
    -- It is deliberately copied, not referenced. The assessment can change or
    -- be deleted, and a program that explains itself with numbers that have
    -- since changed would confuse whoever reads it a year later.
    assessment_snapshot JSONB,

    title       TEXT NOT NULL,
    description TEXT NOT NULL,

    status TEXT NOT NULL DEFAULT 'active',

    -- Three Indonesian values, kept EXACTLY as they are.
    --
    -- They are not internal terms: clients send them as-is and display them
    -- as-is. Translating them would break existing clients without fixing
    -- anything.
    difficulty TEXT NOT NULL,

    -- start_date and end_date are the ONLY source of truth for the end of the
    -- program (F4-18, finding B5).
    --
    -- The legacy system stored end_date but its completer used created_at + 28
    -- days. Two sources of truth for one fact means one of them is wrong, and
    -- the wrong one is the one nobody looks at.
    start_date DATE NOT NULL,
    end_date   DATE NOT NULL,

    -- The graduation report, filled in later by llm-worker.
    graduation_report JSONB,

    -- The state of graduation report generation, for the same reason as
    -- personalization_status in assessment: a state derived from the presence
    -- of the report cannot express "failed".
    graduation_status TEXT NOT NULL DEFAULT 'not_requested',
    graduation_error  TEXT,

    -- The state of curriculum generation. A freshly created program has no
    -- weeks and no tasks at all - those come from llm-worker (F4-08).
    curriculum_status TEXT NOT NULL DEFAULT 'pending',
    curriculum_error  TEXT,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT coaching_programs_status_known CHECK (
        status IN ('active', 'paused', 'completed')
    ),
    CONSTRAINT coaching_programs_difficulty_known CHECK (
        difficulty IN ('Santai & Bertahap', 'Standar & Konsisten', 'Intensif & Menantang')
    ),
    CONSTRAINT coaching_programs_graduation_status_known CHECK (
        graduation_status IN ('not_requested', 'pending', 'completed', 'failed')
    ),
    CONSTRAINT coaching_programs_curriculum_status_known CHECK (
        curriculum_status IN ('pending', 'completed', 'failed')
    ),

    -- end_date is ALWAYS after start_date. A program that ends before it
    -- starts is not a program; allowing it means every remaining-days
    -- computation yields a negative number that every reader has to handle.
    CONSTRAINT coaching_programs_ends_after_it_starts CHECK (end_date > start_date)
);

-- D2: one ACTIVE program per user.
--
-- Enforced by the database, not only by code. The legacy system checked and
-- then cancelled the old one, and two concurrent requests both saw "nothing
-- active" and both created one - leaving two active programs that should not
-- exist.
CREATE UNIQUE INDEX coaching_programs_one_active_per_user
    ON coaching_programs (user_id)
    WHERE status = 'active';

-- D3: one program per analysis result.
--
-- Partial because risk_assessment_id may be NULL: a program can be started
-- without an assessment. An ordinary unique index would treat every NULL as
-- distinct in PostgreSQL, so it would still work - but declaring it partial
-- makes the intent readable instead of relying on NULL behaviour not everyone
-- remembers.
CREATE UNIQUE INDEX coaching_programs_one_per_assessment
    ON coaching_programs (risk_assessment_id)
    WHERE risk_assessment_id IS NOT NULL;

CREATE INDEX coaching_programs_by_user ON coaching_programs (user_id, created_at DESC);

-- Pekan dalam program.
CREATE TABLE coaching_weeks (
    id UUID PRIMARY KEY,

    -- FKs INSIDE this schema are kept: they do not cross a service boundary,
    -- so no coupling is brought back. What ADR-006 forbids is CROSS-schema
    -- FKs.
    coaching_program_id UUID NOT NULL
        REFERENCES coaching_programs (id) ON DELETE CASCADE,

    -- Positive, and enforced here. Week 0 or a negative week would order the
    -- curriculum in a way that makes no sense.
    week_number SMALLINT NOT NULL,

    title       TEXT NOT NULL,
    description TEXT NOT NULL,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT coaching_weeks_number_is_positive CHECK (week_number > 0),

    -- The same week number must not exist twice in one program. Without
    -- this, a curriculum consumer that ran twice would duplicate the entire
    -- content.
    CONSTRAINT coaching_weeks_unique_per_program UNIQUE (coaching_program_id, week_number)
);

-- Tugas harian.
CREATE TABLE coaching_tasks (
    -- UUID, as in the legacy system - the only table that already used one.
    id UUID PRIMARY KEY,

    coaching_week_id UUID NOT NULL
        REFERENCES coaching_weeks (id) ON DELETE CASCADE,

    task_date DATE NOT NULL,
    task_type TEXT NOT NULL,

    title       TEXT NOT NULL,
    description TEXT NOT NULL,

    is_completed BOOLEAN NOT NULL DEFAULT FALSE,

    -- When the task was completed. NULL means not yet.
    --
    -- It does not duplicate is_completed: one answers "done?", the other
    -- "when?" - and the second is what the graduation report needs.
    completed_at TIMESTAMPTZ,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT coaching_tasks_type_known CHECK (
        task_type IN ('main_mission', 'bonus_challenge')
    ),

    -- Both completion columns have to agree. A row that is is_completed but
    -- has a NULL completed_at would make the graduation report count a task
    -- that has no date.
    CONSTRAINT coaching_tasks_completion_is_consistent CHECK (
        (is_completed AND completed_at IS NOT NULL)
        OR (NOT is_completed AND completed_at IS NULL)
    )
);

CREATE INDEX coaching_tasks_by_week ON coaching_tasks (coaching_week_id, task_date);

-- Thread diskusi.
CREATE TABLE coaching_threads (
    id UUID PRIMARY KEY,

    coaching_program_id UUID NOT NULL
        REFERENCES coaching_programs (id) ON DELETE CASCADE,

    slug TEXT NOT NULL UNIQUE,

    -- The default is kept from the legacy system. D12 explains when it is
    -- replaced by a title derived from the first message.
    title TEXT NOT NULL DEFAULT 'Diskusi Program',

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX coaching_threads_by_program ON coaching_threads (coaching_program_id, created_at DESC);

-- Pesan dalam thread.
CREATE TABLE coaching_messages (
    id UUID PRIMARY KEY,

    coaching_thread_id UUID NOT NULL
        REFERENCES coaching_threads (id) ON DELETE CASCADE,

    -- Only two roles, enforced by the database. A third role that slipped in
    -- would be sent to the LLM provider as a role it does not recognise.
    role TEXT NOT NULL,

    content JSONB NOT NULL,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT coaching_messages_role_known CHECK (role IN ('user', 'model'))
);

-- A conversation is read in time order, oldest first.
CREATE INDEX coaching_messages_by_thread ON coaching_messages (coaching_thread_id, created_at);
