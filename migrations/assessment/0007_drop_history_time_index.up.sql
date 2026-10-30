-- squawk-ignore-file require-lock-timeout, require-statement-timeout
-- why: DROP INDEX CONCURRENTLY must be the only statement in the file, so it runs outside a transaction and cannot carry SET LOCAL timeouts
-- The contract step of 0006. The old index is a prefix of risk_assessments_by_profile_recent_id, so every query it served - including the created_at-only ordering of the release before this one, still running during a rolling deploy - is served by the new index. Keeping it would only cost a second write on every insert.
DROP INDEX CONCURRENTLY IF EXISTS risk_assessments_by_profile_recent;
