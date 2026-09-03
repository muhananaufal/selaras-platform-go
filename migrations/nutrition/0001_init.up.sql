-- Skema nutrition-svc.
--
-- Dua tabel. Yang pertama, culinary_preferences, adalah bagian "expand" dari
-- pemisahan expand-contract: di sistem lama preferensi kuliner menumpang
-- sebagai SATU KOLOM JSON di user_profiles, sehingga sebuah agregat yang tidak
-- ada hubungannya dengan identitas atau demografi ikut terkunci setiap kali
-- profil disentuh, dan tidak ada satu pun batasan basis data yang menjaga
-- isinya. Di sini ia menjadi tabel dengan kolom sungguhan dan batasan
-- sungguhan.

CREATE TABLE culinary_preferences (
    -- UUIDv7, seragam dengan seluruh platform (E16).
    id UUID PRIMARY KEY,

    -- Pemiliknya PENGGUNA, bukan profilnya (ADR-024).
    --
    -- UNIQUE: satu pengguna punya satu himpunan preferensi. Di sistem lama
    -- keunikan itu datang gratis karena ia sebuah kolom; setelah dipisah ia
    -- harus dinyatakan, kalau tidak dua baris untuk satu orang akan muncul dan
    -- tidak ada yang tahu mana yang berlaku.
    user_id UUID NOT NULL UNIQUE,

    -- Teks bebas: alergi tidak bisa dienumerasi, dan mencoba melakukannya
    -- hanya membuat pengguna dengan alergi yang tidak ada di daftar berbohong.
    allergies TEXT,

    -- NULL berarti "belum dipilih", dan itu berbeda dari nilai mana pun.
    -- Batasannya ditegakkan di sini, bukan hanya di Go: kolom TEXT tanpa CHECK
    -- akan menerima apa pun yang berhasil melewati satu jalur penulisan yang
    -- terlupakan, dan nilai itu akan bertahan selamanya.
    budget_level  TEXT CHECK (budget_level  IN ('thrifty', 'standard', 'flexible')),
    cooking_style TEXT CHECK (cooking_style IN ('quick_every_time', 'batch_meal_prep')),

    -- Array asli PostgreSQL, bukan JSON.
    --
    -- Isinya daftar string pendek tanpa struktur di dalamnya, dan array bisa
    -- diindeks serta ditanyai tanpa membongkar dokumen. DEFAULT '{}' dengan
    -- NOT NULL: "belum pernah diisi" dan "diisi kosong" sengaja disamakan di
    -- sini - keduanya berarti tidak ada preferensi - sehingga pembacanya tidak
    -- perlu menangani NULL dan array kosong secara terpisah.
    taste_profiles    TEXT[] NOT NULL DEFAULT '{}',
    kitchen_equipment TEXT[] NOT NULL DEFAULT '{}',

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE daily_meal_guides (
    id UUID PRIMARY KEY,

    user_id UUID NOT NULL,

    -- Tanggal panduan, di zona waktu server. DATE, bukan TIMESTAMPTZ: yang
    -- ditanyakan adalah "panduan hari apa", dan jam pembuatannya sudah ada di
    -- created_at.
    guide_date DATE NOT NULL,

    -- Waktu makan ditentukan dari jam server saat panduan DIMINTA (D10), lalu
    -- DIBEKUKAN di sini.
    --
    -- Menghitungnya ulang saat panduan dibaca akan membuat saran sarapan
    -- muncul sebagai saran makan malam hanya karena pengguna membuka aplikasi
    -- lagi malam harinya. Yang disimpan adalah konteks saat ia dibuat.
    meal_time TEXT NOT NULL
        CHECK (meal_time IN ('breakfast', 'lunch', 'afternoon_snack', 'dinner')),

    -- Pembuatannya ASINKRON, berbeda dari sistem lama.
    --
    -- Di sistem lama panggilan Gemini terjadi di dalam permintaan HTTP dengan
    -- timeout 180 detik (B14), sehingga baris ini hanya pernah ada dalam
    -- keadaan sudah jadi. Di sini baris ditulis lebih dulu dalam keadaan
    -- pending, dan worker mengisinya belakangan.
    status TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'ready', 'failed')),

    -- Masukan harian ditambah konteks yang dirakit saat permintaan dibuat.
    -- Disimpan supaya sebuah saran bisa dijelaskan kembali kemudian: tanpa ini,
    -- "mengapa saya disarankan ini" tidak punya jawaban.
    generation_context JSONB NOT NULL,

    -- NULL sampai panduannya tiba.
    guide_data JSONB,

    -- Invarian statusnya ditegakkan STRUKTURAL, bukan dengan disiplin.
    --
    -- Panduan berstatus ready tanpa isi akan tampil sebagai halaman kosong yang
    -- mengaku selesai; panduan pending yang isinya sudah ada berarti ada
    -- penulis yang lupa memindahkan statusnya. Keduanya mustahil di sini.
    CONSTRAINT daily_meal_guides_ready_has_data
        CHECK ((status = 'ready') = (guide_data IS NOT NULL)),

    -- Ditandai pengguna sebagai menu yang benar-benar ia pilih.
    --
    -- Kolom ini ada di sistem lama pula, tetapi TIDAK ADA satu baris kode pun
    -- yang pernah menulisnya (B17). Di sini ia dipakai sungguhan sebagai
    -- saringan riwayat pembelajaran, dan selama belum ada yang menandainya,
    -- riwayat itu memang kosong - jauh lebih jujur daripada menyuapkan kembali
    -- saran model sendiri kepada model seolah pengguna menyukainya.
    chosen BOOLEAN NOT NULL DEFAULT FALSE,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Riwayat hub dibaca per pengguna, terbaru lebih dulu. created_at ikut sebagai
-- pemecah seri: beberapa panduan dalam satu hari punya guide_date yang sama,
-- dan tanpa kolom kedua urutannya ditentukan PostgreSQL sesukanya - sehingga
-- halaman kedua bisa mengulang baris yang sudah muncul di halaman pertama.
CREATE INDEX daily_meal_guides_by_user
    ON daily_meal_guides (user_id, guide_date DESC, created_at DESC);

-- Riwayat pembelajaran hanya membaca yang ready DAN dipilih. Indeks parsial:
-- ia hanya memuat baris yang benar-benar ditanyakan, dan tetap kecil walau
-- tabelnya tumbuh dengan panduan yang tidak pernah dipilih siapa pun.
CREATE INDEX daily_meal_guides_chosen
    ON daily_meal_guides (user_id, created_at DESC)
    WHERE chosen AND status = 'ready';
