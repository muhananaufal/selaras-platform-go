-- Second factors (ADR-032). Every table references users with ON DELETE
-- CASCADE: the deletion saga ends by deleting the users row, and a factor
-- left behind would be an authenticator for an account that no longer exists.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '30s';

-- TOTP. The secret is sealed with AES-256-GCM under MFA_ENCRYPTION_KEY, with
-- the user id as associated data, so neither a database dump nor a row copied
-- to another account yields a usable secret.
CREATE TABLE mfa_totp (
    user_id           UUID        PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    secret_ciphertext BYTEA       NOT NULL,
    -- Which key sealed the secret, so the key can be rotated.
    key_version       BIGINT      NOT NULL,
    -- NULL while enrolment is pending: the secret was shown but never proven
    -- with a code, and it does not count as a factor.
    confirmed_at      TIMESTAMPTZ,
    -- The last accepted 30-second time step. A code from this step or an
    -- earlier one is refused: an OTP is accepted only once (NIST SP 800-63B
    -- 3.1.4.2).
    last_used_step    BIGINT      NOT NULL DEFAULT 0,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Recovery codes: the first four characters find the row, the rest is
-- verified with argon2id. Below 112 bits a look-up secret must be stored
-- salted and hashed with a password hashing scheme (NIST SP 800-63B 3.1.2.2).
CREATE TABLE mfa_recovery_codes (
    user_id    UUID        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    lookup     TEXT        NOT NULL,
    code_hash  TEXT        NOT NULL,
    used_at    TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, lookup)
);

-- Consecutive second-factor failures across tickets. Ten lock verification
-- for fifteen minutes; a hundred disable TOTP (NIST SP 800-63B 3.2.2).
CREATE TABLE mfa_attempts (
    user_id              UUID        PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    consecutive_failures BIGINT      NOT NULL DEFAULT 0,
    locked_until         TIMESTAMPTZ,
    CONSTRAINT mfa_attempts_failures_not_negative CHECK (consecutive_failures >= 0)
);

-- The WebAuthn user handle: random, not the user id. The handle is stored on
-- the authenticator and returned in every assertion, and the spec asks for it
-- to carry no personal information (WebAuthn L3 5.4.3).
CREATE TABLE webauthn_user_handles (
    user_id UUID  PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    handle  BYTEA NOT NULL UNIQUE,
    CONSTRAINT webauthn_user_handles_length CHECK (octet_length(handle) BETWEEN 16 AND 64)
);

-- Passkeys. The credential is kept as go-webauthn serialises it (public key,
-- sign count, flags, attestation), so a library upgrade that adds a field
-- does not need a migration; credential_id is a column of its own because a
-- passwordless sign-in looks the credential up by it.
CREATE TABLE webauthn_credentials (
    id            UUID        PRIMARY KEY,
    user_id       UUID        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    credential_id BYTEA       NOT NULL UNIQUE,
    credential    JSONB       NOT NULL,
    name          TEXT        NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at  TIMESTAMPTZ,
    CONSTRAINT webauthn_credentials_name_length CHECK (char_length(name) BETWEEN 1 AND 64)
);

CREATE INDEX webauthn_credentials_user_id ON webauthn_credentials (user_id);
