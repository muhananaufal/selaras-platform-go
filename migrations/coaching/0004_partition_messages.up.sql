-- F9-28: coaching_messages is partitioned by time range.
--
-- The reason is the same as for chat_messages (see migrations/chat/0004): the
-- fastest-growing table is converted while small, is read through the
-- (coaching_thread_id, created_at) index that remains per partition, and old
-- partitions can be detached whole if a retention policy exists. The PRIMARY
-- KEY contains the partition key; no other table references
-- coaching_messages.

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
