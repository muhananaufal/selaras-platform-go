DROP INDEX IF EXISTS risk_assessments_personalization_pending;
ALTER TABLE risk_assessments DROP COLUMN IF EXISTS personalization_error;
ALTER TABLE risk_assessments DROP CONSTRAINT IF EXISTS risk_assessments_personalization_status_known;
ALTER TABLE risk_assessments DROP COLUMN IF EXISTS personalization_status;
