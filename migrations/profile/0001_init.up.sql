-- The profile schema. Run by the svc_profile role, which has rights only in
-- this schema (deploy/compose/initdb/01-schemas.sh).

CREATE TABLE user_profiles (
    id         UUID PRIMARY KEY,

    -- Points at identity.users, WITHOUT a foreign key, and that is
    -- deliberate.
    --
    -- A cross-schema foreign key would force the svc_profile role to have
    -- read rights in the identity schema, and with that undo the isolation
    -- the database itself enforces (ADR-006). It would also make the two
    -- services one deployment unit: a migration in either could block writes
    -- in the other.
    --
    -- The price is real and accepted knowingly: orphan rows may exist. What
    -- cleans them up is the account deletion saga (F8), not the database.
    user_id    UUID NOT NULL,

    -- All of them may be NULL. So could the legacy system's, but it then
    -- ran Carbon::parse(null) at the presentation layer, so an empty date
    -- of birth showed as today and the age showed as 0 (finding B6). What
    -- is fixed is not the column - the column was right - but the honesty
    -- of the mapping.
    first_name           TEXT,
    last_name            TEXT,
    date_of_birth        DATE,
    sex                  TEXT,
    country_of_residence TEXT,

    -- language has a default and may not be NULL, the same as the legacy
    -- system. The interface has to pick a language for every user, so "not
    -- determined yet" is not a useful state here.
    language   TEXT        NOT NULL DEFAULT 'id',

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT user_profiles_sex_known CHECK (sex IS NULL OR sex IN ('male', 'female')),
    CONSTRAINT user_profiles_language_known CHECK (language IN ('id', 'en')),

    -- A date of birth in the future cannot be right. The bound is in the
    -- database, not only in request validation, because the risk engine
    -- reads this column and a negative age would flow silently into a
    -- clinical computation.
    CONSTRAINT user_profiles_dob_in_the_past CHECK (date_of_birth IS NULL OR date_of_birth < CURRENT_DATE)
);

-- One profile per user. The legacy system enforced it with a unique on the
-- foreign key column; here the index stands on its own because the foreign
-- key does not exist.
CREATE UNIQUE INDEX user_profiles_user_id_unique ON user_profiles (user_id);
