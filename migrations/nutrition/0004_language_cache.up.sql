-- The user's language, copied from profile.updated events.
--
-- The reason is the same as for the profile cache in assessment-svc (ADR-007):
-- producing a menu guide must not call profile-svc on every request. That call
-- adds avoidable failures - a dead profile-svc makes menu guides dead too - for
-- data that changes a few times a year.
--
-- This is a CACHE, not the source of truth. profile-svc remains the owner. What
-- lives here may be stale, may be lost, and may be rebuilt from the start of
-- the topic. What it must NOT become is the only place a fact exists - and the
-- language indeed is not: it always has a usable default.
--
-- Only the language is copied, not the whole profile. Copying more than is used
-- means storing copies nobody ever reads, which then have to be deleted along
-- with the account.

CREATE TABLE user_languages (
    -- Keyed on user_id: that is the identity verified on every request
    -- (ADR-023, ADR-024).
    user_id UUID PRIMARY KEY,

    language TEXT NOT NULL,

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
CREATE INDEX user_languages_by_age ON user_languages (observed_at);
