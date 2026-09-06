# Runbook on-call — dashboard-svc

Read-model halaman utama (ADR-009): satu tabel per pengguna yang
diproyeksikan dari `assessment.completed`, `coaching.program.updated`, dan
`user.deletion`. Ia TIDAK memanggil unit lain saat dibaca — `GET /dashboard`
adalah satu baris. Nilai terakhir/sebelumnya/total diturunkan dari tabel
riwayat saat dibaca, bukan dari kolom yang dipelihara, supaya event yang
tiba tidak berurutan tidak merusaknya. Bergantung keras pada Postgres
(skema `dashboard`); pada Kafka untuk tetap segar.

## Siapa yang terdampak bila ia mati

- **Mati**: `GET /dashboard` saja.
- **Bertahan**: semua yang lain. Halaman utama klien kehilangan ringkasan,
  bukan datanya — riwayat penilaian dan program masih bisa dibaca dari
  unit pemiliknya.

## Gejala dan cara membacanya

| Gejala | Yang hampir pasti terjadi | Periksa |
| :--- | :--- | :--- |
| Dashboard tidak memuat penilaian yang baru saja selesai | lag proyeksi; wajar di bawah satu detik (F7: 444–920 ms terukur) | lebih dari beberapa detik: `assessment.outbox` tertunda, atau projector tertinggal — `dashboard.projection_state` |
| Dashboard kosong untuk pengguna yang punya data | proyeksi belum pernah berjalan untuk pengguna itu (mis. dashboard-svc dipasang setelah datanya ada) | `cmd/dashboard-rebuild -yes` membaca ulang seluruh riwayat dari topic; hasilnya byte-identik dengan proyeksi langsung (F7 diuji) |
| `previous_risk_percentage` kosong padahal ada dua penilaian | tidak mungkin lagi setelah F7 (diturunkan dari riwayat saat dibaca); bila terjadi, riwayatnya yang hilang | `dashboard.dashboard_assessments` untuk pengguna itu |
| Data pengguna yang sudah dihapus masih tampil | konfirmasi saga penghapusan dari dashboard belum terkirim | `identity.deletion_confirmations` untuk saga itu; runbook penghapusan akun |
| Projector berputar | event yang gagal diproyeksikan ditahan (Rewinder) — periksa galatnya; proyeksi memakai `ON CONFLICT DO NOTHING`, jadi duplikat bukan galat | log `projecting ... failed` |

## Triase dalam lima menit

1. `curl :9703/readyz`.
2. Grafana → gRPC `dashboard.v1.Dashboard/Show`; panel lag hanya untuk
   llm-worker — lag projector dashboard **belum punya metrik** (celah;
   dicatat di RFC penutup). Gantinya: `SELECT max(projected_at) FROM
   dashboard.dashboards` dibandingkan waktu event terakhir.
3. `trace_id` dari `assessment.completed` → span `assessment.completed
   process` di dashboard-svc menunjukkan kapan proyeksinya terjadi.

## Cara pulih

- Nyalakan lagi; projector melanjutkan dari offset.
- Proyeksi yang diragukan: `cmd/dashboard-rebuild -yes` (menuntut flag
  eksplisit karena ia menghapus dan membangun ulang tabel proyeksi). Aman
  diulang; idempoten.

## Yang JANGAN dilakukan

- Jangan "memperbaiki" angka di `dashboards` manual. Sumber kebenarannya
  adalah event; rebuild akan menimpa suntingan itu, dan itu memang yang
  seharusnya.

## Metrik yang membuktikan pulih

`rpc_server_call_duration_seconds{rpc_method="dashboard.v1.Dashboard/Show"}`
OK dengan p95 ~5 ms (laporan kinerja: `GET /dashboard` p95 4,9 ms);
`max(projected_at)` mendekati sekarang.
