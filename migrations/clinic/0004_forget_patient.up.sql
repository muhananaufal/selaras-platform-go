-- Erasure on account deletion (ADR-030).
--
-- The consent ledger and the access audit are append-only: a trigger refuses
-- DELETE to every role but clinic_owner. The one delete that must exist is
-- erasure when an account is deleted, and it runs through this function,
-- owned by clinic_owner and marked SECURITY DEFINER, so the runtime role can
-- ask for exactly this and nothing else.
--
-- What goes is the rows where the deleted user is the PATIENT: their consents
-- and the record of who read their data. Rows where they were the CLINICIAN
-- are other patients' history - who could read them, and who did - and stay,
-- the id now pointing at an account that no longer exists.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '30s';

CREATE FUNCTION forget_patient(target UUID) RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
-- A SECURITY DEFINER function runs as its owner; a search_path the caller
-- could influence would let it resolve names to the caller's objects.
SET search_path = clinic, pg_temp
AS $$
BEGIN
    DELETE FROM consent_events WHERE patient_user_id = target;
    DELETE FROM access_audit WHERE patient_user_id = target;
END;
$$;

REVOKE ALL ON FUNCTION forget_patient(UUID) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION forget_patient(UUID) TO svc_clinic;
