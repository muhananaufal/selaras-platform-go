# Runbook — backup Postgres

F9-30. Backup yang tidak pernah dipulihkan bukan backup; latihan
pemulihannya ada di `docs/runbook/restore-drill.md`, dan dokumen ini hanya
tentang MEMBUAT dan MEMERIKSA arsipnya.

Ada dua lapis backup, dan keduanya dipakai untuk hal yang berbeda:

| Lapis | Menjawab | RPO |
| :--- | :--- | :--- |
| `pg_dump` logis (dokumen ini) | salinan portabel: bisa dipulihkan ke versi Postgres lain atau per skema | sampai satu putaran (bawaan 6 jam) |
| pgBackRest fisik + WAL ([`pitr.md`](pitr.md)) | kembali ke **detik** tertentu, misalnya sesaat sebelum `DELETE` yang salah | ≤ 60 detik saat idle |

## Apa yang dibackup, dan apa yang tidak

| Sumber | Dibackup? | Alasan |
| :--- | :--- | :--- |
| Postgres — delapan skema (`identity` … `llm`) | **Ya**, `pg_dump -Fc` seluruh basis data | satu-satunya keadaan yang tidak bisa dibangun ulang dari sumber lain |
| Peran dan kata sandinya (`svc_*`) | **Ya**, `pg_dumpall --globals-only` | tanpa ini, pemulihan menghasilkan skema tanpa pemilik dan unit tidak bisa masuk |
| Kafka | Tidak | outbox di Postgres adalah sumber kebenaran event; topic bisa dibangun ulang dari relay (chaos F9-12), dan proyeksi dashboard dari `cmd/dashboard-rebuild` |
| Redis | Tidak | penghitung generasi token dan pembatas laju boleh hilang menurut ADR-020, dengan harga yang disebut di runbook identity |
| Volume Tempo / Loki / Prometheus | Tidak | telemetri, bukan data pengguna |

## Bagaimana ia berjalan

`deploy/compose/backup.yml` — container `selaras-backup` dari image Postgres
yang **sama** dengan servernya (`postgres:18.6-alpine`), menjalankan
`deploy/compose/backup/backup.sh` dalam putaran setiap `BACKUP_INTERVAL`
detik (bawaan 21600 = enam jam):

1. `pg_dumpall --globals-only` → `globals-<stamp>.sql.tmp`
2. `pg_dump --format=custom --compress=6` → `selaras-<stamp>.dump.tmp`
3. Verifikasi, dua-duanya **wajib**:
   - `pg_restore --list` atas arsip memuat ≥ 8 skema;
   - berkas peran memuat `CREATE ROLE svc_<unit>;` untuk kedelapan unit.
4. Hanya kalau keduanya lolos, kedua berkas diberi nama final. Putaran yang
   gagal di langkah mana pun menghapus berkas `.tmp`-nya dan tercatat
   `FAILED stamp=… : <alasan>` di log.
5. arsip yang lebih tua dari `BACKUP_KEEP` hari (bawaan 14) dibuang

Setiap langkah memeriksa hasilnya sendiri, tanpa mengandalkan `set -e`.
**Sebelum 2026-09-25, klaim "ditulis atomik" di runbook ini tidak benar.**
Dalam mode layanan, `run_once` dipanggil dari `if ! run_once`, dan POSIX sh
mengabaikan `errexit` di dalam fungsi yang dipanggil dari sebuah kondisi.
Akibatnya, setiap kali `pg_dump` gagal (misalnya database dimatikan oleh
drill), `mv` tetap jalan. Volume `selaras-core_backups` sampai menyimpan
**tujuh dump dan lima berkas peran berukuran 0 byte dengan nama final**, dan
drill pemulihan memilih berkas terbaru, yang kosong. Keadaan ini
direproduksi di container sekali pakai, lalu diperbaiki dan dikunci oleh
`test/drill/backup.test.sh` (job CI `backup and restore scripts`).
Berkas 0 byte lama masih ada di volume itu, dan `restore.sh` kini
melewatinya (lihat [`restore-drill.md`](restore-drill.md)).

Dinyalakan bersama `task up:full`. Sekali jalan sekarang: `task backup:now`.
Daftar arsip: `task backup:list`.

## Bukti bahwa ia bekerja (2026-09-07)

```
2026-09-06T21:40:23Z backup starting stamp=20260906T214023Z
2026-09-06T21:40:24Z backup verified stamp=20260906T214023Z schemas=8 bytes=892291 file=/backups/selaras-20260906T214023Z.dump
2026-09-06T21:40:24Z backup done stamp=20260906T214023Z
```

Volume `selaras-core_backups` sesudahnya:

```
globals-20260906T214023Z.sql      3611
selaras-20260906T214023Z.dump   892291
```

Satu detik untuk basis data pengembangan; ukurannya akan tumbuh bersama
`chat_messages` dan `coaching_messages` — dua tabel yang sengaja tidak punya
retensi (F9-29).

## Di mana arsipnya

Volume Docker `selaras-core_backups`, di daemon yang sama dengan Postgres.
Itu menjawab "salah hapus" dan "migrasi yang merusak" — **bukan** "disk
mati" dan **bukan** "mesin hilang". Menyalin arsip ke lokasi lain (bucket
objek, mesin lain) adalah langkah yang BELUM ada, dan dicatat di RFC penutup
sebagai hutang yang diketahui. Tanpa itu, backup ini hanya melindungi dari
separuh kegagalan yang seharusnya ia lindungi.

## Memeriksa bahwa backup semalam berjalan

```
docker logs --since 24h selaras-backup | grep -E 'verified|FAILED'
```

Tidak ada baris `verified` dalam 24 jam = backup tidak berjalan. Baris
`FAILED` = arsipnya tidak lolos verifikasi; arsip `.tmp` tidak pernah
dipromosikan, jadi yang tersisa di volume tetap yang terakhir sah.

## Yang JANGAN dilakukan

- Jangan mengganti `pg_dump` dengan menyalin direktori data Postgres selagi
  server hidup; salinan itu tidak konsisten dan pemulihannya bisa gagal
  dengan cara yang baru terlihat saat dibutuhkan.
- Jangan menaikkan `BACKUP_INTERVAL` "supaya ringan": ia satu detik.
