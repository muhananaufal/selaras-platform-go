-- CONTRACT: once no code reads the column, it is dropped.
SET LOCAL lock_timeout = '2s';
SET LOCAL statement_timeout = '30s';
-- why: contract step of this drill's own expand; no code ever read drill_note
-- squawk-ignore ban-drop-column
ALTER TABLE risk_assessments DROP COLUMN IF EXISTS drill_note;
