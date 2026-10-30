-- The clinic schema (ADR-030): clinics, their members, the consent ledger,
-- and the access audit.
--
-- This file runs as clinic_owner, the schema's migration role. svc_clinic,
-- the unit's runtime role, owns nothing here and gets exactly the privileges
-- granted at the end: a table's owner can switch its triggers and row level
-- security off with DDL, so both are only as strong as a runtime role that
-- does not own the table.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '30s';

CREATE TABLE clinics (
    id         UUID PRIMARY KEY,
    name       TEXT NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 200),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Membership is state, not history: a clinician who leaves is deleted here,
-- and the OpenFGA model then denies every read that needed the membership.
-- One row per role, since a user may hold more than one (an admin who also
-- treats patients).
CREATE TABLE clinic_members (
    clinic_id UUID NOT NULL REFERENCES clinics (id) ON DELETE CASCADE,
    user_id   UUID NOT NULL,
    role      TEXT NOT NULL CHECK (role IN ('owner', 'admin', 'clinician')),
    added_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (clinic_id, user_id, role)
);

-- The consent ledger: a patient granting or revoking one clinician's access.
-- Append-only - a revocation is a new row, never an edit - so the history of
-- who could read what, and since when, cannot be rewritten.
CREATE TABLE consent_events (
    id                BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    patient_user_id   UUID NOT NULL,
    clinician_user_id UUID NOT NULL,
    clinic_id         UUID NOT NULL REFERENCES clinics (id),
    kind              TEXT NOT NULL CHECK (kind IN ('granted', 'revoked')),
    recorded_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (patient_user_id <> clinician_user_id)
);

-- The current state of a pair is its latest event.
CREATE INDEX consent_events_by_pair ON consent_events (patient_user_id, clinician_user_id, id DESC);
CREATE INDEX consent_events_by_clinician ON consent_events (clinician_user_id, id DESC);

-- Every read a clinician makes of a patient's data, recorded by the service
-- that served it (ADR-030 rule 5) and ingested here. Append-only.
CREATE TABLE access_audit (
    id                BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    -- The owning service's event id: ingestion is at-least-once, and a
    -- repeated event must not become a second access.
    event_id          UUID NOT NULL UNIQUE,
    clinician_user_id UUID NOT NULL,
    patient_user_id   UUID NOT NULL,
    resource          TEXT NOT NULL CHECK (resource IN ('risk_assessments', 'coaching_progress')),
    accessed_at       TIMESTAMPTZ NOT NULL,
    recorded_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX access_audit_by_patient ON access_audit (patient_user_id, accessed_at DESC);

-- Append-only, enforced by the database for every role but the owner:
-- UPDATE is refused to everyone, DELETE to everyone but clinic_owner. The
-- one DELETE that exists is erasure when an account is deleted, and it runs
-- through a SECURITY DEFINER function owned by clinic_owner, so the runtime
-- role can ask for it but cannot perform an arbitrary one.
CREATE FUNCTION refuse_rewrite() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' AND current_user = 'clinic_owner' THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION '% is append-only: % refused', TG_TABLE_NAME, TG_OP
        USING ERRCODE = 'insufficient_privilege';
END;
$$;

CREATE TRIGGER consent_events_append_only
    BEFORE UPDATE OR DELETE ON consent_events
    FOR EACH ROW EXECUTE FUNCTION refuse_rewrite();

CREATE TRIGGER access_audit_append_only
    BEFORE UPDATE OR DELETE ON access_audit
    FOR EACH ROW EXECUTE FUNCTION refuse_rewrite();

-- Row level security binds svc_clinic. Every transaction states whose data
-- it acts for (SET LOCAL app.user_id); without it, no row is visible. A
-- transaction-scoped setting is what makes this safe behind PgBouncer in
-- transaction mode: the next transaction on the same server connection
-- starts without it.
--
-- app.scope names the two jobs that act for nobody in particular: the
-- OpenFGA projection reads every consent event, and audit ingestion writes
-- rows for any pair. The runtime role sets both itself, so RLS here guards
-- against a query that forgets its WHERE, not against a compromised unit.
ALTER TABLE consent_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE access_audit ENABLE ROW LEVEL SECURITY;

CREATE POLICY consent_events_read ON consent_events FOR SELECT TO svc_clinic
    USING (
        patient_user_id = nullif(current_setting('app.user_id', true), '')::uuid
        OR clinician_user_id = nullif(current_setting('app.user_id', true), '')::uuid
        OR current_setting('app.scope', true) = 'projection'
    );

-- Only the patient grants or revokes their own consent.
CREATE POLICY consent_events_write ON consent_events FOR INSERT TO svc_clinic
    WITH CHECK (patient_user_id = nullif(current_setting('app.user_id', true), '')::uuid);

CREATE POLICY access_audit_read ON access_audit FOR SELECT TO svc_clinic
    USING (
        patient_user_id = nullif(current_setting('app.user_id', true), '')::uuid
        OR clinician_user_id = nullif(current_setting('app.user_id', true), '')::uuid
    );

CREATE POLICY access_audit_write ON access_audit FOR INSERT TO svc_clinic
    WITH CHECK (current_setting('app.scope', true) = 'ingest');

GRANT SELECT, INSERT ON clinics TO svc_clinic;
GRANT SELECT, INSERT, DELETE ON clinic_members TO svc_clinic;
GRANT SELECT, INSERT ON consent_events, access_audit TO svc_clinic;
