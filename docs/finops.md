# FinOps — biaya per seribu permintaan, dihitung dari resource nyata

F9-16. Dua sumber angka, dan keduanya disebut supaya bisa diperiksa:

- **Resource**: `docker stats` setiap 5 detik selama dataran skenario k6
  baca (20 VU, 105 detik, 8.135 permintaan, 76,8/detik), stack compose
  lokal, 2026-09-07. Rinciannya di `docs/performance-report.md`.
- **Harga**: halaman harga publik yang dibuka pada sesi yang sama —
  DigitalOcean Droplet (`digitalocean.com/pricing/droplets`) untuk komputasi,
  Gemini API (`ai.google.dev/gemini-api/docs/pricing`) untuk LLM. Harga
  berubah; tanggalnya yang menjadi bagian dari angka.

Yang TIDAK ada di sini: harga penyedia lain, transfer data, penyimpanan
objek untuk backup — tidak diukur, tidak dikarang.

## 1. Resource per permintaan

Rata-rata CPU (persen dari satu core) dan RSS maksimum per container selama
dataran beban, 12 sampel:

| Container | CPU | RSS maks | Bergantung beban? |
| :--- | ---: | ---: | :--- |
| edge-gateway | 13,85 % | 30 MiB | ya |
| assessment-svc | 4,14 % | 27 MiB | ya |
| dashboard-svc | 2,95 % | 29 MiB | ya |
| profile-svc | 2,48 % | 20 MiB | ya |
| nutrition-svc | 2,33 % | 23 MiB | ya |
| identity-svc | 0,25 % | 151 MiB | ya (argon2id saat login/daftar) |
| chat-svc / coaching-svc / llm-worker | 0,2 % masing-masing | 16–18 MiB | tidak pada skenario ini |
| **Sembilan unit** | **26,6 %** | **~330 MiB** | |
| postgres + pgbouncer | 9,2 % | 129 MiB | ya |
| kafka | 17,9 % | 525 MiB | **tidak** — idle KRaft |
| redis, mailpit | 2,4 % | 44 MiB | sedikit |
| observability (collector, Tempo, Prometheus, Loki, Alloy, Grafana) | 7,2 % | ~1,1 GiB | sedikit (sampling 100 %) |

Turunannya:

| | Nilai | Cara hitung |
| :--- | ---: | :--- |
| CPU sembilan unit per permintaan | **3,5 ms core** | 0,266 core ÷ 76,8 req/s |
| CPU basis data per permintaan | 1,2 ms core | 0,092 core ÷ 76,8 |
| **CPU per 1.000 permintaan (unit + basis data)** | **4,7 detik core** | |
| Memori dasar sembilan unit, tanpa beban | ~330 MiB | RSS maks; identity mendominasi karena argon2id |
| Memori tetap dependensi (Kafka, Postgres, Redis, PgBouncer, Mailpit) | ~700 MiB | |

Pembacaan yang jujur: **biaya marginal per permintaan hampir nol**. Yang
membeli mesin adalah biaya TETAP — Kafka memakai 18 % core dan 525 MiB tanpa
melakukan apa pun — dan pekerjaan LLM, yang dibayar per token, bukan per
core.

## 2. Biaya komputasi

Harga [fakta: DigitalOcean, dibuka 2026-09-07], Basic Droplet CPU reguler:

| Mesin | Bulanan | Per jam |
| :--- | ---: | ---: |
| 2 vCPU / 4 GiB | $24 | $0,03571 |
| 4 vCPU / 8 GiB | $48 | $0,07143 |
| 8 vCPU / 16 GiB | $96 | $0,14286 |

Mesin terkecil yang memuat seluruh sistem (unit + dependensi + observability
≈ 2,2 GiB RSS, ~60 % dari satu core pada 77 req/s) adalah **4 vCPU / 8 GiB,
$48/bulan** — sama persis dengan bentuk laptop pengembangan ini, yang
memang menjalankannya.

| Ukuran | Nilai |
| :--- | ---: |
| Harga satu core-detik | $0,07143 ÷ 3.600 ÷ 4 = **$4,96 × 10⁻⁶** |
| Biaya CPU marginal per 1.000 permintaan | 4,7 core-detik × $4,96 × 10⁻⁶ = **$0,000023** |
| Biaya tetap per 1.000 permintaan pada 77 req/s terus-menerus (6,6 juta/hari) | $48 ÷ (6,6 juta × 30 ÷ 1.000) = **$0,00024** |
| Biaya tetap per 1.000 permintaan pada 1 req/s (86 ribu/hari) | $48 ÷ 2.590 = **$0,019** |

Tiga angka itu menceritakan satu hal: pada beban rendah, biaya per seribu
permintaan adalah biaya mesin yang menganggur dibagi sedikit permintaan;
pada beban tinggi ia mendekati nol. Skala yang menentukan biaya bukanlah
permintaan HTTP — melainkan seberapa banyak mesin yang harus tetap menyala.

## 3. Biaya LLM — yang sebenarnya mahal

Setiap personalisasi, kurikulum, laporan kelulusan, balasan thread, balasan
chat, dan panduan menu adalah satu panggilan Gemini. Harga
[fakta: ai.google.dev, dibuka 2026-09-07], per 1 juta token, tingkat berbayar:

| Model | Masukan (teks) | Keluaran |
| :--- | ---: | ---: |
| Gemini 2.5 Flash-Lite | $0,10 | $0,40 |
| Gemini 2.5 Flash | $0,30 | $2,50 |
| Gemini 2.5 Pro (≤ 200k token) | $1,25 | $10,00 |

Ukuran prompt dari templat di `internal/llm/prompt/templates/`
(byte, sebelum data pengguna disisipkan):

| Pekerjaan | Templat | Perkiraan token masukan¹ | Perkiraan token keluaran¹ |
| :--- | ---: | ---: | ---: |
| personalization.v1 | 4.619 B | ~1.500 | ~1.500 (laporan JSON) |
| curriculum.v1 | 2.742 B | ~1.000 | ~2.000 (kurikulum mingguan) |
| daily_guide.v1 | 2.235 B | ~800 | ~800 |
| graduation.v1 | 1.754 B | ~700 | ~600 |
| chat_reply.v1 | 1.396 B | ~600 + jendela 20 pesan | ~200 |

¹ [inferensi] 4 byte ≈ 1 token untuk teks Indonesia; data pengguna yang
disisipkan menambah 20–50 %. Angka ini BELUM diukur terhadap tokenizer
Gemini — penyedia `fake` tidak menghitung token, dan itu satu-satunya yang
pernah dijalankan di sini. Metrik `llm_job_duration_seconds` ada; metrik
token masuk/keluar per job adalah tambahan yang dibutuhkan sebelum angka ini
boleh dipakai untuk menagih siapa pun.

Biaya per pekerjaan dengan perkiraan di atas, Gemini 2.5 Flash:

| Pekerjaan | Biaya per 1 pekerjaan | Per 1.000 pekerjaan |
| :--- | ---: | ---: |
| Personalisasi (1.500 masuk + 1.500 keluar) | $0,00420 | **$4,20** |
| Kurikulum (1.000 + 2.000) | $0,00530 | $5,30 |
| Panduan menu (800 + 800) | $0,00224 | $2,24 |
| Balasan chat (1.000 + 200) | $0,00080 | $0,80 |

Dibandingkan $0,00024 per 1.000 permintaan HTTP: **satu personalisasi
berharga sekitar 17.000 permintaan HTTP.** Inilah alasan jalur LLM dibatasi
per pengguna (10/menit, `docs/runbook/rate-limits.md`) sementara jalur baca
tidak — dan alasan job yang `dead` setelah tiga percobaan berhenti, bukan
mencoba selamanya.

Dengan Flash-Lite biayanya turun ~6×; dengan Pro naik ~4×. Pilihan modelnya
adalah `GEMINI_MODEL`, satu variabel, tanpa nilai bawaan di kode (F3-16).

## 4. Retensi — yang dibayar per byte

Kebijakan retensi ditegakkan `cmd/partitions` (F9-29) dan konfigurasi
broker; angkanya adalah KEBIJAKAN, dan alasannya disebut:

| Data | Retensi | Alasan | Mekanisme |
| :--- | ---: | :--- | :--- |
| `outbox` (8 skema), baris terkirim | 7 hari | menjawab "apakah event X pernah terbit" | partisi bulanan dilepas; sisa di DEFAULT dipangkas |
| `outbox`, baris belum terkirim | selamanya | relay masih membutuhkannya | tidak disentuh |
| `llm_jobs`, selesai/mati | 90 hari | keluhan "hasil saya salah" datang berminggu-minggu kemudian | partisi bulanan dilepas |
| `chat_messages`, `coaching_messages` | **selamanya** | riwayat pengguna; menghapusnya keputusan produk | dipartisi bulanan untuk pemeliharaan, tidak pernah dilepas |
| Topic Kafka (`llm.jobs`, `llm.results`, `user.deletion`, …) | bawaan broker (7 hari) | outbox adalah sumber kebenaran; topic hanya jalur | `retention.ms` bawaan; belum diubah, dan dinyatakan begitu |
| Trace (Tempo) | bawaan 3.x (14 hari) | pengembangan lokal | `block_retention` bawaan |
| Log (Loki) | 7 hari | | `retention_period: 168h` |
| Metrik (Prometheus) | 7 hari compose, 2 hari k3d | | `--storage.tsdb.retention.time` |
| Backup Postgres | 14 hari | | `BACKUP_KEEP` |

Ukuran hari ini: arsip backup 892 KB, seluruh basis data pengembangan.
Pertumbuhan yang tak terbatas hanya ada di dua tabel pesan, dan itu
keputusan yang disengaja.

## 5. Yang akan mengubah angka-angka ini

- **Replika.** HPA (F9-21) menggandakan unit HTTP saat CPU > 60 %; setiap
  replika unit Go ~15–30 MiB. identity-svc ~150 MiB per replika karena
  argon2id — itu unit termahal untuk direplikasi, dan bukan karena Postgres.
- **llm-worker ke nol** (F9-23) menghemat 18 MiB dan satu koneksi Kafka saat
  idle — nyaris tidak ada. Nilainya di cloud adalah bila worker memakai
  node terpisah yang bisa dilepas, bukan di RSS-nya.
- **Sampling trace** 100 % adalah ~7 % CPU observability pada beban ini;
  di produksi, `OTEL_TRACES_SAMPLER=parentbased_traceidratio` dengan rasio
  yang diukur.
- **Kafka** adalah biaya tetap terbesar tanpa beban (525 MiB, 18 % core).
  ADR-003 mempertahankannya; angka ini adalah harga keputusan itu, dan
  dinyatakan supaya keputusan itu bisa ditinjau bersama datanya.
