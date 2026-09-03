-- Tabel idempotensi untuk identity.
--
-- Isinya identik di setiap service dan berasal dari satu sumber:
-- internal/platform/idempotency/schema.sql.

-- Tabel idempotensi. Satu per skema service.
--
-- Ia menjawab satu pertanyaan: "pekerjaan dengan kunci ini sudah pernah
-- dikerjakan atau belum?" Jawabannya harus benar meski dua proses bertanya
-- pada saat yang sama, dan itulah sebabnya jawabannya datang dari kunci primer
-- basis data - bukan dari SELECT lalu INSERT, yang di antara keduanya ada
-- celah tempat keduanya membaca "belum".

CREATE TABLE processed_messages (
    -- Kunci idempotensi. Ia kunci primer, dan itu bukan pilihan gaya:
    -- INSERT ... ON CONFLICT DO NOTHING hanya bisa menjadi penjaga kalau
    -- basis data yang menegakkan keunikannya.
    key TEXT PRIMARY KEY,

    -- Ruang lingkup pemakainya - nama konsumen atau use case.
    --
    -- Dua konsumen berbeda yang memproses event yang sama tidak boleh saling
    -- meniadakan: penulis cache yang sudah menangani sebuah event tidak berarti
    -- pengirim notifikasi juga sudah. Ia ikut ke dalam kuncinya di sisi Go,
    -- dan disimpan terpisah di sini supaya bisa disaring saat menyelidiki.
    scope TEXT NOT NULL,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- Hasil pekerjaannya, bila ada.
    --
    -- Tanpa ini, permintaan ulang hanya bisa dijawab "sudah pernah" - dan
    -- pemanggil yang kehilangan jawaban pertamanya tidak punya cara mendapatkan
    -- jawaban yang sama. Dengan ini, ia mendapat jawaban yang sama persis.
    result BYTEA
);

-- Tabel ini SENGAJA tidak dipartisi, berbeda dari outbox.
--
-- Partisi mensyaratkan kunci partisi ikut ke dalam setiap batasan unik, jadi
-- kunci primernya harus menjadi (key, created_at) - dan keunikan key sendiri
-- berhenti ditegakkan lintas partisi. Justru itu satu-satunya hal yang
-- dijanjikan tabel ini. Pertumbuhannya ditangani dengan menyapu baris lama
-- (Sweep), bukan dengan melepas partisi.
CREATE INDEX processed_messages_by_age ON processed_messages (created_at);
