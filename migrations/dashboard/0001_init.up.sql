-- Read-model dasbor.
--
-- Service ini TIDAK memiliki data apa pun. Setiap baris di sini adalah salinan
-- fakta yang dimiliki unit lain, dibentuk ulang menjadi bentuk yang dibaca satu
-- halaman. Semuanya boleh dihapus dan dibangun ulang dari awal topic; itu
-- justru diuji (F7-05).
--
-- Yang digantikannya di sistem lama: satu repository yang memanggil empat
-- repository lain, dibungkus Cache::remember 15 menit, ditambah EMPAT listener
-- yang menghapus cache itu secara manual saat sesuatu berubah. Keempat listener
-- itulah gejalanya - setiap kali sebuah fakta baru ditampilkan di dasbor,
-- seseorang harus ingat menambahkan listener kelima. Yang lupa menghasilkan
-- dasbor yang basi tanpa ada yang tahu (E17, ADR-009).

CREATE TABLE dashboards (
    -- Satu baris per PENGGUNA (ADR-024).
    --
    -- Kunci primernya, bukan kolom biasa dengan indeks: dua baris untuk satu
    -- orang berarti dua dasbor yang berbeda, dan mana yang benar tidak akan
    -- terjawab.
    user_id UUID PRIMARY KEY,

    -- Penilaian TIDAK diringkas ke dalam kolom di sini.
    --
    -- Versi pertama tabel ini menyimpan latest_*, previous_risk_percentage,
    -- dan total_assessments sebagai kolom yang diperbarui setiap event. Itu
    -- salah, dan salahnya terbukti saat dijalankan: dua penilaian yang tiba
    -- TERBALIK - hal biasa, karena Kafka menjamin urutan per kunci partisi dan
    -- penilaian dikunci pada id penilaiannya, bukan pada penggunanya -
    -- meninggalkan previous_risk_percentage kosong selamanya, sehingga dasbor
    -- menjawab "belum ada pembanding" untuk orang yang sudah dua kali
    -- menganalisis.
    --
    -- Ketiganya adalah turunan dari dashboard_assessments, yang sudah memuat
    -- seluruhnya. Menurunkannya saat DIBACA benar untuk urutan kedatangan apa
    -- pun, dan membuat penerapan event menjadi satu INSERT yang idempoten
    -- secara struktural - bukan urutan CASE yang harus benar.

    -- Program coaching berjalan, disalin dari coaching.program.updated.
    -- NULL berarti tidak ada program - juga keadaan yang sah.
    program_slug        TEXT,
    program_title       TEXT,
    program_status      TEXT,
    program_current_day INT,
    program_total_days  INT,

    -- Persentase penyelesaian disimpan TERPISAH dan boleh NULL.
    --
    -- Event program terbit dari dua tempat, dan yang satu tidak menghitung
    -- tugas sama sekali. Menyimpan nol untuk "belum dihitung" akan membuat
    -- dasbor melompat ke nol persen setiap kali program dijeda.
    program_completion_percentage NUMERIC(5,2),

    -- Kapan proyeksi ini terakhir bergerak. Dibuka apa adanya lewat API:
    -- read-model bersifat eventually consistent, dan menyembunyikan jedanya
    -- membuat jeda itu tampak seperti bug.
    projected_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Riwayat penilaian dan titik grafik, satu baris per penilaian.
--
-- Tabel terpisah, bukan JSONB di dalam dashboards: riwayatnya tumbuh tanpa
-- batas, dan dokumen yang tumbuh berarti seluruh baris ditulis ulang setiap
-- kali satu penilaian ditambahkan.
CREATE TABLE dashboard_assessments (
    user_id UUID NOT NULL,

    -- Slug penilaiannya. Bersama user_id ia kunci primer, dan itu yang membuat
    -- proyeksi IDEMPOTEN: event yang sama diputar dua kali menulis baris yang
    -- sama, bukan baris kedua (F7-03).
    slug TEXT NOT NULL,

    assessed_at     TIMESTAMPTZ NOT NULL,
    risk_percentage NUMERIC(5,2) NOT NULL,
    risk_category   TEXT NOT NULL,
    model_used      TEXT NOT NULL,

    PRIMARY KEY (user_id, slug)
);

-- Riwayat dibaca per pengguna, terbaru lebih dulu. slug ikut sebagai pemecah
-- seri: dua penilaian pada detik yang sama akan terurut sesukanya tanpa itu,
-- dan halaman kedua bisa mengulang baris halaman pertama.
CREATE INDEX dashboard_assessments_by_user
    ON dashboard_assessments (user_id, assessed_at DESC, slug DESC);

-- Posisi konsumen, satu baris per proyektor.
--
-- Ia BUKAN pengganti offset Kafka - itu tetap milik consumer group. Yang
-- disimpan di sini adalah tanda "sampai kapan proyeksi ini sudah dibangun",
-- dipakai perintah rebuild untuk menyatakan hasilnya lengkap, dan dipakai
-- pengukuran lag untuk mengetahui event terakhir yang sudah masuk.
CREATE TABLE projection_state (
    name TEXT PRIMARY KEY,

    -- Waktu OCCURRED_AT event terakhir yang diproyeksikan, bukan waktu
    -- pemrosesannya. Selisih antara keduanya adalah lag-nya, dan itu yang
    -- diukur F7-06.
    last_event_at TIMESTAMPTZ,

    events_applied BIGINT NOT NULL DEFAULT 0,
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
