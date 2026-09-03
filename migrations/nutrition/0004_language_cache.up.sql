-- Bahasa pengguna, disalin dari event profile.updated.
--
-- Alasannya sama dengan cache profil di assessment-svc (ADR-007): membuat
-- panduan menu tidak boleh memanggil profile-svc pada setiap permintaan.
-- Panggilan itu menambah kegagalan yang bisa dihindari - profile-svc yang mati
-- membuat panduan menu ikut mati - untuk data yang berubah beberapa kali
-- setahun.
--
-- Ini CACHE, bukan sumber kebenaran. profile-svc tetap pemiliknya. Yang di sini
-- boleh basi, boleh hilang, dan boleh dibangun ulang dari awal topic. Yang
-- TIDAK boleh adalah menjadi satu-satunya tempat sebuah fakta ada - dan
-- bahasanya memang tidak: ia selalu punya nilai bawaan yang bisa dipakai.
--
-- Hanya bahasa yang disalin, bukan seluruh profil. Menyalin lebih banyak dari
-- yang dipakai berarti menyimpan salinan yang tidak pernah dibaca siapa pun,
-- lalu harus ikut dihapus saat akun dihapus.

CREATE TABLE user_languages (
    -- Dikunci pada user_id: itu identitas yang sudah terverifikasi di setiap
    -- permintaan (ADR-023, ADR-024).
    user_id UUID PRIMARY KEY,

    language TEXT NOT NULL,

    -- Waktu event yang menghasilkan baris ini, BUKAN waktu penulisannya.
    --
    -- Ia yang menahan event yang tiba terlambat menimpa yang lebih baru: Kafka
    -- menjamin urutan per partisi, tetapi partisi bisa berubah dan konsumen
    -- bisa diputar ulang.
    observed_at TIMESTAMPTZ NOT NULL,

    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Cache yang basi harus bisa ditemukan tanpa memindai seluruh tabel.
CREATE INDEX user_languages_by_age ON user_languages (observed_at);
