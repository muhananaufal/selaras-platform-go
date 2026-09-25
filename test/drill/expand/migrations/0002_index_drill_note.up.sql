-- squawk-ignore-file require-lock-timeout, require-statement-timeout
-- why: CONCURRENTLY must be the only statement in the file, so it runs outside a transaction and cannot carry SET LOCAL timeouts
CREATE INDEX CONCURRENTLY IF NOT EXISTS risk_assessments_drill_note ON risk_assessments (drill_note);
