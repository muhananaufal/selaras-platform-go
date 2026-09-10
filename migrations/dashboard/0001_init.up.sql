-- The dashboard read-model.
--
-- This service OWNS no data at all. Every row here is a copy of a fact owned by
-- another unit, reshaped into the form one page reads. All of it may be deleted
-- and rebuilt from the start of the topics; that is precisely what is tested
-- (F7-05).
--
-- What it replaces in the legacy system: one repository calling four other
-- repositories, wrapped in a 15-minute Cache::remember, plus FOUR listeners
-- that manually cleared that cache when something changed. Those four listeners
-- were the symptom - every time a new fact was shown on the dashboard, someone
-- had to remember to add a fifth listener. Forgetting produced a stale
-- dashboard nobody knew about (E17, ADR-009).

CREATE TABLE dashboards (
    -- One row per USER (ADR-024).
    --
    -- The primary key, not an ordinary column with an index: two rows for
    -- one person means two different dashboards, and which one is right
    -- would never be answerable.
    user_id UUID PRIMARY KEY,

    -- Assessments are NOT summarised into columns here.
    --
    -- The first version of this table stored latest_*,
    -- previous_risk_percentage, and total_assessments as columns updated on
    -- every event. That was wrong, and the mistake showed when it ran: two
    -- assessments arriving REVERSED - an ordinary thing, since Kafka
    -- guarantees order per partition key and assessments are keyed on their
    -- assessment id, not their user - left previous_risk_percentage empty
    -- forever, so the dashboard answered "nothing to compare against" for
    -- someone who had analysed twice.
    --
    -- All three are derived from dashboard_assessments, which already holds
    -- everything. Deriving them on READ is correct for any order of arrival,
    -- and makes applying an event a single structurally idempotent INSERT -
    -- not a sequence of CASE expressions that has to be right.

    -- The running coaching program, copied from coaching.program.updated.
    -- NULL means no program - also a valid state.
    program_slug        TEXT,
    program_title       TEXT,
    program_status      TEXT,
    program_current_day INT,
    program_total_days  INT,

    -- The completion percentage is stored SEPARATELY and may be NULL.
    --
    -- Program events are published from two places, and one of them does
    -- not count tasks at all. Storing zero for "not computed yet" would
    -- make the dashboard jump to zero percent every time a program is
    -- paused.
    program_completion_percentage NUMERIC(5,2),

    -- When this projection last moved. Exposed as it is through the API:
    -- the read-model is eventually consistent, and hiding the delay makes
    -- that delay look like a bug.
    projected_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The assessment history and chart points, one row per assessment.
--
-- A separate table, not JSONB inside dashboards: the history grows without
-- bound, and a growing document means the whole row is rewritten every time
-- one assessment is added.
CREATE TABLE dashboard_assessments (
    user_id UUID NOT NULL,

    -- The assessment slug. Together with user_id it is the primary key, and
    -- that is what makes the projection IDEMPOTENT: the same event replayed
    -- twice writes the same row, not a second one (F7-03).
    slug TEXT NOT NULL,

    assessed_at     TIMESTAMPTZ NOT NULL,
    risk_percentage NUMERIC(5,2) NOT NULL,
    risk_category   TEXT NOT NULL,
    model_used      TEXT NOT NULL,

    PRIMARY KEY (user_id, slug)
);

-- The history is read per user, newest first. slug joins as a tie-breaker:
-- two assessments in the same second would be ordered arbitrarily without
-- it, and the second page could repeat rows from the first.
CREATE INDEX dashboard_assessments_by_user
    ON dashboard_assessments (user_id, assessed_at DESC, slug DESC);

-- The consumer position, one row per projector.
--
-- It is NOT a replacement for the Kafka offset - that still belongs to the
-- consumer group. What is stored here is the mark "up to when this
-- projection has been built", used by the rebuild command to declare its
-- result complete, and by the lag measurement to know the last event that
-- came in.
CREATE TABLE projection_state (
    name TEXT PRIMARY KEY,

    -- The OCCURRED_AT time of the last projected event, not its processing
    -- time. The difference between the two is the lag, and that is what
    -- F7-06 measures.
    last_event_at TIMESTAMPTZ,

    events_applied BIGINT NOT NULL DEFAULT 0,
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
