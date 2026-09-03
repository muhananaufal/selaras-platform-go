# Runbook — memindahkan preferensi kuliner dari sistem lama

Preferensi kuliner di sistem lama menumpang sebagai satu kolom JSON di
`user_profiles`. Di platform ini ia menjadi tabel `nutrition.culinary_preferences`
dengan kolom dan batasan sungguhan — bagian **expand** dari pemisahan
expand-contract. Dokumen ini menjelaskan cara memindahkan isinya.

## Prasyarat yang BELUM ADA

**Berkas masukannya harus sudah memuat `user_id` platform (UUID), bukan id
bilangan bulat sistem lama.** Pemetaan antara keduanya lahir dari pemindahan
identitas, dan pemindahan itu belum ada: setiap service di platform ini sejauh
ini dimulai dari kosong.

Perkakas ini **menolak** baris yang `user_id`-nya bukan UUID, bukan
menerjemahkannya dengan tebakan. Preferensi yang mendarat pada orang yang salah
lebih buruk daripada preferensi yang tidak pindah — salah satunya memuat catatan
alergi.

Selama pemetaan itu belum ada, jalankan perkakas ini hanya pada data contoh.

## Mengapa bukan berkas migrasi

Rencananya semula menyebut `migrations/nutrition/0002_backfill.sql`. Itu tidak
dipakai, dan alasannya bukan gaya:

- golang-migrate menjalankan setiap berkas di direktori itu pada **setiap**
  lingkungan, termasuk yang tidak punya data lama. Pemindahan data yang ikut
  berjalan di lingkungan kosong hanya bisa gagal atau tidak melakukan apa-apa —
  dan yang kedua lebih buruk, karena ia mencatat dirinya sebagai sudah selesai.
- Sistem lama memakai MySQL dan platform ini Postgres. Satu berkas SQL tidak bisa
  membaca keduanya.
- Nomor `0002` sudah dipakai migrasi outbox.

## Langkah

### 1. Ekspor dari sistem lama

Dari basis data MySQL sistem lama, hasilkan NDJSON — satu baris satu pengguna:

```sh
mysql --batch --raw --skip-column-names selaras_database -e "
  SELECT JSON_OBJECT(
    'user_id', up.user_id,
    'culinary_preferences', COALESCE(up.culinary_preferences, JSON_OBJECT())
  )
  FROM user_profiles up
  WHERE up.culinary_preferences IS NOT NULL
" > culinary.ndjson
```

`user_id` di sana masih bilangan bulat. Terjemahkan kolom itu menjadi UUID
platform sebelum melanjutkan (lihat **Prasyarat** di atas).

### 2. Periksa dulu — uji kering

```sh
NUTRITION_DATABASE_DSN='postgres://svc_nutrition:...@host:5432/db?sslmode=disable&search_path=nutrition' \
  go run ./cmd/backfill-nutrition -input culinary.ndjson -dry-run
```

Uji kering membaca dan memvalidasi seluruh baris, lalu **tidak menulis apa pun**.
Angka `written` yang dilaporkannya adalah jumlah baris yang AKAN ditulis.

Perhatikan `rejected`. Setiap baris yang ditolak dicatat beserta nomor barisnya
dan sebabnya. Sebab yang paling sering:

| Pesan | Artinya |
| :--- | :--- |
| `user_id ... is not a platform uuid` | Langkah pemetaan id belum dijalankan. |
| `invalid budget level: legacy label ...` | Label di luar Hemat/Standar/Fleksibel. |
| `invalid cooking style: legacy label ...` | Label gaya memasak di luar yang dikenal. |
| `too many preference tags` | Daftar melebihi 30 entri — sistem lama tidak punya batas. |

### 3. Jalankan

```sh
NUTRITION_DATABASE_DSN='...' go run ./cmd/backfill-nutrition -input culinary.ndjson
```

**Kode keluar:**

| Kode | Artinya |
| :--- | :--- |
| 0 | Seluruh baris diterima. |
| 1 | Berhenti di tengah — DSN salah, berkas tidak terbaca, penulisan gagal. |
| 2 | Selesai, tetapi ada baris yang ditolak. Periksa log. |

Kode 2 ada supaya pemindahan yang separuh berhasil tidak terlihat sukses di
pipeline mana pun.

### 4. Periksa hasilnya

```sql
SELECT budget_level, count(*) FROM nutrition.culinary_preferences GROUP BY 1;
```

## Menjalankannya dua kali aman

Perkakas ini **idempoten**: pengguna yang barisnya sudah ada dilewati, tidak
ditimpa. Preferensi yang sudah diubah pengguna di platform baru tetap utuh.

Terbukti pada data contoh: menjalankannya lagi setelah sebuah baris diubah
tangan melaporkan `written=0 skipped_existing=3`, dan perubahan tangan itu masih
ada sesudahnya.

## Jalur mundur

Tidak ada perubahan skema yang perlu dibatalkan — yang ditulis hanya baris data.
Hapus persis pengguna yang dipindahkan, **bukan** seluruh tabel: tabelnya juga
memuat preferensi yang dibuat pengguna langsung di platform baru.

```sql
-- Daftar user_id diambil dari berkas ekspor yang dipakai.
DELETE FROM nutrition.culinary_preferences
WHERE user_id = ANY($1::uuid[]);
```

Bila ekspornya masih ada:

```sh
jq -r '.user_id' culinary.ndjson > moved.txt
```

lalu hapus berdasarkan daftar itu.

## Data contoh

`test/fixtures/culinary_preferences_sample.ndjson` memuat lima baris: tiga sah,
satu dengan id bilangan bulat lama, dan satu dengan label anggaran yang tidak
dikenal. Ia dipakai membuktikan perkakas ini menerima yang benar dan menolak
yang salah, tanpa menghentikan sisanya.
