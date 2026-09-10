-- Confirmations first: they reference the saga through a foreign key, and
-- the reverse order would be refused by Postgres.
DROP TABLE IF EXISTS deletion_confirmations;
DROP TABLE IF EXISTS deletion_sagas;
