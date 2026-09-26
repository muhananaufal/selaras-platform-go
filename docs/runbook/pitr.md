# Runbook — point-in-time recovery (pgBackRest)

`pg_dump` harian ([`backup.md`](backup.md)) hanya bisa mengembalikan keadaan
**pada saat dump**. Semua yang ditulis setelahnya hilang, jadi RPO-nya
sampai satu hari. PITR menutup celah itu. WAL (catatan setiap perubahan)
diarsipkan terus-menerus, sehingga database bisa dikembalikan ke **detik
mana pun** di dalam jendela retensi. Kasus yang ditanganinya misalnya
`DELETE` tanpa `WHERE` pukul 14:03:12, yang bisa dikembalikan ke 14:03:11.

| Bagian | Isi | Di mana |
| :--- | :--- | :--- |
| Image | `postgres:18.6-alpine` + `pgbackrest=2.58.0-r0` | `deploy/compose/postgres/Dockerfile` |
| Arsip WAL | `archive_mode=on`, `archive_timeout=60`, `archive_command=pgbackrest … archive-push %p`; flag-nya ada **di dalam image**, jadi setiap `postgres` dari image ini pasti mengarsipkan | `deploy/compose/postgres/archiving-entrypoint.sh` |
| Konfigurasi | repo di volume `pgbackrest-repo`, retensi 2 full, `start-fast` | `deploy/compose/postgres/pgbackrest.conf` |
| Backup dasar | layanan `pitr`: full mingguan, diff harian; membaca data dir read-only dan bicara lewat socket bersama | `deploy/compose/postgres/pitr.sh`, `core.yml` |
| Bukti | restore ke waktu T memuat yang ditulis sebelum T dan **tidak** memuat yang sesudahnya | `test/drill/pitr.test.sh`, job CI `backup and restore scripts` |

Setiap opsi pgBackRest yang dipakai sudah diperiksa dengan `pgbackrest help`
versi 2.58.0 yang terpasang, bukan dari ingatan.

## Jendela pemulihan

- **RPO** (data yang bisa hilang): paling lama 60 detik saat idle, karena
  `archive_timeout` menutup segmen yang berisi WAL paling tidak sekali
  semenit. Di bawah beban, segmen penuh (16 MB) diarsipkan begitu penuh.
- **Jendela** (seberapa jauh ke belakang): dari full backup tertua yang
  disimpan sampai sekarang. Dengan retensi 2 full dan full mingguan,
  jangkauannya sekitar dua pekan.
- **Belum**: repo kedua di luar mesin ini (object storage). Semua repo ada di
  volume Docker di daemon yang sama. Itu menjawab "salah hapus" dan
  "migrasi yang merusak", **bukan** "disk mati". Repo kedua butuh akun,
  sehingga keputusannya ada di pemilik.

## Memeriksa

```sh
task pitr:info            # backup yang ada dan rentang WAL yang terarsip
task pitr:backup -- full  # backup sekarang (full, diff, atau incr)
task pitr:test            # bukti PITR ujung ke ujung di container sekali pakai
```

Tanda sehat:

- `status: ok`;
- `wal archive min/max` terus bergerak;
- di server, `pg_stat_archiver.failed_count` tidak naik.

Kalau arsip gagal, Postgres **menahan** WAL di `pg_wal` dan mencoba lagi
berkala sampai berhasil. Menurut dokumentasi PostgreSQL 18 §25.3.1, kalau
disk `pg_wal` sampai penuh, server melakukan **PANIC shutdown**. Tidak ada
transaksi yang hilang, tetapi database mati sampai ruang dikosongkan. Karena
itu `failed_count` yang naik harus ditangani, bukan dibiarkan.

## Memulihkan ke suatu waktu

Pemulihan **tidak** dilakukan di atas database yang sedang berjalan. Hasilnya
dipulihkan ke tempat baru, diperiksa, dan baru kemudian diputuskan.

```sh
# 1. Container sekali pakai dengan repo read-only.
docker run -d --name pitr-restore -v selaras-core_pgbackrest-repo:/var/lib/pgbackrest:ro \
  --entrypoint sh selaras/postgres:dev -c "sleep infinity"
docker exec -u postgres pitr-restore install -d -m 700 /tmp/restored

# 2. Restore ke waktu target (UTC), lalu promote.
docker exec -u postgres pitr-restore pgbackrest --stanza=selaras --pg1-path=/tmp/restored \
  --type=time "--target=2026-09-26 07:03:44+00" --target-action=promote restore

# 3. Nyalakan di port lain, tanpa mengarsipkan, lalu periksa isinya.
docker exec -u postgres pitr-restore pg_ctl -D /tmp/restored -o "-p 5433 -c archive_mode=off" -w start
docker exec -u postgres pitr-restore psql -U selaras_admin -p 5433 -d selaras
```

Dari hasil itu ada dua jalan. Data yang hilang bisa diambil lalu dimasukkan
kembali ke database utama (biasanya ini yang benar untuk satu tabel). Atau,
untuk bencana penuh, seluruh cluster diganti: hentikan unit, ganti volume
`postgres-data` dengan hasil restore, lalu nyalakan lagi. Jalan kedua
merusak data yang ditulis setelah target, jadi keputusannya ada di pemilik.

Catatan: `psql -U` harus superuser cluster ini (`selaras_admin`). Role
`postgres` tidak ada di cluster ini, dan pgBackRest pun diberi tahu lewat
`PGBACKREST_PG1_USER`.

## Bukti (2026-09-26)

**Di CI dan lokal, di container sekali pakai** (`test/drill/pitr.test.sh`):

```text
ok    restored to T (2026-09-26 06:58:59.096280+00): A
ok    restored to the end of the archive: A,B
```

Kontrol negatif: dengan `archive_mode=off` di entrypoint, backup ditolak
(`archive_mode must be enabled`) dan test **gagal**.

**Di stack lokal yang nyata**, tanpa merusak apa pun (repo di-mount
read-only ke container sekali pakai):

| Langkah | Hasil |
| :--- | :--- |
| Jumlah pengguna pada T = 07:03:44.201390 UTC | 25 |
| `Auth/Register` setelah T | HTTP 200, pengguna 26 |
| Restore ke T | **25** pengguna |
| Restore ke akhir arsip | **26** pengguna |

Full backup pertama: database 41,3 MB, di repo 5,7 MB (terkompresi).
Selama pengukuran, `pg_stat_archiver` mencatat nol kegagalan.

## Yang belum

- **k3d / Kubernetes.** `deploy/k8s/infra/postgres.yaml` masih memakai
  `postgres:18.6-alpine` biasa tanpa arsip WAL. Di cloud, peran ini biasanya
  diambil oleh Postgres terkelola, yang PITR-nya bawaan. Di k3d, porting ke
  image ini adalah pekerjaan terpisah.
- **Repo di luar mesin** (lihat Jendela pemulihan).
- **Waktu pemulihan (RTO) untuk database besar** belum diukur. Yang terukur
  hanya database pengembangan 41 MB.
