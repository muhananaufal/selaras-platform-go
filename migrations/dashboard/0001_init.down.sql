-- The read-model may be deleted entirely: not a single fact exists only
-- here. All of it can be rebuilt from the start of the topics.
DROP TABLE IF EXISTS projection_state;
DROP TABLE IF EXISTS dashboard_assessments;
DROP TABLE IF EXISTS dashboards;
