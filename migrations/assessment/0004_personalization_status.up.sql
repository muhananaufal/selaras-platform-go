-- Status personalisasi, sebagai kolom tersendiri.
--
-- Sebelum ini, statusnya DITURUNKAN dari ada tidaknya result_details - yang
-- hanya bisa membedakan dua keadaan: belum diminta, dan selesai. Klien tidak
-- bisa membedakan "sedang dikerjakan" dari "belum pernah diminta", dan tidak
-- bisa tahu sama sekali kalau pekerjaannya gagal: keduanya terlihat sebagai
-- laporan yang tidak ada.
--
-- Kolom ini yang membuat F3-12 mungkin.

ALTER TABLE risk_assessments
    ADD COLUMN personalization_status TEXT NOT NULL DEFAULT 'not_requested';

ALTER TABLE risk_assessments
    ADD CONSTRAINT risk_assessments_personalization_status_known CHECK (
        personalization_status IN ('not_requested', 'pending', 'completed', 'failed')
    );

-- Baris yang sudah punya laporan berstatus completed.
--
-- Tanpa ini, penilaian lama yang laporannya sudah ada akan berstatus
-- not_requested, dan klien akan menawarkan tombol "buat laporan" untuk laporan
-- yang sudah di layar.
UPDATE risk_assessments
SET personalization_status = 'completed'
WHERE result_details IS NOT NULL;

-- Alasan gagalnya, supaya kegagalan bisa dijelaskan alih-alih hanya dihitung.
ALTER TABLE risk_assessments
    ADD COLUMN personalization_error TEXT;

-- Pekerjaan yang menggantung, untuk pemantauan: pending yang tidak pernah
-- berubah adalah gejala worker yang mati atau event yang hilang.
CREATE INDEX risk_assessments_personalization_pending
    ON risk_assessments (updated_at)
    WHERE personalization_status = 'pending';
