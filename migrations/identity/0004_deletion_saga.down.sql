-- Konfirmasi lebih dulu: ia merujuk ke saga lewat foreign key, dan urutan
-- terbalik akan ditolak Postgres.
DROP TABLE IF EXISTS deletion_confirmations;
DROP TABLE IF EXISTS deletion_sagas;
