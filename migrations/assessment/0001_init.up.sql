-- Skema assessment. Dijalankan oleh peran svc_assessment, yang hanya punya
-- hak di skema ini (deploy/compose/initdb/01-schemas.sh).

CREATE TABLE risk_assessments (
    id              UUID PRIMARY KEY,

    -- Menunjuk ke profile.user_profiles, TANPA foreign key lintas skema -
    -- alasannya sama seperti di skema profile (ADR-006): kunci asing lintas
    -- skema membatalkan isolasi yang ditegakkan basis datanya sendiri.
    user_profile_id UUID NOT NULL,

    -- Slug adalah id publik. Ia berbeda dari kunci primer supaya id internal
    -- tidak pernah muncul di URL, dan supaya ia bisa dicari tanpa
    -- mengungkapkan berapa banyak penilaian yang pernah dibuat.
    slug            TEXT NOT NULL,

    model_used      TEXT NOT NULL,

    -- NUMERIC, bukan FLOAT seperti sistem lama.
    --
    -- Angka ini dibaca orang tentang jantungnya sendiri dan dibandingkan
    -- antar waktu. Float biner tidak bisa mewakili 66.85 dengan tepat,
    -- sehingga nilai yang disimpan dan nilai yang dihitung bisa berbeda di
    -- digit terakhir - dan perbedaan itu muncul sebagai riwayat yang
    -- berubah sendiri.
    final_risk_percentage NUMERIC(5,2) NOT NULL,

    -- Cuplikan lengkap sesi analisis. inputs adalah jawaban asli pengguna;
    -- generated_values adalah nilai klinis yang benar-benar masuk ke model,
    -- entah diketik atau ditebak.
    --
    -- Keduanya disimpan karena angka risiko tanpa masukannya tidak bisa
    -- dibantah siapa pun - termasuk oleh kami sendiri saat menyelidiki
    -- keluhan.
    inputs           JSONB NOT NULL,
    generated_values JSONB NOT NULL,

    -- Diisi belakangan oleh llm-worker (F3). NULL berarti belum ada, dan itu
    -- keadaan yang sah: penilaian selesai tanpanya.
    result_details   JSONB,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- Risiko di luar 0-100 tidak mungkin benar. Batasnya di basis data
    -- karena kolom ini dibaca unit lain - dashboard dan coaching - yang
    -- tidak akan memeriksa ulang.
    CONSTRAINT risk_assessments_percentage_in_range
        CHECK (final_risk_percentage >= 0 AND final_risk_percentage <= 100),

    CONSTRAINT risk_assessments_model_known
        CHECK (model_used IN ('SCORE2', 'SCORE2-OP', 'SCORE2-Diabetes'))
);

CREATE UNIQUE INDEX risk_assessments_slug_unique ON risk_assessments (slug);

-- Riwayat selalu dibaca per pengguna dan terurut waktu. Indeks gabungan ini
-- melayani keduanya sekaligus; dua indeks terpisah akan memaksa pengurutan
-- setelah pembacaan.
CREATE INDEX risk_assessments_by_profile_recent
    ON risk_assessments (user_profile_id, created_at DESC);
