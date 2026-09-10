-- The assessment schema. Run by the svc_assessment role, which has rights
-- only in this schema (deploy/compose/initdb/01-schemas.sh).

CREATE TABLE risk_assessments (
    id              UUID PRIMARY KEY,

    -- Points at profile.user_profiles, WITHOUT a cross-schema foreign key -
    -- for the same reason as in the profile schema (ADR-006): a
    -- cross-schema foreign key undoes the isolation the database itself
    -- enforces.
    user_profile_id UUID NOT NULL,

    -- The slug is the public id. It differs from the primary key so the
    -- internal id never appears in a URL, and so it can be looked up without
    -- revealing how many assessments have ever been made.
    slug            TEXT NOT NULL,

    model_used      TEXT NOT NULL,

    -- NUMERIC, not FLOAT as in the legacy system.
    --
    -- This is a number people read about their own heart and compare over
    -- time. Binary floats cannot represent 66.85 exactly, so the stored
    -- value and the computed value can differ in the last digit - and that
    -- difference shows up as a history that changes on its own.
    final_risk_percentage NUMERIC(5,2) NOT NULL,

    -- The full snapshot of the analysis session. inputs is the user's
    -- original answers; generated_values is the clinical values that
    -- actually entered the model, whether typed or estimated.
    --
    -- Both are stored because a risk number without its inputs cannot be
    -- disputed by anyone - including ourselves when investigating a
    -- complaint.
    inputs           JSONB NOT NULL,
    generated_values JSONB NOT NULL,

    -- Filled in later by llm-worker (F3). NULL means not there yet, and that
    -- is a valid state: the assessment is complete without it.
    result_details   JSONB,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- A risk outside 0-100 cannot be right. The bound is in the database
    -- because this column is read by other units - dashboard and coaching -
    -- which will not re-check it.
    CONSTRAINT risk_assessments_percentage_in_range
        CHECK (final_risk_percentage >= 0 AND final_risk_percentage <= 100),

    CONSTRAINT risk_assessments_model_known
        CHECK (model_used IN ('SCORE2', 'SCORE2-OP', 'SCORE2-Diabetes'))
);

CREATE UNIQUE INDEX risk_assessments_slug_unique ON risk_assessments (slug);

-- The history is always read per user and ordered by time. This composite
-- index serves both at once; two separate indexes would force a sort after
-- the read.
CREATE INDEX risk_assessments_by_profile_recent
    ON risk_assessments (user_profile_id, created_at DESC);
