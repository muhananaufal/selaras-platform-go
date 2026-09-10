-- Soft references to analysis results (F4-06; ADR-004 coupling no. 2).
--
-- assessment-svc announces assessment.completed; coaching stores the snapshot
-- it needs HERE, and StartProgram then resolves the slug from this table - not
-- by calling assessment-svc synchronously, and not through the cross-schema FK
-- ADR-006 forbids. Before this table existed, the slug a client sent was never
-- translated into anything: every program was stored without its analysis
-- source, and D3 (one program per analysis) could never be enforced. The D3
-- acceptance test is what found it.
CREATE TABLE coaching_assessments (
    -- The analysis id owned by assessment-svc. The primary key, so an event
    -- arriving twice (the at-least-once relay) stops at ON CONFLICT DO
    -- NOTHING.
    id UUID PRIMARY KEY,

    user_id UUID NOT NULL,

    -- The public slug the client sends when starting a program.
    slug TEXT NOT NULL UNIQUE,

    -- The snapshot copied into coaching_programs.assessment_snapshot when a
    -- program starts: slug, risk_percentage, risk_category, model_used.
    snapshot JSONB NOT NULL,

    completed_at TIMESTAMPTZ NOT NULL,
    recorded_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX coaching_assessments_by_user ON coaching_assessments (user_id);
