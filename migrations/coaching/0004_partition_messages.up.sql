-- F9-28: coaching_messages dipartisi menurut rentang waktu.
--
-- Alasannya sama dengan chat_messages (lihat migrations/chat/0004): tabel
-- yang tumbuh paling cepat diubah selagi kecil, dibaca lewat indeks
-- (coaching_thread_id, created_at) yang tetap ada per partisi, dan partisi
-- lama bisa dilepas utuh bila ada kebijakan retensi. PRIMARY KEY memuat kunci
-- partisi; tidak ada tabel lain yang merujuk ke coaching_messages.

BEGIN;

CREATE TABLE coaching_messages_partitioned (
    id UUID NOT NULL,
    coaching_thread_id UUID NOT NULL
        REFERENCES coaching_threads (id) ON DELETE CASCADE,
    role TEXT NOT NULL,
    content JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT coaching_messages_role_known CHECK (role IN ('user', 'model')),
    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);

CREATE TABLE coaching_messages_default PARTITION OF coaching_messages_partitioned DEFAULT;

CREATE INDEX coaching_messages_by_thread_p
    ON coaching_messages_partitioned (coaching_thread_id, created_at);

INSERT INTO coaching_messages_partitioned (id, coaching_thread_id, role, content, created_at, updated_at)
SELECT id, coaching_thread_id, role, content, created_at, updated_at FROM coaching_messages;

DROP TABLE coaching_messages;
ALTER TABLE coaching_messages_partitioned RENAME TO coaching_messages;
ALTER INDEX coaching_messages_by_thread_p RENAME TO coaching_messages_by_thread;

COMMIT;
