-- F9-28: chat_messages dipartisi menurut rentang waktu.
--
-- Mengapa sekarang, bukan nanti: mengubah tabel yang sudah besar menjadi
-- terpartisi berarti menyalin seluruh isinya sambil menahan kunci, dan pesan
-- adalah tabel yang tumbuh paling cepat di seluruh sistem. Di lingkungan yang
-- datanya masih kecil, salinannya milidetik; di produksi setahun kemudian,
-- ia jam.
--
-- Mengapa rentang waktu, bukan hash percakapan: yang dibaca selalu "pesan
-- terbaru satu percakapan" (indeks conversation_id, created_at), dan yang
-- akan dipelihara adalah usia - partisi lama bisa dilepas utuh bila suatu
-- saat ada kebijakan retensi, bukan dihapus baris per baris (F9-29).
--
-- PRIMARY KEY harus memuat kunci partisi; karena itu (id, created_at). id
-- tetap UUID unik secara praktis, dan tidak ada tabel lain yang merujuk ke
-- chat_messages, jadi tidak ada FK yang perlu diubah.

BEGIN;

CREATE TABLE chat_messages_partitioned (
    id UUID NOT NULL,
    conversation_id UUID NOT NULL
        REFERENCES conversations (id) ON DELETE CASCADE,
    role TEXT NOT NULL,
    content TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT chat_messages_role_known CHECK (role IN ('user', 'model')),
    CONSTRAINT chat_messages_not_empty CHECK (length(btrim(content)) > 0),
    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);

-- Partisi bawaan menangkap bulan yang partisinya belum dibuat; tanpa ia,
-- INSERT untuk bulan baru akan GAGAL dan membawa transaksi bisnisnya.
-- Partisi bulanan dibuat oleh cmd/partitions (F9-29).
CREATE TABLE chat_messages_default PARTITION OF chat_messages_partitioned DEFAULT;

-- Indeks pada induk diwariskan ke setiap partisi, termasuk yang dibuat
-- kemudian.
CREATE INDEX chat_messages_by_conversation_p
    ON chat_messages_partitioned (conversation_id, created_at);

INSERT INTO chat_messages_partitioned (id, conversation_id, role, content, created_at, updated_at)
SELECT id, conversation_id, role, content, created_at, updated_at FROM chat_messages;

DROP TABLE chat_messages;
ALTER TABLE chat_messages_partitioned RENAME TO chat_messages;
ALTER INDEX chat_messages_by_conversation_p RENAME TO chat_messages_by_conversation;

COMMIT;
