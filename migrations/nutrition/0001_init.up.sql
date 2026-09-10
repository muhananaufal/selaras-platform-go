-- The nutrition-svc schema.
--
-- Two tables. The first, culinary_preferences, is the "expand" half of an
-- expand-contract split: in the legacy system culinary preferences rode along
-- as ONE JSON COLUMN in user_profiles, so an aggregate with nothing to do with
-- identity or demographics was locked every time a profile was touched, and
-- not a single database constraint guarded its content. Here it becomes a
-- table with real columns and real constraints.

CREATE TABLE culinary_preferences (
    -- UUIDv7, uniform across the whole platform (E16).
    id UUID PRIMARY KEY,

    -- The owner is the USER, not their profile (ADR-024).
    --
    -- UNIQUE: one user has one set of preferences. In the legacy system that
    -- uniqueness came for free because it was a column; once split out it has
    -- to be declared, otherwise two rows for one person will appear and nobody
    -- will know which one applies.
    user_id UUID NOT NULL UNIQUE,

    -- Free text: allergies cannot be enumerated, and trying to only makes
    -- users with an allergy not on the list lie.
    allergies TEXT,

    -- NULL means "not chosen yet", and that differs from any value. The
    -- constraint is enforced here, not only in Go: a TEXT column without a
    -- CHECK accepts anything that gets through one forgotten write path, and
    -- that value stays forever.
    budget_level  TEXT CHECK (budget_level  IN ('thrifty', 'standard', 'flexible')),
    cooking_style TEXT CHECK (cooking_style IN ('quick_every_time', 'batch_meal_prep')),

    -- A native PostgreSQL array, not JSON.
    --
    -- The content is a list of short strings with no structure inside, and an
    -- array can be indexed and queried without unpacking a document. DEFAULT
    -- '{}' with NOT NULL: "never filled in" and "filled in empty" are
    -- deliberately made the same here - both mean no preference - so readers
    -- need not handle NULL and an empty array separately.
    taste_profiles    TEXT[] NOT NULL DEFAULT '{}',
    kitchen_equipment TEXT[] NOT NULL DEFAULT '{}',

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE daily_meal_guides (
    id UUID PRIMARY KEY,

    user_id UUID NOT NULL,

    -- The guide date, in the server's time zone. DATE, not TIMESTAMPTZ: the
    -- question is "the guide for which day", and the creation time is already
    -- in created_at.
    guide_date DATE NOT NULL,

    -- The meal time is determined from the server clock when the guide is
    -- REQUESTED (D10), then FROZEN here.
    --
    -- Recomputing it when the guide is read would make a breakfast suggestion
    -- show up as a dinner suggestion just because the user opened the app
    -- again in the evening. What is stored is the context at the time it was
    -- created.
    meal_time TEXT NOT NULL
        CHECK (meal_time IN ('breakfast', 'lunch', 'afternoon_snack', 'dinner')),

    -- Generation is ASYNCHRONOUS, unlike the legacy system.
    --
    -- In the legacy system the Gemini call happened inside the HTTP request
    -- with a 180-second timeout (B14), so this row only ever existed in the
    -- finished state. Here the row is written first in the pending state, and
    -- the worker fills it in later.
    status TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'ready', 'failed')),

    -- The daily input plus the context assembled when the request was made.
    -- Stored so a suggestion can be explained again later: without it, "why was
    -- I suggested this" has no answer.
    generation_context JSONB NOT NULL,

    -- NULL until the guide arrives.
    guide_data JSONB,

    -- The status invariant is enforced STRUCTURALLY, not by discipline.
    --
    -- A ready guide without content would show up as an empty page claiming to
    -- be done; a pending guide whose content already exists means a writer
    -- forgot to move its status. Both are impossible here.
    CONSTRAINT daily_meal_guides_ready_has_data
        CHECK ((status = 'ready') = (guide_data IS NOT NULL)),

    -- Marked by the user as a menu they actually chose.
    --
    -- This column existed in the legacy system too, but NOT ONE line of code
    -- ever wrote it (B17). Here it is really used as the filter of the
    -- learning history, and until something marks it, that history is indeed
    -- empty - far more honest than feeding the model's own suggestions back to
    -- the model as if the user liked them.
    chosen BOOLEAN NOT NULL DEFAULT FALSE,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The hub history is read per user, newest first. created_at joins as a
-- tie-breaker: several guides in one day share the same guide_date, and
-- without a second column PostgreSQL orders them however it likes - so the
-- second page can repeat rows that already appeared on the first.
CREATE INDEX daily_meal_guides_by_user
    ON daily_meal_guides (user_id, guide_date DESC, created_at DESC);

-- The learning history reads only what is ready AND chosen. A partial index:
-- it holds only the rows actually asked about, and stays small even as the
-- table grows with guides nobody ever chose.
CREATE INDEX daily_meal_guides_chosen
    ON daily_meal_guides (user_id, created_at DESC)
    WHERE chosen AND status = 'ready';
