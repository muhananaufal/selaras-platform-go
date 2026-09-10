-- The outbox table. One per service schema; the contents are identical.
--
-- It is embedded and used by the migration generator so there are not eight
-- copies slowly drifting apart. One copy that differs means one service
-- whose events behave differently, and the difference only shows once
-- something goes missing.

CREATE TABLE outbox (
    -- UUIDv7: time-ordered, so the relay reads rows in the same order they
    -- were written without a separate sequence column.
    id UUID NOT NULL,

    -- created_at is part of the primary key because it is the partition
    -- key. PostgreSQL requires it: without that, the primary key cannot be
    -- enforced across partitions.
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- The aggregate that changed. Used as the Kafka partition key, so every
    -- event of one aggregate lands on the same partition and its ordering is
    -- preserved - Kafka does not guarantee global ordering, it guarantees
    -- ordering per key.
    aggregate_type TEXT NOT NULL,
    aggregate_id   TEXT NOT NULL,

    event_type TEXT NOT NULL,

    -- The serialised protobuf envelope. BYTEA, not JSONB: what is stored is
    -- the exact form that will be sent, so there is no re-encoding between
    -- what is stored and what is delivered.
    payload BYTEA NOT NULL,

    -- NULL means not published yet. Published rows are kept for a while for
    -- investigation, then swept away with their partition.
    published_at TIMESTAMPTZ,

    -- How many delivery attempts were made. It exists so that rows that
    -- always fail can be found, instead of silently clogging the queue.
    attempts INT NOT NULL DEFAULT 0,

    -- The last error. Without it, the only way to learn why an event was not
    -- delivered is to be reading the log at the right moment.
    last_error TEXT,

    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);

-- Partitioning is set up NOW, not later (F3-17).
--
-- Turning a populated table into a partitioned one means copying its entire
-- contents while holding a lock - an operation that, on a table growing
-- monotonically like this one, would take an unacceptable amount of time.
--
-- The default partition catches anything that falls outside the ranges
-- already created. Without it, an INSERT for a month whose partition does not
-- exist yet would FAIL - and that failure would take the business transaction
-- down with it.
CREATE TABLE outbox_default PARTITION OF outbox DEFAULT;

-- The relay only reads what has not been published, in time order. The
-- partial index holds only those rows, so it stays small even as the table
-- grows.
CREATE INDEX outbox_unpublished ON outbox (created_at) WHERE published_at IS NULL;
