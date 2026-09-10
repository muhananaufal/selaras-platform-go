-- The personalisation status, as a column of its own.
--
-- Before this, the status was DERIVED from the presence of result_details -
-- which can only tell two states apart: not requested, and done. A client
-- could not tell "in progress" from "never requested", and could not know at
-- all when the job had failed: both looked like a report that is not there.
--
-- This column is what makes F3-12 possible.

ALTER TABLE risk_assessments
    ADD COLUMN personalization_status TEXT NOT NULL DEFAULT 'not_requested';

ALTER TABLE risk_assessments
    ADD CONSTRAINT risk_assessments_personalization_status_known CHECK (
        personalization_status IN ('not_requested', 'pending', 'completed', 'failed')
    );

-- Rows that already have a report get the completed status.
--
-- Without this, old assessments whose report already exists would be
-- not_requested, and the client would offer a "create report" button for a
-- report already on screen.
UPDATE risk_assessments
SET personalization_status = 'completed'
WHERE result_details IS NOT NULL;

-- The reason for the failure, so a failure can be explained instead of merely
-- counted.
ALTER TABLE risk_assessments
    ADD COLUMN personalization_error TEXT;

-- Hanging jobs, for monitoring: a pending that never changes is a symptom
-- of a dead worker or a lost event.
CREATE INDEX risk_assessments_personalization_pending
    ON risk_assessments (updated_at)
    WHERE personalization_status = 'pending';
