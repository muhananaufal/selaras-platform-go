# ADR-017 — Meninjau tiga pilihan dengan data terukur


**Konteks.** K1 — klaim tentang apa yang dipakai industri — sejak awal berstatus gagal
diverifikasi, tetapi ia memblokir tiga keputusan nyata: router, akses database, dan tooling
manifest. Riset dijalankan; hasilnya tidak seragam dan dilaporkan apa adanya.

| Klaim | Hasil |
| :--- | :--- |
| Go Developer Survey mengukur pilihan router dan ORM | **Salah.** Survei resmi tidak membahasnya sama sekali `[fakta:E23]` |
| CNCF Survey memberi angka Helm vs Kustomize | **Tidak tertutup.** PDF-nya tidak terekstrak jadi teks `[fakta:E24 — GAGAL]` |
| Ada ukuran objektif untuk library Go | **Ada.** Hitungan "Imported by" di pkg.go.dev, indeks resmi Go `[fakta:E21, E22]` |

**Batas ukuran itu, dinyatakan di muka.** "Imported by" menghitung paket **publik** yang mengimpor.
Ia tidak melihat kode privat perusahaan, dan condong ke ekosistem open-source. Ia ukuran nyata,
bukan ukuran sempurna.

### Prinsip yang dipakai memutuskan

Ketiganya diputuskan dengan satu aturan yang sama, supaya hasilnya tidak terlihat sewenang-wenang:

> **Biaya sinyal menang ketika argumen teknisnya seri. Argumen teknis menang ketika ada perbedaan
> nyata yang bisa ditunjukkan.**

### Keputusan 1 — Router: `chi` diganti **Gin**

`[fakta:E21]` Gin diimpor 183.243 paket publik, `chi/v5` 17.309 — selisih sekitar sepuluh kali.

Secara teknis keduanya **seri**: sama-sama router HTTP, sama-sama matang, dan keduanya duduk di
lapisan adapter. Argumen `chi` satu-satunya adalah kompatibilitas `http.Handler` — nyata, tetapi
tidak menghasilkan kemampuan yang tidak dimiliki Gin. Karena teknisnya seri, biaya sinyal
menang, dan ADR-016 sisi pertama berlaku: bangun seperti yang dibangun di industri.

**Aturan yang mengikat agar keputusan ini tetap murah dibalik:** `gin.Context` **DILARANG
melewati batas handler.** Use case menerima tipe domain, bukan tipe framework. Begitu
`gin.Context` masuk ke `app` atau `domain`, keputusan ini berubah dari dua-arah jadi satu-arah
tanpa ada yang menyadarinya.

### Keputusan 2 — Akses database: **tetap `sqlc`**

`[fakta:E22]` GORM diimpor 86.926, `pgx/v5` 8.606. Angka itu tampak searah dengan keputusan 1,
tetapi **perbandingannya cacat**: driver Postgres milik GORM sendiri memakai `pgx`, sehingga
sebagian besar pemakai GORM ikut menghitung pgx tanpa terlihat. Perbedaan sesungguhnya lebih
kecil dari yang terbaca.

Dan di sini teknisnya **tidak seri**. Sistem ini memakai transactional outbox, read-model yang
dimaterialisasi dari event, dan schema-per-service — tiga hal yang menuntut kendali penuh atas
SQL. Pencarian pembantah menemukan tepat keberatan yang paling relevan: *"Business logic ends up
tightly coupled with the database, making it harder to migrate to other databases"* — dan itu
persis yang dicegah oleh `domain` yang bebas library.

Karena ada perbedaan teknis nyata, ia mengalahkan biaya sinyal. `sqlc` tetap.

### Keputusan 3 — Manifest: **Kustomize diganti Helm**

**Keputusan ini TIDAK berbasis data terukur, dan itu dinyatakan terbuka** `[fakta:E24 — GAGAL]`.

Alasan yang dipakai bukan popularitas melainkan bentuk artefaknya: Helm chart adalah **artefak
mandiri yang bisa di-version, dipaketkan, dan dipasang orang lain**, sementara Kustomize
menghasilkan overlay yang hanya bermakna di dalam repo ini. Untuk sistem yang ingin menunjukkan
kesiapan deployment, artefak yang berdiri sendiri lebih lengkap.

Satu sistem saja: memakai Helm **dan** Kustomize bersamaan adalah dua bahasa templating untuk
satu klaster — utang tanpa imbalan, dan itu tetap ditolak.

**Konsekuensi.** Positif: dua dari tiga keputusan kini berdiri di atas angka yang bisa diperiksa
siapa pun; prinsip pemutusnya tertulis sehingga hasilnya bisa dibantah secara konsisten.
Negatif: Gin mengikat handler pada tipe frameworknya sehingga menuntut disiplin yang dulu gratis;
Helm menambah bahasa templating yang punya kelas galatnya sendiri; dan keputusan 3 tetap berdiri
tanpa data.

**Pembatal.** Bila angka CNCF berhasil dibaca dan ternyata Kustomize setara atau lebih banyak
dipakai, keputusan 3 ditinjau ulang — ongkosnya rendah selama manifest belum ditulis (F9-01).

---

