-- Skema chat-svc.
--
-- Dua tabel, sama seperti bentuk akhir sistem lama: percakapan, dan pesan yang
-- menggantung padanya. Sistem lama sampai ke sana lewat dua migrasi - pesan
-- semula menggantung pada profil, lalu dipindahkan ke percakapan. Yang ditulis
-- di sini adalah bentuk akhirnya; riwayat migrasinya tidak perlu ikut.

CREATE TABLE conversations (
    -- UUIDv7, seragam dengan seluruh platform (E16).
    id UUID PRIMARY KEY,

    -- Pemiliknya PENGGUNA, bukan profilnya (ADR-024).
    --
    -- Sistem lama sudah memakai user_id di ChatController sementara
    -- CoachingController memakai profile->id - dua pola untuk satu pertanyaan,
    -- separuh temuan S9. Yang dipilih di sini adalah pola yang benar, dan
    -- kebetulan itu pola yang sudah dipakai chat.
    user_id UUID NOT NULL,

    -- Slug publik. Klien tidak pernah melihat id internalnya, dan id berurutan
    -- membiarkan siapa pun menelusuri percakapan orang lain hanya dengan
    -- menghitung.
    slug TEXT NOT NULL UNIQUE,

    title TEXT NOT NULL,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Daftar percakapan selalu dibaca per pengguna, terbaru lebih dulu.
CREATE INDEX conversations_by_user ON conversations (user_id, updated_at DESC);

CREATE TABLE chat_messages (
    id UUID PRIMARY KEY,

    conversation_id UUID NOT NULL
        REFERENCES conversations (id) ON DELETE CASCADE,

    -- Hanya dua peran, ditegakkan basis data. Peran ketiga yang menyelinap
    -- masuk akan dikirim ke penyedia LLM sebagai peran yang tidak dikenalnya.
    role TEXT NOT NULL,

    -- TEXT, bukan JSONB, mengikuti sistem lama.
    --
    -- Percakapan umum menyimpan teks biasa; yang berstruktur adalah thread
    -- coaching. Menyimpannya sebagai JSONB akan memaksa setiap pesan lama
    -- dibungkus ulang tanpa ada yang membacanya sebagai struktur.
    content TEXT NOT NULL,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT chat_messages_role_known CHECK (role IN ('user', 'model')),

    -- Pesan kosong bukan pesan. Batas atasnya ada di Go - kolom TEXT tidak
    -- membatasi apa pun - tetapi batas bawahnya ditegakkan di sini, karena ia
    -- tidak bergantung pada konfigurasi apa pun.
    CONSTRAINT chat_messages_not_empty CHECK (length(btrim(content)) > 0)
);

-- Percakapan dibaca berurutan waktu, dari yang paling lama.
CREATE INDEX chat_messages_by_conversation ON chat_messages (conversation_id, created_at);
