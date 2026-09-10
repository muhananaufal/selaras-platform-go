-- The order is the reverse of the up migration. There is no foreign key
-- between the two (ADR-006 forbids it across schemas, and even inside the
-- schema these two tables do not reference each other), so the order is
-- actually free - it is kept reversed so readers need not check to know
-- that.
DROP TABLE IF EXISTS daily_meal_guides;
DROP TABLE IF EXISTS culinary_preferences;
