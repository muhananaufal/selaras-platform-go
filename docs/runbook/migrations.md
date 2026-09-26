# Runbook — migrasi skema tanpa downtime

Setiap perubahan skema dijalankan sementara versi kode LAMA (N) masih
melayani, dan versi BARU (N+1) menyala berdampingan selama rolling update.
Jadi setiap migrasi harus aman bagi dua versi kode sekaligus. Aturannya
dijaga mesin, bukan ingatan:

| Penjaga | Yang diperiksa | Di mana |
| :--- | :--- | :--- |
| squawk 2.66.0 | kunci panjang, kolom wajib tanpa default, drop/rename, indeks non-concurrent, timeout yang hilang | `task migrate:lint`, job CI `migrations` |
| `test/migrations` | pasangan up/down, nomor versi tanpa celah, alasan tertulis setiap pengecualian, `CONCURRENTLY` sendirian, timeout ber-`LOCAL` | job CI `build and test` |

Migrasi yang sudah diterapkan sebelum aturan ini ada **dibekukan** di
`.squawk.toml` dan tidak ditulis ulang: mengubah migrasi yang sudah berjalan
justru berbahaya. Daftar itu hanya boleh menyusut.

## Pola expand / contract

Perubahan yang merusak dipecah menjadi langkah yang masing-masing aman bagi
kode yang sedang berjalan.

| Langkah | Migrasi | Kode |
| :--- | :--- | :--- |
| 1. Expand | tambah kolom/tabel baru, **nullable** atau ber-default; indeks `CONCURRENTLY` di berkas sendiri | N tetap berjalan (tidak menyebut kolom baru) |
| 2. Tulis ganda | - | N+1 menulis ke lama DAN baru, membaca yang lama |
| 3. Backfill | batch kecil, per rentang id, di luar transaksi migrasi | - |
| 4. Baca baru | - | N+2 membaca yang baru, masih menulis keduanya |
| 5. Contract | hapus kolom/tabel lama, **di rilis berikutnya**, dengan `-- squawk-ignore ban-drop-column` + `-- why:` yang menyebut rilis expand-nya | N+3 tidak lagi menyentuh yang lama |

Rename kolom = expand kolom baru + tulis ganda + backfill + contract kolom
lama. Tidak pernah `RENAME COLUMN` dalam satu langkah: kode N masih memakai
nama lama pada detik yang sama.

## Bentuk berkas yang benar

```sql
-- migrations/<unit>/NNNN_<nama>.up.sql
SET LOCAL lock_timeout = '2s';
SET LOCAL statement_timeout = '30s';
ALTER TABLE risk_assessments ADD COLUMN IF NOT EXISTS clinician_note TEXT;
```

- **`SET LOCAL`, bukan `SET`.** Driver pgx/v5 golang-migrate (yang diimpor
  `cmd/migrate`) menjalankan satu berkas dalam satu `ExecContext`
  (`database/pgx/v5/pgx.go`), dan pgx mengirim pernyataan tanpa argumen lewat
  protokol simple, jadi berkas bermultipernyataan adalah satu transaksi
  implisit. `SET` biasa bertahan di
  koneksi dan bocor ke migrasi berikutnya dalam larian yang sama.
- **`lock_timeout` pendek.** `ALTER TABLE` meminta kunci eksklusif; bila ada
  transaksi panjang di depannya, ia menunggu, dan setiap query di belakangnya
  ikut antre. Gagal cepat lalu ulang lebih murah daripada antrean itu.
- **`CREATE INDEX CONCURRENTLY` sendirian di berkasnya**, tanpa `SET`:
  dua pernyataan membentuk transaksi implisit, dan `CONCURRENTLY` menolak
  berjalan di dalam transaksi. Konsekuensinya berkas itu tanpa timeout, dan
  itu dinyatakan dengan `-- squawk-ignore` + `-- why:`.

## Mengabaikan saran squawk

Diperbolehkan, tidak pernah diam-diam:

```sql
-- why: contract step of 0006_add_clinician_note (released in v1.4); no code reads note since v1.5
-- squawk-ignore ban-drop-column
ALTER TABLE risk_assessments DROP COLUMN IF EXISTS note;
```

Baris `-- why:` wajib menempel pada pengecualiannya (`test/migrations`). Untuk
`squawk-ignore` per pernyataan, letakkan `-- why:` di ATAS pengecualian:
squawk hanya menerapkan pengecualian pada pernyataan yang tepat di bawahnya,
dan baris komentar di antaranya memutus hubungan itu (diuji dengan squawk
2.66.0). Untuk `squawk-ignore-file`, `-- why:` boleh di bawahnya.

## Bila migrasi gagal di tengah

- Berkas bermultipernyataan: seluruhnya batal (transaksi implisit), versi
  ditandai *dirty*. Periksa penyebabnya, lalu
  `go run ./cmd/migrate -service <unit> -direction force -force-version <versi sebelumnya>`
  dan jalankan ulang.
- `lock_timeout` terlampaui: tidak ada yang berubah. Cari transaksi panjang
  (`SELECT pid, now() - xact_start, query FROM pg_stat_activity WHERE state <> 'idle' ORDER BY 2 DESC`),
  tunggu atau hentikan, lalu ulang.
- Indeks `CONCURRENTLY` yang gagal meninggalkan indeks `INVALID`. Hapus dengan
  `DROP INDEX CONCURRENTLY <nama>` lalu ulang.

## Drill: expand/contract di bawah lalu lintas nyata

`test/drill/expand` menerapkan migrasi contoh ke skema assessment sementara
enam pengguna terus memulai, membaca, dan mendaftar penilaian lewat gateway.
Migrasinya ada di `test/drill/expand/migrations` dan ikut di-lint squawk
sebagai contoh yang wajib bersih. Tabel versinya sendiri
(`drill_schema_migrations`), jadi tidak bercampur dengan migrasi produk.

```bash
TEST_DRILL=1 TEST_E2E_BASE_URL=http://127.0.0.1:18080 \
TEST_DSN_ASSESSMENT="postgres://svc_assessment:...@127.0.0.1:15432/selaras?sslmode=disable&search_path=assessment" \
go test ./test/drill/expand/ -count=1 -v
```

Hasil 2026-09-25, stack compose lokal:

| Langkah | Durasi |
| :--- | ---: |
| expand: kolom nullable | 51 ms |
| backfill per 200 baris | 44 ms |
| expand: indeks `CONCURRENTLY` | 97 ms |
| contract: drop indeks | 76 ms |
| contract: drop kolom | 87 ms |

**662 request, 0 gagal**, p50 57 ms, p99 99 ms, maks 155 ms. Larian kedua:
681 request, 0 gagal, p99 88 ms.

Sejak PR #15, drill ini **wajib jalan di CI** pada setiap perubahan, sebagai
langkah terpisah di job `e2e, acceptance, k6` sesudah k6. Sebelumnya drill
ini men-skip dirinya sendiri di CI tanpa ada yang melihat. Gerbang skip
(`test/skips/check.sh`) yang membuat skip itu terlihat. Larian pertama di
CI: **5.731 request, 0 gagal**, p50 6 ms, p99 14 ms, maks 23 ms. Runner CI
lebih cepat daripada laptop yang dipakai bersama, sehingga jumlah request
dalam jendela waktu yang sama lebih banyak.

Kontrol negatif, supaya drill ini terbukti bisa merah: migrasi expand diganti
sementara dengan `RENAME COLUMN final_risk_percentage` - perubahan yang
merusak kode N. Hasilnya **266 dari 700 request gagal** dan drill merah;
langkah contract mengembalikan nama kolomnya dan skema kembali utuh.

Yang TIDAK dibuktikan drill ini: rolling update N ke N+1 di klaster. Ia
membuktikan langkah expand dan contract aman bagi kode yang sedang berjalan;
langkah tulis-ganda dan baca-baru adalah perubahan kode biasa yang diuji
test unit dan e2e-nya sendiri.
