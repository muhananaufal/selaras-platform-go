SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '30s';

DROP TABLE IF EXISTS access_audit;
DROP TABLE IF EXISTS consent_events;
DROP TABLE IF EXISTS clinic_members;
DROP TABLE IF EXISTS clinics;
DROP FUNCTION IF EXISTS refuse_rewrite();
