# Latihan pemulihan dari nol

F9-31, ADR-016, kriteria selesai #14: *backup dipulihkan, bukan
diasumsikan*. Pemulihan yang tidak pernah dicoba tidak dihitung sebagai
backup.

Skrip: `DRILL=yes bash test/drill/restore.sh` (dari WSL), lalu
`task test:e2e` (dari Windows). Skrip menolak berjalan tanpa `DRILL=yes`
karena ia menghapus basis data lokal seluruhnya.

## Yang dilakukan, langkah demi langkah

1. Memilih arsip terakhir di volume `selaras-core_backups` (dump + globals)
   SEBELUM apa pun disentuh.
2. Mencatat isi sekarang: jumlah pengguna dan penilaian.
3. Menghentikan seluruh unit, PgBouncer, dan container backup — semua yang
   memegang koneksi.
4. `pg_terminate_backend` untuk sisa koneksi, lalu **`DROP DATABASE`**.
5. `CREATE DATABASE`, `psql -f globals-*.sql` (peran sudah ada; galat
   "already exists" diabaikan dengan sengaja), `pg_restore --no-owner
   --exit-on-error`.
6. Mengembalikan kepemilikan skema dan tabel ke `svc_<skema>` — `--no-owner`
   membuat semuanya milik admin, dan ADR-006 menuntut peran per-service
   memiliki skemanya. Tanpa langkah ini, pemelihara partisi dan migrasi
   berikutnya gagal dengan "must be owner".
7. Menghitung ulang isinya, menyalakan semuanya, menunggu `readyz` gateway.

## Hasil 2026-09-07

```
04:48:40  restoring from /backups/selaras-20260906T214812Z.dump (+ /backups/globals-20260906T214812Z.sql)
04:48:40  before: 40 users, 1671 assessments
04:48:40  stopping units and pgbouncer
04:48:51  stopped at +13s
04:48:52  database dropped at +14s
04:48:57  restored at +19s
04:48:58  ownership restored at +20s
04:48:59  after: 40 users, 1671 assessments
04:49:07  edge ready at +29s
04:49:07  total: 29s from stop to ready
```

Lalu suite e2e (chat, coaching, dashboard, nutrition, outbox, penghapusan
akun — seluruh alur lintas unit) terhadap basis data yang dipulihkan:

```
ok  	github.com/muhananaufal/selaras-platform-go/test/e2e	28.498s
```

| Ukuran | Nilai |
| :--- | ---: |
| Waktu pemulihan (stop → gateway siap) | **29 detik** |
| Di antaranya: menghentikan unit | 13 s |
| pg_restore, 892 KB | 5 s |
| Isi sebelum = sesudah | 40 pengguna, 1.671 penilaian ✅ |
| Test e2e sesudahnya | hijau, 28,5 s ✅ |

## Yang ditemukan karena mencobanya

Larian pertama **gagal** — dan itu alasan latihan ini ada:

- Skrip menghitung isi basis data lewat koneksi ke basis data `postgres`
  (yang dipakai untuk `DROP`/`CREATE`), bukan ke `selaras`; hitungan
  "sebelum" menjadi `unknown` dan hitungan "sesudah" gagal dengan
  `relation "identity.users" does not exist` — **setelah** pemulihan
  sebenarnya berhasil. Karena `set -e`, skrip berhenti di situ dan **tidak
  menyalakan unit kembali**. Diperbaiki dengan dua helper (`psql_admin`,
  `psql_app`); larian kedua di atas bersih.
- `pg_restore --no-owner` diperlukan (peran yang memiliki objek di arsip
  tidak boleh menjadi syarat pemulihan), dan itu berarti kepemilikan HARUS
  dikembalikan sesudahnya. Tanpa langkah 6, pemulihan "berhasil" tetapi
  sistemnya rusak pada migrasi berikutnya — kegagalan yang baru terlihat
  minggu depan.

Dua hal yang tidak akan pernah terlihat dari membaca skripnya.

## Batas yang jujur

- Arsipnya 892 KB. Angka 29 detik adalah angka basis data kecil; yang
  berskala bersama data adalah langkah 5, dan ia satu-satunya yang tidak
  bisa diperkirakan dari sini.
- Kafka tidak dipulihkan dan tidak perlu: outbox di Postgres adalah sumber
  kebenaran event, relay mengirim ulang yang belum terkirim, dan proyeksi
  dashboard dibangun ulang dengan `cmd/dashboard-rebuild`. Tetapi konsumen
  memegang OFFSET topic — hasil LLM yang terbit setelah arsip dibuat dan
  sebelum pemulihan sudah "terkonsumsi" menurut broker, sementara barisnya
  di Postgres kembali ke keadaan arsip. Jendela itu (≤ enam jam pada jadwal
  bawaan) adalah kehilangan data yang jujur dari strategi backup ini, dan
  memperpendeknya adalah soal `BACKUP_INTERVAL`, bukan soal skrip.
- Arsip tinggal di daemon Docker yang sama dengan datanya
  (`docs/runbook/backup.md`).
