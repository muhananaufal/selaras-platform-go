-- A profile snapshot copied from profile.updated events (F2-16).
--
-- The reason is ADR-007: the risk computation must not call another service on
-- every request. That call adds avoidable failures - a slow profile-svc makes
-- assessments slow, a dead profile-svc makes assessments dead - for data that
-- changes a few times a year.
--
-- This is a CACHE, not the source of truth. profile-svc remains the owner. What
-- lives here may be stale, may be lost, and may be rebuilt from the start of
-- the topic.

CREATE TABLE profile_snapshots (
    -- Keyed on user_id, NOT user_profile_id.
    --
    -- user_id is the identity verified on every request (ADR-023). A cache
    -- keyed on the profile id would force assessment to call profile-svc
    -- first to translate it - and that call is precisely what this cache is
    -- meant to remove.
    user_id UUID PRIMARY KEY,

    user_profile_id UUID NOT NULL,

    -- All three may be NULL: a profile not yet filled in is a valid state
    -- (ADR-002 rule 2). NULL means "not filled in yet", as opposed to an
    -- empty value that means "known to be empty".
    date_of_birth        DATE,
    sex                  TEXT,
    country_of_residence TEXT,

    language TEXT NOT NULL DEFAULT 'id',

    -- The time of the event that produced this row, NOT the time it was
    -- written.
    --
    -- It is what keeps a late-arriving event from overwriting a newer one:
    -- Kafka guarantees order per partition, but partitions can change and
    -- consumers can be replayed.
    observed_at TIMESTAMPTZ NOT NULL,

    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- A stale cache has to be findable without scanning the whole table.
CREATE INDEX profile_snapshots_by_age ON profile_snapshots (observed_at);
