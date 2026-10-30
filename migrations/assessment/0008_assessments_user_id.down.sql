SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '30s';

-- squawk-ignore ban-drop-column
-- why: the down step of 0008 removes only the column 0008 added
ALTER TABLE risk_assessments DROP COLUMN IF EXISTS user_id;
