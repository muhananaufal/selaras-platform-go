-- squawk-ignore-file require-lock-timeout, require-statement-timeout
-- why: CREATE INDEX CONCURRENTLY must be alone in its file for the same reason as the drop
CREATE INDEX CONCURRENTLY IF NOT EXISTS risk_assessments_by_profile_recent ON risk_assessments (user_profile_id, created_at DESC);
