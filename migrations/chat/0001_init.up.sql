-- The chat-svc schema.
--
-- Two tables, the same as the legacy system's final shape: conversations, and
-- the messages hanging off them. The legacy system got there through two
-- migrations - messages first hung off profiles, then moved to conversations.
-- What is written here is the final shape; the migration history need not come
-- along.

CREATE TABLE conversations (
    -- UUIDv7, uniform across the whole platform (E16).
    id UUID PRIMARY KEY,

    -- The owner is the USER, not their profile (ADR-024).
    --
    -- The legacy system already used user_id in ChatController while
    -- CoachingController used profile->id - two patterns for one question,
    -- half of finding S9. The one chosen here is the correct pattern, and it
    -- happens to be the one chat already used.
    user_id UUID NOT NULL,

    -- The public slug. Clients never see the internal id, and sequential ids
    -- would let anyone walk through other people's conversations just by
    -- counting.
    slug TEXT NOT NULL UNIQUE,

    title TEXT NOT NULL,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The conversation list is always read per user, newest first.
CREATE INDEX conversations_by_user ON conversations (user_id, updated_at DESC);

CREATE TABLE chat_messages (
    id UUID PRIMARY KEY,

    conversation_id UUID NOT NULL
        REFERENCES conversations (id) ON DELETE CASCADE,

    -- Only two roles, enforced by the database. A third role that slipped in
    -- would be sent to the LLM provider as a role it does not recognise.
    role TEXT NOT NULL,

    -- TEXT, not JSONB, following the legacy system.
    --
    -- General conversations store plain text; the structured ones are
    -- coaching threads. Storing it as JSONB would force every old message
    -- to be re-wrapped without anyone reading it as structure.
    content TEXT NOT NULL,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT chat_messages_role_known CHECK (role IN ('user', 'model')),

    -- An empty message is not a message. The upper bound lives in Go - a TEXT
    -- column bounds nothing - but the lower bound is enforced here, because
    -- it does not depend on any configuration.
    CONSTRAINT chat_messages_not_empty CHECK (length(btrim(content)) > 0)
);

-- A conversation is read in time order, oldest first.
CREATE INDEX chat_messages_by_conversation ON chat_messages (conversation_id, created_at);
