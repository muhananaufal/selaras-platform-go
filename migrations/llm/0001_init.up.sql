-- The llm-worker schema.
--
-- This worker keeps its own state rather than riding on the assessment
-- schema. Per-schema separation is what keeps a mistake in one service from
-- touching another service's data (ADR-006), and riding along would throw
-- that separation away precisely where the most calls to outside parties are
-- made.

-- Pekerjaan LLM beserta hasilnya.
CREATE TABLE llm_jobs (
    -- UUIDv7: time-ordered, so jobs can be read in order of arrival without
    -- a separate sequence column.
    id UUID NOT NULL,

    -- created_at is part of the primary key because it is the partition key
    -- (F3-17).
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- The idempotency key that came with the request. It is what makes a
    -- message arriving twice produce one job.
    idempotency_key TEXT NOT NULL,

    -- The job kind: personalization, curriculum, chat_reply, meal_guide.
    kind TEXT NOT NULL,

    -- The requesting aggregate, so the result can be returned to the right
    -- place without guessing.
    aggregate_type TEXT NOT NULL,
    aggregate_id   TEXT NOT NULL,

    status TEXT NOT NULL DEFAULT 'pending',

    -- The prompt version that produced the result (F3-09).
    --
    -- Without it, an old report that looks strange cannot be explained: there
    -- is no way to know whether the model answered like that or the template
    -- has been replaced since.
    prompt_version TEXT,

    -- The name of the model that actually answered, as reported by the
    -- provider - not the one requested. The two can differ when the provider
    -- reroutes a request, and what has to be recorded is the one that
    -- answered.
    model TEXT,

    -- The result. BYTEA, not JSONB: what is stored is exactly what was sent
    -- back, without a re-encoding that could change its shape.
    result BYTEA,

    attempts INT NOT NULL DEFAULT 0,
    last_error TEXT,

    started_at  TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,

    PRIMARY KEY (id, created_at),

    CONSTRAINT llm_jobs_status_known CHECK (
        status IN ('pending', 'running', 'completed', 'failed', 'dead')
    ),

    -- A finished job has to carry its result and its provenance. Without this
    -- constraint, a row with status completed and a NULL result would look
    -- like a success until someone read it.
    CONSTRAINT llm_jobs_completed_has_a_result CHECK (
        status <> 'completed'
        OR (result IS NOT NULL AND prompt_version IS NOT NULL AND model IS NOT NULL)
    )
) PARTITION BY RANGE (created_at);

-- Partitioning is set up when the table is created (F3-17), the same as the
-- outbox. Turning a populated table into a partitioned one means copying its
-- entire contents while holding a lock.
--
-- The default partition catches anything outside the ranges already created,
-- so an INSERT for a month not yet provisioned does not fail and drag its
-- transaction down with it.
CREATE TABLE llm_jobs_default PARTITION OF llm_jobs DEFAULT;

-- One job per idempotency key.
--
-- Uniqueness CANNOT be enforced on a partitioned table without including the
-- partition key, so the guard is not this index but processed_messages, which
-- is deliberately not partitioned. The index here is for lookups, not for a
-- guarantee - and this comment exists so nobody assumes otherwise.
CREATE INDEX llm_jobs_by_idempotency_key ON llm_jobs (idempotency_key);

-- Unfinished jobs, for monitoring and recovery.
CREATE INDEX llm_jobs_unfinished ON llm_jobs (created_at)
    WHERE status IN ('pending', 'running');

CREATE INDEX llm_jobs_by_aggregate ON llm_jobs (aggregate_type, aggregate_id, created_at DESC);
