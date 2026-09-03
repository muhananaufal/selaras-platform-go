-- Urutannya kebalikan dari naik. Tidak ada foreign key di antara keduanya
-- (ADR-006 melarangnya lintas skema, dan di dalam skema pun kedua tabel ini
-- tidak saling merujuk), jadi urutan sebenarnya bebas - ia dijaga tetap
-- terbalik supaya pembacanya tidak perlu memeriksa untuk tahu itu.
DROP TABLE IF EXISTS daily_meal_guides;
DROP TABLE IF EXISTS culinary_preferences;
