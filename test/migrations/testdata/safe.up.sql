-- The EXPAND step done right: a nullable column, timeouts scoped to this
-- migration's own transaction. Old code keeps working because it never
-- names the column.
SET LOCAL lock_timeout = '2s';
SET LOCAL statement_timeout = '30s';
ALTER TABLE risk_assessments ADD COLUMN IF NOT EXISTS clinician_note TEXT;
