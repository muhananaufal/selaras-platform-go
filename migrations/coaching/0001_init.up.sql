-- Skema coaching-svc.
--
-- Lima tabel, sama seperti sistem lama. Yang berubah adalah tipe kuncinya,
-- pemilik programnya, dan bagaimana aturan domain ditegakkan - masing-masing
-- dijelaskan di tempatnya.

-- Program coaching.
CREATE TABLE coaching_programs (
    -- UUIDv7 di SELURUH tabel, bukan bigint auto-increment seperti sistem lama.
    --
    -- Sistem lama memakai bigint di mana-mana KECUALI coaching_tasks.id yang
    -- UUID - temuan E16, "id yang tidak seragam". Klien yang memperlakukan satu
    -- id sebagai angka dan yang lain sebagai string akan salah pada salah
    -- satunya. Seragam UUID menutup itu, dan ADR-005 sudah menetapkan seluruh
    -- id publik bertipe string.
    id UUID PRIMARY KEY,

    -- Pemiliknya user_id, BUKAN user_profile_id seperti sistem lama.
    --
    -- Ini identitas yang sudah terverifikasi di setiap permintaan (ADR-023),
    -- dan memakainya menghilangkan langkah penerjemahan yang tidak menambah
    -- apa pun. Ia juga menutup separuh temuan S9: sistem lama memakai dua pola
    -- identitas - CoachingController membandingkan profile->id sementara
    -- ChatController membandingkan user_id - dan dua pola berarti dua tempat
    -- untuk keliru.
    user_id UUID NOT NULL,

    -- Slug publik. Klien tidak pernah melihat id internalnya.
    slug TEXT NOT NULL UNIQUE,

    -- Analisis yang memicu program ini.
    --
    -- TIDAK ada foreign key ke skema assessment (ADR-006, ADR-004 kopling #2).
    -- FK lintas skema akan mengembalikan kopling yang justru dihilangkan
    -- pemisahan service: satu migrasi di assessment akan menahan kunci di
    -- coaching. Keunikannya ditegakkan di sini, dan cuplikan datanya disimpan
    -- supaya program tetap bisa dijelaskan meski penilaiannya hilang.
    risk_assessment_id UUID,

    -- Cuplikan penilaian saat program dimulai.
    --
    -- Ia sengaja disalin, bukan dirujuk. Penilaian bisa berubah atau dihapus,
    -- dan program yang menjelaskan dirinya dengan angka yang sudah berubah
    -- akan membingungkan orang yang membacanya setahun kemudian.
    assessment_snapshot JSONB,

    title       TEXT NOT NULL,
    description TEXT NOT NULL,

    status TEXT NOT NULL DEFAULT 'active',

    -- Tiga nilai dalam Bahasa Indonesia, dipertahankan PERSIS.
    --
    -- Ia bukan istilah internal: klien mengirimkannya apa adanya dan
    -- menampilkannya apa adanya. Menerjemahkannya akan memecahkan klien yang
    -- ada tanpa memperbaiki apa pun.
    difficulty TEXT NOT NULL,

    -- start_date dan end_date adalah SATU-SATUNYA sumber kebenaran akhir
    -- program (F4-18, temuan B5).
    --
    -- Sistem lama menyimpan end_date tetapi penyelesainya memakai
    -- created_at + 28 hari. Dua sumber kebenaran untuk satu fakta berarti
    -- salah satunya salah, dan yang salah adalah yang tidak dilihat siapa pun.
    start_date DATE NOT NULL,
    end_date   DATE NOT NULL,

    -- Laporan kelulusan, diisi belakangan oleh llm-worker.
    graduation_report JSONB,

    -- Keadaan pembuatan laporan kelulusan, dengan alasan yang sama seperti
    -- personalization_status di assessment: yang diturunkan dari ada tidaknya
    -- laporan tidak bisa menyatakan "gagal".
    graduation_status TEXT NOT NULL DEFAULT 'not_requested',
    graduation_error  TEXT,

    -- Keadaan pembuatan kurikulum. Program yang baru dibuat belum punya week
    -- dan task sama sekali - itu datang dari llm-worker (F4-08).
    curriculum_status TEXT NOT NULL DEFAULT 'pending',
    curriculum_error  TEXT,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT coaching_programs_status_known CHECK (
        status IN ('active', 'paused', 'completed')
    ),
    CONSTRAINT coaching_programs_difficulty_known CHECK (
        difficulty IN ('Santai & Bertahap', 'Standar & Konsisten', 'Intensif & Menantang')
    ),
    CONSTRAINT coaching_programs_graduation_status_known CHECK (
        graduation_status IN ('not_requested', 'pending', 'completed', 'failed')
    ),
    CONSTRAINT coaching_programs_curriculum_status_known CHECK (
        curriculum_status IN ('pending', 'completed', 'failed')
    ),

    -- end_date SELALU setelah start_date. Program yang berakhir sebelum
    -- dimulai bukan program; membiarkannya berarti setiap perhitungan sisa
    -- hari menghasilkan angka negatif yang harus ditangani di setiap pembaca.
    CONSTRAINT coaching_programs_ends_after_it_starts CHECK (end_date > start_date)
);

-- D2: satu program AKTIF per pengguna.
--
-- Ditegakkan basis data, bukan hanya kode. Sistem lama memeriksa lalu
-- membatalkan yang lama, dan dua permintaan serempak sama-sama melihat "tidak
-- ada yang aktif" lalu sama-sama membuat satu - meninggalkan dua program aktif
-- yang tidak seharusnya ada.
CREATE UNIQUE INDEX coaching_programs_one_active_per_user
    ON coaching_programs (user_id)
    WHERE status = 'active';

-- D3: satu program per hasil analisis.
--
-- Parsial karena risk_assessment_id boleh NULL: program bisa dimulai tanpa
-- penilaian. Indeks unik biasa akan memperlakukan setiap NULL sebagai berbeda
-- di PostgreSQL, jadi ia tetap bekerja - tetapi menyatakannya parsial membuat
-- niatnya terbaca alih-alih bergantung pada perilaku NULL yang tidak semua
-- orang ingat.
CREATE UNIQUE INDEX coaching_programs_one_per_assessment
    ON coaching_programs (risk_assessment_id)
    WHERE risk_assessment_id IS NOT NULL;

CREATE INDEX coaching_programs_by_user ON coaching_programs (user_id, created_at DESC);

-- Pekan dalam program.
CREATE TABLE coaching_weeks (
    id UUID PRIMARY KEY,

    -- FK di DALAM skema ini dipertahankan: ia tidak melintasi batas service,
    -- jadi tidak ada kopling yang dikembalikan. Yang dilarang ADR-006 adalah
    -- FK LINTAS skema.
    coaching_program_id UUID NOT NULL
        REFERENCES coaching_programs (id) ON DELETE CASCADE,

    -- Positif, dan ditegakkan di sini. Pekan ke-0 atau negatif akan mengurutkan
    -- kurikulum dengan cara yang tidak masuk akal.
    week_number SMALLINT NOT NULL,

    title       TEXT NOT NULL,
    description TEXT NOT NULL,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT coaching_weeks_number_is_positive CHECK (week_number > 0),

    -- Satu pekan bernomor sama tidak boleh ada dua kali dalam satu program.
    -- Tanpa ini, konsumen kurikulum yang berjalan dua kali akan menggandakan
    -- seluruh isinya.
    CONSTRAINT coaching_weeks_unique_per_program UNIQUE (coaching_program_id, week_number)
);

-- Tugas harian.
CREATE TABLE coaching_tasks (
    -- UUID, seperti di sistem lama - satu-satunya tabel yang memang sudah
    -- memakainya.
    id UUID PRIMARY KEY,

    coaching_week_id UUID NOT NULL
        REFERENCES coaching_weeks (id) ON DELETE CASCADE,

    task_date DATE NOT NULL,
    task_type TEXT NOT NULL,

    title       TEXT NOT NULL,
    description TEXT NOT NULL,

    is_completed BOOLEAN NOT NULL DEFAULT FALSE,

    -- Kapan tugasnya diselesaikan. NULL berarti belum.
    --
    -- Ia bukan duplikasi is_completed: yang satu menjawab "sudah?", yang lain
    -- "kapan?" - dan yang kedua diperlukan laporan kelulusan.
    completed_at TIMESTAMPTZ,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT coaching_tasks_type_known CHECK (
        task_type IN ('main_mission', 'bonus_challenge')
    ),

    -- Kedua kolom penyelesaian harus sepakat. Baris yang is_completed tetapi
    -- completed_at NULL akan membuat laporan kelulusan menghitung tugas yang
    -- tidak punya tanggal.
    CONSTRAINT coaching_tasks_completion_is_consistent CHECK (
        (is_completed AND completed_at IS NOT NULL)
        OR (NOT is_completed AND completed_at IS NULL)
    )
);

CREATE INDEX coaching_tasks_by_week ON coaching_tasks (coaching_week_id, task_date);

-- Thread diskusi.
CREATE TABLE coaching_threads (
    id UUID PRIMARY KEY,

    coaching_program_id UUID NOT NULL
        REFERENCES coaching_programs (id) ON DELETE CASCADE,

    slug TEXT NOT NULL UNIQUE,

    -- Bawaannya dipertahankan dari sistem lama. D12 menjelaskan kapan ia
    -- diganti judul turunan dari pesan pertama.
    title TEXT NOT NULL DEFAULT 'Diskusi Program',

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX coaching_threads_by_program ON coaching_threads (coaching_program_id, created_at DESC);

-- Pesan dalam thread.
CREATE TABLE coaching_messages (
    id UUID PRIMARY KEY,

    coaching_thread_id UUID NOT NULL
        REFERENCES coaching_threads (id) ON DELETE CASCADE,

    -- Hanya dua peran, ditegakkan basis data. Peran ketiga yang menyelinap
    -- masuk akan dikirim ke penyedia LLM sebagai peran yang tidak dikenalnya.
    role TEXT NOT NULL,

    content JSONB NOT NULL,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT coaching_messages_role_known CHECK (role IN ('user', 'model'))
);

-- Percakapan dibaca berurutan waktu, dari yang paling lama.
CREATE INDEX coaching_messages_by_thread ON coaching_messages (coaching_thread_id, created_at);
