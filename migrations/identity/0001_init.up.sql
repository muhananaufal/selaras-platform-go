-- The identity schema. Run by the svc_identity role, which has rights only
-- in this schema (deploy/compose/initdb/01-schemas.sh).

CREATE TABLE users (
    id                UUID        PRIMARY KEY,
    email             TEXT        NOT NULL,
    role              TEXT        NOT NULL DEFAULT 'user',
    -- May be NULL, and that is deliberate. The legacy system used a NOT
    -- NULL column and filled it with the hash of a random string for Google
    -- users; that hash claimed there was a usable credential when there was
    -- not.
    password_hash     TEXT,
    google_id         TEXT,
    email_verified_at TIMESTAMPTZ,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at        TIMESTAMPTZ,

    -- Token revocation takes the form of a per-user generation counter, not
    -- a list of revoked tokens. ADR-012 requires revoking ALL of a user's
    -- tokens at once (D1: one session per user), and a per-token list turns
    -- that into as many deletions as there are tokens. Bumping one number
    -- invalidates all of them in a single write.
    --
    -- A counter, not a timestamp: iat on a JWT has second precision, so a
    -- token issued in the same second as the revocation would be ambiguous.
    -- An integer has no clock to be wrong about.
    token_generation  BIGINT      NOT NULL DEFAULT 1,

    CONSTRAINT users_role_known CHECK (role IN ('user', 'admin')),
    -- An account MUST have at least one way to sign in. Without this
    -- constraint, a row with no password and no google_id is an account
    -- nobody can enter and nobody can recover.
    CONSTRAINT users_has_a_credential CHECK (
        password_hash IS NOT NULL OR google_id IS NOT NULL
    )
);

-- Uniqueness applies only to live rows. An ordinary unique index would burn
-- an email address forever once its account was soft-deleted, because the
-- dead row would still occupy the address.
CREATE UNIQUE INDEX users_email_unique_alive
    ON users (email) WHERE deleted_at IS NULL;

CREATE UNIQUE INDEX users_google_id_unique_alive
    ON users (google_id) WHERE google_id IS NOT NULL AND deleted_at IS NULL;

-- Password reset tokens. Closes S1: the legacy system issued tokens that
-- were never actually checked before the password changed.
CREATE TABLE password_reset_tokens (
    -- The hash is stored, not the token. A leaked database must not
    -- directly mean a leaked ability to take over accounts.
    token_hash BYTEA       PRIMARY KEY,
    user_id    UUID        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    expires_at TIMESTAMPTZ NOT NULL,
    used_at    TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX password_reset_tokens_user_id ON password_reset_tokens (user_id);
CREATE INDEX password_reset_tokens_expires_at ON password_reset_tokens (expires_at);

