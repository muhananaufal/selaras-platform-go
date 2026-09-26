-- EXPAND: a nullable column with no default. Code N never names it, so N
-- keeps serving; the ALTER only needs its brief exclusive lock, and gives up
-- after 2 s instead of queueing every query behind it.
SET LOCAL lock_timeout = '2s';
SET LOCAL statement_timeout = '30s';
ALTER TABLE risk_assessments ADD COLUMN IF NOT EXISTS drill_note TEXT;
