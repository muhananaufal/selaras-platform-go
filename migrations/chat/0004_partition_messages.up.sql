-- F9-28: chat_messages is partitioned by time range.
--
-- Why now, not later: turning an already large table into a partitioned one
-- means copying its entire contents while holding a lock, and messages are
-- the fastest-growing table in the whole system. In an environment where the
-- data is still small, the copy takes milliseconds; in production a year
-- later, hours.
--
-- Why time range, not a hash of the conversation: what is read is always "the
-- newest messages of one conversation" (the conversation_id, created_at
-- index), and what will be maintained is age - old partitions can be detached
-- whole if a retention policy ever exists, rather than deleted row by row
-- (F9-29).
--
-- The PRIMARY KEY has to contain the partition key; hence (id, created_at).
-- id remains a practically unique UUID, and no other table references
-- chat_messages, so no FK has to change.

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

-- The default partition catches months whose partition has not been created
-- yet; without it, an INSERT for a new month would FAIL and take the
-- business transaction with it. Monthly partitions are created by
-- cmd/partitions (F9-29).
CREATE TABLE chat_messages_default PARTITION OF chat_messages_partitioned DEFAULT;

-- An index on the parent is inherited by every partition, including those
-- created later.
CREATE INDEX chat_messages_by_conversation_p
    ON chat_messages_partitioned (conversation_id, created_at);

INSERT INTO chat_messages_partitioned (id, conversation_id, role, content, created_at, updated_at)
SELECT id, conversation_id, role, content, created_at, updated_at FROM chat_messages;

DROP TABLE chat_messages;
ALTER TABLE chat_messages_partitioned RENAME TO chat_messages;
ALTER INDEX chat_messages_by_conversation_p RENAME TO chat_messages_by_conversation;

COMMIT;
