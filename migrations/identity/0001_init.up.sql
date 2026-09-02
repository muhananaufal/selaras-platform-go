-- Skema identity. Dijalankan oleh peran svc_identity, yang hanya punya hak
-- di skema ini (deploy/compose/initdb/01-schemas.sh).

CREATE TABLE users (
    id                UUID        PRIMARY KEY,
    email             TEXT        NOT NULL,
    role              TEXT        NOT NULL DEFAULT 'user',
    -- Boleh NULL, dan itu disengaja. Sistem lama memakai kolom NOT NULL
    -- lalu mengisinya dengan hash dari string acak untuk pengguna Google;
    -- hash itu menyatakan ada kredensial yang bisa dipakai, padahal tidak.
    password_hash     TEXT,
    google_id         TEXT,
    email_verified_at TIMESTAMPTZ,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at        TIMESTAMPTZ,

    CONSTRAINT users_role_known CHECK (role IN ('user', 'admin')),
    -- Sebuah akun WAJIB punya setidaknya satu cara untuk masuk. Tanpa
    -- batasan ini, baris tanpa kata sandi dan tanpa google_id adalah akun
    -- yang tidak bisa dimasuki siapa pun dan tidak bisa dipulihkan.
    CONSTRAINT users_has_a_credential CHECK (
        password_hash IS NOT NULL OR google_id IS NOT NULL
    )
);

-- Keunikan diberlakukan hanya atas baris yang hidup. Indeks unik biasa akan
-- membuat sebuah alamat email hangus selamanya begitu akunnya dihapus lunak,
-- karena baris matinya tetap menempati alamat itu.
CREATE UNIQUE INDEX users_email_unique_alive
    ON users (email) WHERE deleted_at IS NULL;

CREATE UNIQUE INDEX users_google_id_unique_alive
    ON users (google_id) WHERE google_id IS NOT NULL AND deleted_at IS NULL;

-- Token reset kata sandi. Menutup S1: sistem lama menerbitkan token yang
-- tidak pernah benar-benar diperiksa sebelum kata sandi berubah.
CREATE TABLE password_reset_tokens (
    -- Yang disimpan hash-nya, bukan tokennya. Bocornya basis data tidak
    -- boleh langsung berarti bocornya kemampuan mengambil alih akun.
    token_hash BYTEA       PRIMARY KEY,
    user_id    UUID        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    expires_at TIMESTAMPTZ NOT NULL,
    used_at    TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX password_reset_tokens_user_id ON password_reset_tokens (user_id);
CREATE INDEX password_reset_tokens_expires_at ON password_reset_tokens (expires_at);

-- Daftar cabut token akses (F1-09). Barisnya disapu setelah exp terlampaui;
-- setelah itu tanda tangannya sendiri yang menolak, jadi catatannya
-- tidak perlu disimpan selamanya.
CREATE TABLE revoked_tokens (
    jti        UUID        PRIMARY KEY,
    user_id    UUID        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    expires_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX revoked_tokens_expires_at ON revoked_tokens (expires_at);
