-- Tabel outbox untuk profile.
--
-- Isinya identik di setiap service dan berasal dari satu sumber:
-- internal/platform/outbox/schema.sql. Delapan salinan yang ditulis tangan
-- akan menyimpang perlahan, dan perbedaannya baru terlihat saat ada event
-- yang hilang di satu service saja.

-- Tabel outbox. Satu per skema service; isinya identik.
--
-- Ia di-embed dan dipakai generator migrasi supaya tidak ada delapan salinan
-- yang perlahan menyimpang. Satu salinan yang berbeda berarti satu service
-- yang eventnya berperilaku lain, dan perbedaannya baru terlihat saat ada
-- yang hilang.

CREATE TABLE outbox (
    -- UUIDv7: terurut waktu, sehingga relay membacanya dalam urutan yang
    -- sama dengan urutan penulisannya tanpa perlu kolom urutan terpisah.
    id UUID NOT NULL,

    -- created_at ikut kunci primer karena ia kunci partisi. PostgreSQL
    -- mensyaratkannya: tanpa itu, kunci primer tidak bisa ditegakkan lintas
    -- partisi.
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- Agregat yang berubah. Dipakai sebagai kunci partisi Kafka, sehingga
    -- seluruh event satu agregat mendarat di partisi yang sama dan urutannya
    -- terjaga - urutan global tidak dijamin Kafka, urutan per kunci dijamin.
    aggregate_type TEXT NOT NULL,
    aggregate_id   TEXT NOT NULL,

    event_type TEXT NOT NULL,

    -- Envelope protobuf yang sudah diserialkan. BYTEA, bukan JSONB: yang
    -- disimpan adalah bentuk yang akan dikirim apa adanya, sehingga tidak ada
    -- penyandian ulang antara yang tersimpan dan yang terkirim.
    payload BYTEA NOT NULL,

    -- NULL berarti belum terkirim. Baris yang sudah terkirim disimpan
    -- sebentar untuk penyelidikan, lalu disapu bersama partisinya.
    published_at TIMESTAMPTZ,

    -- Berapa kali pengiriman dicoba. Ia ada supaya baris yang selalu gagal
    -- bisa ditemukan, bukan hanya menyumbat antrean diam-diam.
    attempts INT NOT NULL DEFAULT 0,

    -- Galat terakhir. Tanpa ini, satu-satunya cara mengetahui mengapa sebuah
    -- event tidak terkirim adalah membaca log pada saat yang tepat.
    last_error TEXT,

    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);

-- Partisi dipasang SEKARANG, bukan nanti (F3-17).
--
-- Mengubah tabel yang sudah terisi menjadi terpartisi berarti menyalin
-- seluruh isinya sambil menahan kunci - operasi yang di tabel yang tumbuh
-- monoton seperti ini akan memakan waktu yang tidak bisa diterima.
--
-- Partisi bawaan menangkap apa pun yang jatuh di luar rentang yang sudah
-- dibuat. Tanpa ia, INSERT untuk bulan yang partisinya belum ada akan GAGAL -
-- dan kegagalan itu akan menggagalkan transaksi bisnisnya juga.
CREATE TABLE outbox_default PARTITION OF outbox DEFAULT;

-- Relay hanya membaca yang belum terkirim, terurut waktu. Indeks parsial
-- hanya memuat baris itu, sehingga ia tetap kecil walau tabelnya tumbuh.
CREATE INDEX outbox_unpublished ON outbox (created_at) WHERE published_at IS NULL;
