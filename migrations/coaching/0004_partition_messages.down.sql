-- Mengembalikan coaching_messages menjadi tabel biasa. Isinya disalin utuh.
BEGIN;

CREATE TABLE coaching_messages_plain (
    id UUID PRIMARY KEY,
    coaching_thread_id UUID NOT NULL
        REFERENCES coaching_threads (id) ON DELETE CASCADE,
    role TEXT NOT NULL,
    content JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT coaching_messages_role_known CHECK (role IN ('user', 'model'))
);

INSERT INTO coaching_messages_plain (id, coaching_thread_id, role, content, created_at, updated_at)
SELECT id, coaching_thread_id, role, content, created_at, updated_at FROM coaching_messages;

DROP TABLE coaching_messages;
ALTER TABLE coaching_messages_plain RENAME TO coaching_messages;
CREATE INDEX coaching_messages_by_thread ON coaching_messages (coaching_thread_id, created_at);

COMMIT;
