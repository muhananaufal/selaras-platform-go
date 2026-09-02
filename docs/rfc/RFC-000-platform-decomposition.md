# RFC-000 — Pemecahan Platform

**Status:** diusulkan, menunggu persetujuan Foundation Gate (B1-10)
**Tanggal:** 2026-09-02

## 1. Masalah

`selaras-backend-api` adalah monolit Laravel 12: 32 endpoint, ±8.465 baris, 11 model,
16 migrasi. Ia berfungsi, tetapi memikul tiga hal yang membatasinya.

**Panggilan model berjalan di dalam request.** Tidak ada satu pun `ShouldQueue`, tidak ada
`app/Jobs`, dan `GenerateGraduationReportJob::dispatch` dikomentari mati. Satu permintaan
laporan menahan koneksi HTTP sampai model selesai — dengan batas waktu 300 detik di
`GeminiReportService`.

**Tidak ada test.** `tests/` hanya berisi `ExampleTest` bawaan. Tidak ada jaring pengaman
untuk perubahan apa pun.

**Cache dan invalidasinya tersebar.** Sedikitnya 15 kunci dengan sembilan TTL berbeda,
diinvalidasi manual dari controller, repository, listener, dan trait model sekaligus.

## 2. Sembilan unit

| Unit | Milik | Alasan ia berdiri sendiri |
| :--- | :--- | :--- |
| `edge-gateway` | — | Satu-satunya yang menghadap publik; memegang authn, authz, dan agregasi |
| `identity-svc` | `users`, `access_tokens`, `password_reset_tokens` | Gerbang autentikasi |
| `profile-svc` | `user_profiles` | Hub yang di-FK **seluruh** domain, dan satu-satunya sumber masukan mesin risiko |
| `assessment-svc` | `risk_assessments` | Memuat SCORE2 beserta konstantanya; deterministik dan paling berbahaya bila salah |
| `coaching-svc` | 5 tabel coaching | Domain terberat, ±1.227 baris |
| `chat-svc` | `conversations`, `chat_messages` | Agregat percakapan berdiri sendiri, berbeda dari thread yang selalu milik program |
| `nutrition-svc` | `culinary_preferences`, `daily_meal_guides` | Preferensi dipisah dari profil lewat expand-contract |
| `dashboard-svc` | `dashboard_read_model` | Read-model, bukan pemilik data |
| `llm-worker` | `llm_jobs`, `llm_prompts`, `idempotency_keys` | Menyatukan lima implementasi Gemini yang saling menduplikasi |

## 3. Kopling yang diketahui dan cara menanganinya

| # | Kopling | Penanganan |
| :--- | :--- | :--- |
| 1 | Seluruh tabel domain ber-FK ke `user_profile_id`, bukan `user_id` | `user_profile_id` diangkat jadi klaim token dan header event (ADR-007) |
| 2 | `coaching_programs.risk_assessment_id` — FK lintas domain dengan constraint unique | Referensi lunak plus snapshot dari event; keunikan ditegakkan coaching-svc (ADR-004) |
| 3 | Mesin risiko menerima `UserProfile` di setiap kalkulasi | assessment-svc menyimpan salinan dari event `profile.updated`, tidak memanggil per kalkulasi |
| 4 | Penghapusan akun menyentuh seluruh domain | Saga koreografi dengan verifikasi akhir (ADR-011) |
| 5 | `coaching_tasks.id` UUID, tabel lain bigint | ID publik berbentuk string di seluruh kontrak (ADR-005) |

## 4. Model konsistensi

Transactional outbox. Perubahan bisnis dan baris outbox ditulis dalam **satu** transaksi lokal;
proses relay menerbitkannya ke Kafka. Pengiriman at-least-once, jadi **setiap konsumen wajib
idempoten** — itu bukan saran, melainkan syarat.

Yang sengaja tidak dipakai: two-phase commit (koordinasi global, ketersediaan turun) dan
publikasi langsung setelah commit (bekerja sampai proses mati di antara keduanya).

## 5. Alternatif yang ditolak

| Alternatif | Alasan ditolak |
| :--- | :--- |
| Tetap Laravel dengan stack industri di sekelilingnya | Termurah dan langsung memperbaiki ketiadaan test, Docker, dan job async — tetapi tidak memenuhi tujuan menguasai Go |
| Modular monolith Go satu binary | Paling masuk akal secara rekayasa untuk beban sebesar ini, dan didukung sumber eksternal. Ditolak karena tujuannya eksplisit multi-service; penolakannya dicatat agar terlihat, bukan disembunyikan |
| Strangler fig dengan Laravel tetap sebagai edge | Paling aman untuk sistem hidup; tidak relevan karena tidak ada trafik nyata yang perlu dilindungi |
| Empat unit, bukan sembilan | Beban operasional lebih rendah, tetapi menggabungkan domain yang siklus hidupnya berbeda |

## 6. Yang membuat rencana ini bisa dibatalkan

| Trigger | Aksi |
| :--- | :--- |
| F1 memakan waktu jauh melebihi dugaan | Turunkan jumlah unit; gabungkan chat ke coaching, nutrition ke assessment |
| Golden vector tidak lulus setelah tiga putaran diagnosis | Berhenti. Curigai asumsi presisi, jangan menambal koefisien |
| Kapasitas lokal tidak cukup | Turun ke profil `core`; observability pindah ke fase cloud |
| Ternyata ada pengguna nyata | Klasifikasi ulang total: kewajiban hukum atas data kesehatan muncul |

## 7. Keputusan pendukung

Sembilan belas ADR di [`../adr/`](../adr/), masing-masing dengan konteks, opsi, konsekuensi dua
arah, dan **pembatal** — kondisi yang membuat keputusan itu gugur.

Yang paling menentukan bentuk sistem ini: ADR-002 (topologi), ADR-003 (broker), ADR-004
(konsistensi), ADR-007 (identitas global), ADR-013 (kebijakan port), ADR-016 (bangun seolah
produksi).

## 8. Yang diminta pada gerbang ini

Kontrak di `api/proto/` dan `api/openapi/` dikunci setelah persetujuan. Perubahan yang merusak
konsumen sesudahnya harus datang sebagai versi paket baru, bukan suntingan di tempat — dan
`buf breaking` di CI yang menegakkannya.

Yang paling mahal diubah belakangan, dan karena itu paling layak dipertanyakan sekarang:
**tabel kepemilikan di bagian 2** dan **peta kopling di bagian 3**.
