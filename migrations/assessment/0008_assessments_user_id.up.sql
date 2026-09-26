-- The owning user's id on every assessment (ADR-024: the owner is the user,
-- not their profile).
--
-- A clinician's read (ADR-030) names the patient by user id. Going through
-- the profile id meant a lookup in the event-fed profile cache, which can
-- lag behind an assessment computed through the fallback - the read then
-- answered an empty history for a patient who had one (seen in CI). With the
-- user id on the row there is nothing to look up.
--
-- Nullable: rows written before this column get it from
-- cmd/backfill-assessment-owners, in batches outside any migration, where the
-- profile cache knows the mapping (runbook assessment-svc).

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '30s';

ALTER TABLE risk_assessments ADD COLUMN user_id UUID;
