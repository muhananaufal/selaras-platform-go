-- Pelacakan saga penghapusan akun (F8-01).
--
-- Penghapusan akun menyentuh ENAM unit yang masing-masing memiliki basis
-- datanya sendiri, dan tidak ada transaksi yang bisa merangkul keenamnya.
-- Yang menggantikannya adalah saga: satu permintaan, enam konfirmasi, dan
-- catatan tentang siapa yang belum menjawab.
--
-- Tanpa catatan itu, penghapusan yang berhenti di tengah tidak meninggalkan
-- jejak apa pun. Datanya tetap ada di unit yang tidak dituju siapa pun lagi,
-- dan tidak seorang pun tahu ia di sana - termasuk pengguna yang memintanya
-- dihapus.

CREATE TABLE deletion_sagas (
    -- UUIDv7: terurut waktu, sehingga saga yang menggantung paling lama ada di
    -- awal daftar tanpa perlu kolom urutan terpisah.
    id UUID PRIMARY KEY,

    user_id UUID NOT NULL,

    -- Id profil ikut dibawa karena beberapa unit menyimpan datanya dengan
    -- kunci itu, bukan dengan user_id. Ia disalin SEKARANG, saat profilnya
    -- masih ada: setelah profile-svc menghapus barisnya, tidak ada lagi yang
    -- bisa menerjemahkannya.
    user_profile_id UUID,

    -- Keadaan saga secara keseluruhan.
    --
    -- 'requested'  : sudah diumumkan, menunggu konfirmasi
    -- 'completed'  : keenam unit mengonfirmasi, akun sudah dihapus
    -- 'failed'     : satu unit atau lebih menyatakan gagal
    --
    -- Tidak ada 'compensating'. Penghapusan TIDAK bisa dibatalkan - data yang
    -- sudah hilang tidak kembali - jadi kompensasinya bukan mengembalikan
    -- keadaan, melainkan membuat kegagalannya TERLIHAT dan bisa diselesaikan
    -- manusia. Lihat docs/runbook/account-deletion.md.
    status TEXT NOT NULL DEFAULT 'requested'
        CHECK (status IN ('requested', 'completed', 'failed')),

    requested_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at  TIMESTAMPTZ,

    -- finished_at HANYA ada saat saganya sudah berakhir, dan selalu ada saat
    -- ia berakhir. Ditegakkan di sini supaya "sudah selesai kapan" tidak
    -- pernah menjadi pertanyaan yang tidak bisa dijawab barisnya sendiri.
    CONSTRAINT deletion_sagas_finished_when_over
        CHECK ((status = 'requested') = (finished_at IS NULL))
);

-- Saga yang menggantung harus bisa ditemukan tanpa memindai seluruh tabel.
-- Indeks parsial: hanya yang belum selesai yang pernah ditanyakan seperti ini.
CREATE INDEX deletion_sagas_outstanding
    ON deletion_sagas (requested_at)
    WHERE status = 'requested';

-- Satu pengguna tidak boleh punya dua saga berjalan sekaligus.
--
-- Dua saga berarti dua rangkaian konfirmasi untuk satu akun, dan yang kedua
-- akan mengira dirinya belum lengkap karena unit-unitnya sudah menjawab yang
-- pertama. Indeks unik PARSIAL: setelah selesai, saga lama boleh berdampingan
-- dengan yang baru - meski dalam praktiknya akunnya sudah tidak ada.
CREATE UNIQUE INDEX deletion_sagas_one_per_user
    ON deletion_sagas (user_id)
    WHERE status = 'requested';

-- Konfirmasi per unit.
--
-- Tabel terpisah, bukan kolom boolean per unit di deletion_sagas: menambah
-- unit ketujuh nanti akan menjadi migrasi ALTER TABLE, dan yang lupa
-- menambahkannya menghasilkan saga yang selesai tanpa unit itu pernah
-- dihubungi.
CREATE TABLE deletion_confirmations (
    saga_id UUID NOT NULL REFERENCES deletion_sagas (id) ON DELETE CASCADE,

    -- Nama unit yang mengonfirmasi: 'profile', 'assessment', dan seterusnya.
    service TEXT NOT NULL,

    succeeded BOOLEAN NOT NULL,

    -- Alasan kegagalan, bila ada. Ia yang dibaca manusia saat menyelesaikan
    -- saga yang macet.
    failure_reason TEXT,

    confirmed_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- Satu unit satu konfirmasi. Inilah gerbang idempotensinya: relay outbox
    -- bersifat at-least-once, jadi konfirmasi yang sama BISA tiba dua kali,
    -- dan yang kedua tidak boleh membuat saga mengira ada tujuh unit menjawab.
    PRIMARY KEY (saga_id, service),

    -- Alasan kegagalan HANYA ada saat gagal. Yang berhasil tetapi membawa
    -- alasan adalah baris yang menceritakan dua hal berbeda sekaligus.
    CONSTRAINT deletion_confirmations_reason_when_failed
        CHECK (succeeded OR failure_reason IS NOT NULL)
);
