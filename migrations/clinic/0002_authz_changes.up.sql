-- The OpenFGA projection's outbox (ADR-030).
--
-- Every change to consent or membership writes the tuple changes it implies
-- here, in the same transaction. The projector applies them to OpenFGA in id
-- order and marks them applied, so a change committed here always reaches
-- OpenFGA and one rolled back never does - two writes to two systems cannot
-- promise that on their own.
--
-- It is not the Kafka outbox: nothing outside clinic-svc reads these rows,
-- and a broker between the ledger and the tuples would only add a hop to
-- the time a revoked consent keeps working.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '30s';

CREATE TABLE authz_changes (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    op          TEXT NOT NULL CHECK (op IN ('write', 'delete')),
    tuple_user  TEXT NOT NULL,
    relation    TEXT NOT NULL,
    object      TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    applied_at  TIMESTAMPTZ,
    attempts    BIGINT NOT NULL DEFAULT 0,
    last_error  TEXT
);

-- The projector's query: the oldest changes not yet applied.
CREATE INDEX authz_changes_pending ON authz_changes (id) WHERE applied_at IS NULL;

GRANT SELECT, INSERT ON authz_changes TO svc_clinic;
GRANT UPDATE (applied_at, attempts, last_error) ON authz_changes TO svc_clinic;
