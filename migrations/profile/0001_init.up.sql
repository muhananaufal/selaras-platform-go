-- Skema profile. Dijalankan oleh peran svc_profile, yang hanya punya hak di
-- skema ini (deploy/compose/initdb/01-schemas.sh).

CREATE TABLE user_profiles (
    id         UUID PRIMARY KEY,

    -- Menunjuk ke identity.users, TANPA foreign key, dan itu disengaja.
    --
    -- Kunci asing lintas skema akan memaksa peran svc_profile punya hak baca
    -- di skema identity, dan dengan itu membatalkan isolasi yang justru
    -- ditegakkan basis datanya sendiri (ADR-006). Ia juga menjadikan kedua
    -- service satu satuan penyebaran: migrasi salah satunya bisa memblokir
    -- tulisan di yang lain.
    --
    -- Harganya nyata dan diterima sadar: baris yatim mungkin ada. Yang
    -- membersihkannya adalah saga penghapusan akun (F8), bukan basis data.
    user_id    UUID NOT NULL,

    -- Semuanya boleh NULL. Sistem lama juga begitu, tetapi lalu menjalankan
    -- Carbon::parse(null) di lapisan penyajian sehingga tanggal lahir yang
    -- kosong tampil sebagai hari ini dan umur tampil 0 (temuan B6). Yang
    -- diperbaiki bukan kolomnya - kolomnya memang benar - melainkan
    -- kejujuran pemetaannya.
    first_name           TEXT,
    last_name            TEXT,
    date_of_birth        DATE,
    sex                  TEXT,
    country_of_residence TEXT,

    -- language punya nilai bawaan dan tidak boleh NULL, sama seperti sistem
    -- lama. Antarmuka harus memilih bahasa untuk setiap pengguna, jadi
    -- "belum ditentukan" bukan keadaan yang berguna di sini.
    language   TEXT        NOT NULL DEFAULT 'id',

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT user_profiles_sex_known CHECK (sex IS NULL OR sex IN ('male', 'female')),
    CONSTRAINT user_profiles_language_known CHECK (language IN ('id', 'en')),

    -- Tanggal lahir di masa depan tidak mungkin benar. Batasnya di basis
    -- data, bukan hanya di validasi permintaan, karena mesin risiko membaca
    -- kolom ini dan umur negatif akan mengalir diam-diam ke perhitungan
    -- klinis.
    CONSTRAINT user_profiles_dob_in_the_past CHECK (date_of_birth IS NULL OR date_of_birth < CURRENT_DATE)
);

-- Satu profil per pengguna. Sistem lama memberlakukannya lewat unique pada
-- kolom foreign key; di sini indeksnya berdiri sendiri karena kunci asingnya
-- memang tidak ada.
CREATE UNIQUE INDEX user_profiles_user_id_unique ON user_profiles (user_id);
