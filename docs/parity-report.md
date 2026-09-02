# Laporan paritas — mesin risiko SCORE2

**Tanggal.** 2 September 2026
**Gerbang.** F2 — "Seluruh golden vector dari B2 lulus."

---

## Angka

| | |
| :--- | ---: |
| Vektor diuji | **288** |
| Lulus | **288** |
| Gagal | **0** |
| Selisih terbesar | **0.000000** poin persentase |
| Toleransi yang dipakai | 0.005 poin persentase |
| Nilai eGFR antara yang diperiksa | **144** |
| Toleransi eGFR | 1e-9 |

Selisih terbesar **nol** berarti setiap satu dari 288 vektor menghasilkan angka
yang identik sampai dua desimal — bukan "cukup dekat", melainkan sama.

## Dari mana vektornya

Dihasilkan branch `oracle/golden-vector-source` di repo Laravel, yang **tidak pernah
digabung**. Ia bukan milik kode Go: satu-satunya perannya adalah membantah.

Sapuannya: 2 jenis kelamin × 4 wilayah risiko × 9 usia (40–85) × diabetes ya/tidak ×
2 mode masukan (manual dan proksi) = 288.

Ketiga model tercakup: SCORE2 (usia < 70 tanpa diabetes), SCORE2-OP (≥ 70 tanpa
diabetes), dan SCORE2-Diabetes.

## Konstanta yang dipakai kedua sisi

Checksum SHA-256 berkas PHP asalnya direkam di dalam golden vector **dan** melekat
pada JSON yang di-embed Go. Harness membandingkan keduanya sebelum satu vektor pun
dihitung.

| Berkas | SHA-256 |
| :--- | :--- |
| `config/score_models.php` | `ab1d29a755bf76f4b4be52b0ccb272cae4d61168fd325f3cbbdd32f1c179c631` |
| `config/region_mapping.php` | `0980644aff5610f4683a4f231f3923fe48060016ffc39d640596981284bee4f5` |

Tanpa perbandingan itu, paritasnya tidak berarti apa-apa: vektor yang dihasilkan dari
koefisien lain akan lulus atau gagal karena alasan yang tidak ada hubungannya dengan
port ini.

Konstantanya diekspor ke JSON **oleh PHP itu sendiri**, bukan disalin tangan. Tidak
ada satu angka pun yang melewati penafsiran ulang manusia.

## Dua koreksi yang ikut masuk

Golden vector dihasilkan dari sumber yang **sudah** diperbaiki, jadi kedua koreksi ini
adalah bagian dari yang dibuktikan — bukan perbedaan yang dimaafkan.

| Temuan | Perbaikan | Akibatnya di angka |
| :--- | :--- | :--- |
| **B1** | `UserProfile::getAgeAttribute` ditambahkan | `estimateSbp` sebelumnya mengembalikan **104 tetap** untuk usia berapa pun. Setelah diperbaiki, SBP bergerak 166 (usia 40) → 186 (usia 85) |
| **B12** | `q_exercise` dibaca dari satu sumber dengan satu nilai | Penyesuaian −7 pada SBP dan HbA1c sebelumnya **tidak pernah berlaku** |

## Bukti bahwa test-nya bukan test kosong (F2-12)

Empat mutasi dijalankan terhadap kode yang sudah hijau. Semuanya harus merah, dan
semuanya merah.

| Mutasi | Hasil |
| :--- | :--- |
| Satu koefisien diubah di desimal **keempat** (`score2.male.age` 0.3742 → 0.3743) | **8 vektor gagal**, selisih terbesar 0.01 pp |
| Mean linear predictor SCORE2-OP dinolkan | **64 vektor gagal**, selisih terbesar 9.16 pp |
| Urutan pemilihan model dibalik (usia menang atas diabetes) | **64 vektor salah model** |
| Kalibrasi wilayah dilewati | **288 vektor gagal**, selisih terbesar 36.99 pp |

Mutasi pertama yang paling berarti: perubahan di desimal keempat sebuah koefisien
tunggal cukup untuk menggagalkan test. Itu ukuran sensitivitasnya.

## Yang diuji terpisah, dan mengapa

**Nilai klinis diperiksa sebelum model dijalankan.** Estimator yang meleset dan
koefisien yang meleset ke arah berlawanan menghasilkan angka akhir yang benar. Kalau
hanya angka akhir yang diuji, keduanya lolos bersama.

**eGFR diperiksa langsung.** Ia masuk ke logaritma di SCORE2-Diabetes, sehingga
selisih kecil di sana membesar — dan memeriksanya sendiri jauh lebih cepat menunjuk
penyebab daripada memeriksa angka akhirnya saja.

## Yang TIDAK dibuktikan laporan ini

- **Kebenaran klinis.** Yang dibuktikan adalah paritas dengan sistem lama, bukan bahwa
  sistem lama benar terhadap publikasi SCORE2. Kalau koefisien di `score_models.php`
  keliru, port ini keliru dengan setia.
- **Masukan di luar sapuan.** 288 vektor menyapu kombinasi yang disebut di atas.
  Jawaban proksi punya lebih banyak permutasi daripada itu; yang belum tersapu belum
  terbukti.
- **Perilaku pada masukan tak sah.** Vektor hanya berisi masukan yang sah. Penolakan
  masukan cacat diuji terpisah, bukan di sini.

---
