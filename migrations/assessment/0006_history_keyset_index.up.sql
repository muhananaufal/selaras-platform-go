-- squawk-ignore-file require-lock-timeout, require-statement-timeout
-- why: CONCURRENTLY must be the only statement in the file, so it runs outside a transaction and cannot carry SET LOCAL timeouts
-- The history is paged by (created_at, id): created_at alone ties when two assessments share a timestamp, and a page boundary on a tie repeats or skips rows. This is the expand step; 0007 drops the index it replaces.
CREATE INDEX CONCURRENTLY IF NOT EXISTS risk_assessments_by_profile_recent_id ON risk_assessments (user_profile_id, created_at DESC, id DESC);
