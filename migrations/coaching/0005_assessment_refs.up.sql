-- Referensi lunak ke hasil analisis (F4-06; ADR-004 kopling #2).
--
-- assessment-svc menyiarkan assessment.completed; coaching menyimpan cuplikan
-- yang ia perlukan DI SINI, lalu StartProgram meresolusi slug dari tabel ini -
-- bukan dengan memanggil assessment-svc secara sinkron, dan bukan lewat FK
-- lintas skema yang ADR-006 larang. Sebelum tabel ini ada, slug yang dikirim
-- klien tidak pernah diterjemahkan menjadi apa pun: setiap program tersimpan
-- tanpa sumber analisisnya, dan D3 (satu program per analisis) tidak pernah
-- bisa ditegakkan. Test penerimaan D3-lah yang menemukannya.
CREATE TABLE coaching_assessments (
    -- Id analisis milik assessment-svc. Kunci primer, supaya event yang tiba
    -- dua kali (relay at-least-once) berhenti di ON CONFLICT DO NOTHING.
    id UUID PRIMARY KEY,

    user_id UUID NOT NULL,

    -- Slug publik yang dikirim klien saat memulai program.
    slug TEXT NOT NULL UNIQUE,

    -- Cuplikan yang disalin ke coaching_programs.assessment_snapshot saat
    -- program dimulai: slug, risk_percentage, risk_category, model_used.
    snapshot JSONB NOT NULL,

    completed_at TIMESTAMPTZ NOT NULL,
    recorded_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX coaching_assessments_by_user ON coaching_assessments (user_id);
