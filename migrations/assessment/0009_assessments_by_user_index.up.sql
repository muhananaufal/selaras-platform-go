-- squawk-ignore-file require-lock-timeout, require-statement-timeout
-- why: CONCURRENTLY must be the only statement in the file, so it runs outside a transaction and cannot carry SET LOCAL timeouts
-- A clinician's read pages one patient's history by (created_at, id), the same order as 0006.
CREATE INDEX CONCURRENTLY IF NOT EXISTS risk_assessments_by_user_recent ON risk_assessments (user_id, created_at DESC, id DESC);
