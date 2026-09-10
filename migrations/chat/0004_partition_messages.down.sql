-- Turns chat_messages back into an ordinary table. The contents are copied
-- whole.
BEGIN;

CREATE TABLE chat_messages_plain (
    id UUID PRIMARY KEY,
    conversation_id UUID NOT NULL
        REFERENCES conversations (id) ON DELETE CASCADE,
    role TEXT NOT NULL,
    content TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT chat_messages_role_known CHECK (role IN ('user', 'model')),
    CONSTRAINT chat_messages_not_empty CHECK (length(btrim(content)) > 0)
);

INSERT INTO chat_messages_plain (id, conversation_id, role, content, created_at, updated_at)
SELECT id, conversation_id, role, content, created_at, updated_at FROM chat_messages;

DROP TABLE chat_messages;
ALTER TABLE chat_messages_plain RENAME TO chat_messages;
CREATE INDEX chat_messages_by_conversation ON chat_messages (conversation_id, created_at);

COMMIT;
