-- Three things that take production down while code N is still serving:
-- a column N still reads is dropped, a NOT NULL column without a default
-- rewrites and rejects N's inserts, and no timeout lets the ALTER queue
-- every query behind its lock.
ALTER TABLE risk_assessments DROP COLUMN model_used;
ALTER TABLE risk_assessments ADD COLUMN score BIGINT NOT NULL;
