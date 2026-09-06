# Runbook on-call — edge-gateway

Satu-satunya pintu masuk publik. Ia tidak menyimpan apa pun: setiap permintaan
diteruskan ke satu service lewat gRPC, dan yang ia pegang sendiri hanyalah
kunci PUBLIK token (ADR-020), pembatas laju di Redis, dan cache pencabutan.

## Siapa yang terdampak bila ia mati

**Semua orang.** Tidak ada jalur lain ke sistem. Tetapi pekerjaan yang sudah
diantre — personalisasi, kurikulum, panduan menu — tetap dikerjakan worker
dan hasilnya menunggu; yang hilang hanya kemampuan bertanya.

## Gejala dan cara membacanya

| Gejala | Yang hampir pasti terjadi | Periksa |
| :--- | :--- | :--- |
| Semua permintaan 502/503 dari proxy di depannya | proses mati atau `readyz` 503 | `curl :8081/readyz`; log start-up: variabel wajib yang kosong menolak start (ADR-016) |
| 401 untuk semua orang, termasuk yang baru login | kunci verifikasi tidak cocok dengan kunci tanda tangan identity | `JWT_VERIFY_KEY` vs `JWT_SIGNING_KEY` — keduanya dari `task keygen` yang sama |
| 401 hanya untuk sebagian orang setelah mereka logout/hapus akun | ini benar: pencabutan bekerja (ADR-020) | tidak ada yang perlu dilakukan |
| 503 dengan `code: "UNAVAILABLE"` pada satu kelompok rute saja | satu service di belakangnya mati | rute → service: lihat tabel di bawah; runbook service itu |
| 429 mendadak untuk pengguna sah | batas laju terlalu ketat untuk lingkungan ini, atau `X-Forwarded-For` tidak dipercaya sehingga semua orang berbagi satu IP | `docs/runbook/rate-limits.md`; `SetTrustedProxies` |
| Semua permintaan lolos pembatasan laju + log `rate limiting is unavailable` | Redis mati; pembatasan gagal-terbuka dengan sengaja | runbook Redis; pembatasan pulih sendiri saat Redis kembali |
| Semua permintaan terproteksi 503 + log `revocation check unavailable` | Redis mati; pemeriksaan pencabutan gagal-TERTUTUP dengan sengaja (ADR-020) | Redis dulu — ini yang membuat Redis menjadi dependensi keras gateway |
| 413 untuk unggahan yang sah | badan > 1 MiB (`middleware.MaxBodyBytes`) | tidak ada endpoint yang butuh lebih; bila ada, itu perubahan kontrak |

Rute → service yang dipanggil:

| Rute | Service |
| :--- | :--- |
| `/register`, `/login`, `/logout`, `/password-reset/*`, `/delete-account`, `/auth/*` | identity-svc (register juga → profile-svc) |
| `/profile`, `/me` | profile-svc (region via assessment-svc) |
| `/risk-assessments*` | assessment-svc |
| `/coaching/*` | coaching-svc |
| `/chat/*` | chat-svc |
| `/culinary/*` | nutrition-svc |
| `/dashboard` | dashboard-svc |

## Triase dalam lima menit

1. `curl -s :8081/readyz` — 200 berarti prosesnya sehat; masalahnya di
   belakang atau di depan.
2. Grafana → dashboard *Selaras* → panel "Tingkat galat HTTP per rute". Satu
   rute merah = satu service; semua rute merah = gateway, Redis, atau jaringan.
3. Ambil satu permintaan gagal dari log (`docker logs selaras-edge`), salin
   `trace_id`, buka di Tempo. Span yang bergalat menunjuk unitnya.
4. Log gateway TIDAK memuat data pribadi; yang ada hanya rute, kode, dan id.

## Cara pulih

- **Proses mati**: `docker compose up -d edge-gateway` (k8s: pod dijadwalkan
  ulang sendiri). Ia menyala tanpa menunggu service lain — koneksi gRPC
  dibuka malas — jadi tidak ada urutan yang perlu dijaga.
- **Redis mati**: pulihkan Redis. Gateway tidak perlu di-restart; koneksi
  disambung ulang sendiri.
- **Kunci tidak cocok**: pasang pasangan kunci yang benar, restart gateway
  saja. Token yang ada tetap sah selama kunci tanda tangannya tidak berubah.

## Yang JANGAN dilakukan

- Jangan mematikan pemeriksaan pencabutan untuk "meredakan" 503 saat Redis
  mati. Itu membuka setiap token yang sudah dicabut (ADR-020).
- Jangan menaikkan batas laju di produksi untuk meredakan 429 tanpa melihat
  siapa yang ditolak; 429 pada `/login` dari satu IP adalah tanda percobaan
  menebak kata sandi, bukan bug.

## Metrik yang membuktikan pulih

`http_server_request_duration_seconds` di `:8081/metrics`: tingkat 5xx kembali
nol dan p95 kembali ke kisaran laporan kinerja (< 25 ms untuk rute baca).
