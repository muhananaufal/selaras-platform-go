-- Account deletion saga tracking (F8-01).
--
-- Account deletion touches SIX units that each own their own database, and
-- no transaction can span all six. What replaces it is a saga: one request,
-- six confirmations, and a record of who has not answered yet.
--
-- Without that record, a deletion that stops halfway leaves no trace at all.
-- The data stays in units nobody addresses any more, and nobody knows it is
-- there - including the user who asked for it to be deleted.

CREATE TABLE deletion_sagas (
    -- UUIDv7: time-ordered, so the saga that has hung the longest is at the
    -- top of the list without a separate sequence column.
    id UUID PRIMARY KEY,

    user_id UUID NOT NULL,

    -- The profile id comes along because some units store their data under
    -- that key, not under user_id. It is copied NOW, while the profile still
    -- exists: once profile-svc has deleted its row, nothing can translate it
    -- any more.
    user_profile_id UUID,

    -- The state of the saga as a whole.
    --
    -- 'requested' : announced, waiting for confirmations 'completed' : all
    -- six units confirmed, the account has been deleted 'failed' : one unit
    -- or more reported failure
    --
    -- There is no 'compensating'. A deletion CANNOT be undone - data that is
    -- gone does not come back - so the compensation is not restoring the
    -- state, but making the failure VISIBLE and resolvable by a human. See
    -- docs/runbook/account-deletion.md.
    status TEXT NOT NULL DEFAULT 'requested'
        CHECK (status IN ('requested', 'completed', 'failed')),

    requested_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at  TIMESTAMPTZ,

    -- finished_at exists ONLY once the saga has ended, and always exists
    -- once it has. Enforced here so "when did it finish" is never a question
    -- the row itself cannot answer.
    CONSTRAINT deletion_sagas_finished_when_over
        CHECK ((status = 'requested') = (finished_at IS NULL))
);

-- A hanging saga has to be findable without scanning the whole table. A
-- partial index: only unfinished ones are ever asked about this way.
CREATE INDEX deletion_sagas_outstanding
    ON deletion_sagas (requested_at)
    WHERE status = 'requested';

-- One user must not have two sagas running at once.
--
-- Two sagas mean two chains of confirmations for one account, and the second
-- would think it is incomplete because its units have already answered the
-- first. A PARTIAL unique index: once finished, an old saga may sit beside a
-- new one - even though in practice the account no longer exists.
CREATE UNIQUE INDEX deletion_sagas_one_per_user
    ON deletion_sagas (user_id)
    WHERE status = 'requested';

-- Confirmations per unit.
--
-- A separate table, not one boolean column per unit in deletion_sagas:
-- adding a seventh unit later would become an ALTER TABLE migration, and
-- forgetting to add it produces sagas that complete without that unit ever
-- being contacted.
CREATE TABLE deletion_confirmations (
    saga_id UUID NOT NULL REFERENCES deletion_sagas (id) ON DELETE CASCADE,

    -- The name of the confirming unit: 'profile', 'assessment', and so on.
    service TEXT NOT NULL,

    succeeded BOOLEAN NOT NULL,

    -- The reason for the failure, if any. It is what a human reads when
    -- resolving a stuck saga.
    failure_reason TEXT,

    confirmed_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- One unit, one confirmation. This is the idempotency gate: the outbox
    -- relay is at-least-once, so the same confirmation CAN arrive twice, and
    -- the second must not make the saga think seven units answered.
    PRIMARY KEY (saga_id, service),

    -- A failure reason exists ONLY on failure. A success that carries a
    -- reason is a row telling two different stories at once.
    CONSTRAINT deletion_confirmations_reason_when_failed
        CHECK (succeeded OR failure_reason IS NOT NULL)
);
