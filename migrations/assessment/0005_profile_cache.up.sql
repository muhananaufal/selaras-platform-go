-- Cuplikan profil yang disalin dari event profile.updated (F2-16).
--
-- Alasannya ADR-007: kalkulasi risiko tidak boleh memanggil service lain pada
-- setiap permintaan. Panggilan itu menambah kegagalan yang bisa dihindari -
-- profile-svc yang lambat membuat penilaian lambat, profile-svc yang mati
-- membuat penilaian mati - untuk data yang berubah beberapa kali setahun.
--
-- Ini CACHE, bukan sumber kebenaran. Profile-svc tetap pemiliknya. Yang di sini
-- boleh basi, boleh hilang, dan boleh dibangun ulang dari awal topic.

CREATE TABLE profile_snapshots (
    -- Dikunci pada user_id, BUKAN user_profile_id.
    --
    -- user_id adalah identitas yang sudah terverifikasi di setiap permintaan
    -- (ADR-023). Cache yang dikunci pada id profil akan memaksa assessment
    -- memanggil profile-svc lebih dulu untuk menerjemahkannya - dan panggilan
    -- itulah yang seharusnya dihilangkan cache ini.
    user_id UUID PRIMARY KEY,

    user_profile_id UUID NOT NULL,

    -- Ketiganya boleh NULL: profil yang belum diisi adalah keadaan yang sah
    -- (ADR-002 aturan 2). NULL berarti "belum diisi", berbeda dari nilai
    -- kosong yang berarti "diketahui kosong".
    date_of_birth        DATE,
    sex                  TEXT,
    country_of_residence TEXT,

    language TEXT NOT NULL DEFAULT 'id',

    -- Waktu event yang menghasilkan baris ini, BUKAN waktu penulisannya.
    --
    -- Ia yang menahan event yang tiba terlambat menimpa yang lebih baru:
    -- Kafka menjamin urutan per partisi, tetapi partisi bisa berubah dan
    -- konsumen bisa diputar ulang.
    observed_at TIMESTAMPTZ NOT NULL,

    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Cache yang basi harus bisa ditemukan tanpa memindai seluruh tabel.
CREATE INDEX profile_snapshots_by_age ON profile_snapshots (observed_at);
