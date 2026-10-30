-- squawk-ignore-file require-lock-timeout, require-statement-timeout
-- why: DROP INDEX CONCURRENTLY must be alone in its file for the same reason as its creation
DROP INDEX CONCURRENTLY IF EXISTS risk_assessments_by_profile_recent_id;
