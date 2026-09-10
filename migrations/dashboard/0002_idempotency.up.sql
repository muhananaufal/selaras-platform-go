-- The idempotency table for dashboard.
--
-- Its contents are identical in every service and come from one source:
-- internal/platform/idempotency/schema.sql.

-- The idempotency table. One per service schema.
--
-- It answers one question: "has the work with this key been done yet or not?"
-- The answer has to be correct even when two processes ask at the same moment,
-- and that is why it comes from the database's primary key - not from SELECT
-- then INSERT, which has a gap between the two where both read "not yet".

CREATE TABLE processed_messages (
    -- The idempotency key. It is the primary key, and that is not a matter
    -- of style: INSERT ... ON CONFLICT DO NOTHING can only act as a guard
    -- if the database enforces the uniqueness.
    key TEXT PRIMARY KEY,

    -- The scope of its user - the name of a consumer or a use case.
    --
    -- Two different consumers processing the same event must not cancel each
    -- other out: a cache writer that has handled an event does not mean the
    -- notification sender has too. It is folded into the key on the Go side,
    -- and stored separately here so it can be filtered on while investigating.
    scope TEXT NOT NULL,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- The result of the work, if any.
    --
    -- Without it, a repeated request can only be answered "already done" - and
    -- a caller that lost its first answer has no way to obtain the same answer.
    -- With it, it gets exactly the same answer.
    result BYTEA
);

-- This table is DELIBERATELY not partitioned, unlike the outbox.
--
-- Partitioning requires the partition key to be part of every unique
-- constraint, so the primary key would have to become (key, created_at) -
-- and the uniqueness of key alone would stop being enforced across
-- partitions. That is precisely the one thing this table promises. Growth is
-- handled by sweeping old rows (Sweep), not by dropping partitions.
CREATE INDEX processed_messages_by_age ON processed_messages (created_at);
