-- Skema llm-worker.
--
-- Worker ini menyimpan keadaannya sendiri, bukan menumpang skema assessment.
-- Sekat per skema itulah yang membuat kesalahan di satu service tidak bisa
-- menyentuh data service lain (ADR-006), dan menumpang akan membuang sekatnya
-- justru di tempat yang paling banyak memanggil pihak luar.

-- Pekerjaan LLM beserta hasilnya.
CREATE TABLE llm_jobs (
    -- UUIDv7: terurut waktu, sehingga pekerjaan bisa dibaca dalam urutan
    -- kedatangannya tanpa kolom urutan terpisah.
    id UUID NOT NULL,

    -- created_at ikut kunci primer karena ia kunci partisi (F3-17).
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- Kunci idempotensi yang datang bersama permintaannya. Ia yang membuat
    -- pesan yang tiba dua kali menghasilkan satu pekerjaan.
    idempotency_key TEXT NOT NULL,

    -- Jenis pekerjaan: personalization, curriculum, chat_reply, meal_guide.
    kind TEXT NOT NULL,

    -- Agregat yang meminta, sehingga hasilnya bisa dikembalikan ke tempat yang
    -- benar tanpa menebak.
    aggregate_type TEXT NOT NULL,
    aggregate_id   TEXT NOT NULL,

    status TEXT NOT NULL DEFAULT 'pending',

    -- Versi prompt yang menghasilkan hasilnya (F3-09).
    --
    -- Tanpa ini, laporan lama yang terlihat aneh tidak bisa dijelaskan: tidak
    -- ada cara mengetahui apakah modelnya yang menjawab begitu atau templatnya
    -- yang sudah diganti sejak itu.
    prompt_version TEXT,

    -- Nama model yang benar-benar menjawab, sebagaimana dilaporkan penyedia -
    -- bukan yang diminta. Keduanya bisa berbeda saat penyedia mengalihkan
    -- permintaan, dan yang perlu dicatat adalah yang menjawab.
    model TEXT,

    -- Hasilnya. BYTEA, bukan JSONB: yang disimpan adalah persis yang dikirim
    -- kembali, tanpa penyandian ulang yang bisa mengubah bentuknya.
    result BYTEA,

    attempts INT NOT NULL DEFAULT 0,
    last_error TEXT,

    started_at  TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,

    PRIMARY KEY (id, created_at),

    CONSTRAINT llm_jobs_status_known CHECK (
        status IN ('pending', 'running', 'completed', 'failed', 'dead')
    ),

    -- Pekerjaan yang selesai wajib membawa hasil dan asal-usulnya. Tanpa
    -- batasan ini, baris berstatus completed dengan result NULL akan terlihat
    -- seperti keberhasilan sampai ada yang membacanya.
    CONSTRAINT llm_jobs_completed_has_a_result CHECK (
        status <> 'completed'
        OR (result IS NOT NULL AND prompt_version IS NOT NULL AND model IS NOT NULL)
    )
) PARTITION BY RANGE (created_at);

-- Partisi dipasang saat tabel dibuat (F3-17), sama seperti outbox. Mengubah
-- tabel yang sudah terisi menjadi terpartisi berarti menyalin seluruh isinya
-- sambil menahan kunci.
--
-- Partisi bawaan menangkap apa pun di luar rentang yang sudah dibuat, sehingga
-- INSERT untuk bulan yang belum diprovisikan tidak gagal dan menyeret
-- transaksinya ikut gagal.
CREATE TABLE llm_jobs_default PARTITION OF llm_jobs DEFAULT;

-- Satu pekerjaan per kunci idempotensi.
--
-- Keunikannya TIDAK bisa ditegakkan di tabel terpartisi tanpa memasukkan kunci
-- partisi, jadi penjaganya bukan indeks ini melainkan processed_messages, yang
-- sengaja tidak dipartisi. Indeks di sini untuk pencarian, bukan untuk jaminan
-- - dan komentar ini ada supaya tidak ada yang mengira sebaliknya.
CREATE INDEX llm_jobs_by_idempotency_key ON llm_jobs (idempotency_key);

-- Pekerjaan yang belum selesai, untuk pemantauan dan pemulihan.
CREATE INDEX llm_jobs_unfinished ON llm_jobs (created_at)
    WHERE status IN ('pending', 'running');

CREATE INDEX llm_jobs_by_aggregate ON llm_jobs (aggregate_type, aggregate_id, created_at DESC);
