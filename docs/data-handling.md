# Penanganan data pribadi

Aplikasi ini menyimpan tanggal lahir, jenis kelamin, tekanan darah, kadar
kolesterol, status merokok, catatan alergi, dan riwayat percakapan seseorang
tentang kesehatannya. Semuanya tentang satu orang yang bisa dikenali, dan
sebagian besar adalah data kesehatan.

Dokumen ini menyatakan aturannya dan **bagaimana aturan itu ditegakkan**, bukan
sekadar diminta.

## Aturan

### 1. Data pribadi tidak masuk log

Log dibaca banyak orang, dikirim ke tempat lain, dan disimpan lebih lama
daripada yang dikira siapa pun. Yang boleh dicatat adalah **pengenal**, bukan
isinya:

| Boleh | Tidak boleh |
| :--- | :--- |
| `user_id`, `assessment_id`, `saga_id`, `slug` | nama, alamat surel |
| nama unit, nama event, jumlah baris | tanggal lahir, jenis kelamin |
| kode galat, nama kolom | jawaban kuesioner, tekanan darah, kolesterol |
| durasi, offset, partisi | isi pesan chat, catatan alergi |

Pengenal cukup untuk menyelidiki: ia menuntun ke barisnya, dan barisnya ada di
basis data tempat ia memang seharusnya berada.

### 2. Data pribadi tidak masuk pesan galat

Pesan galat sampai ke klien, ke log, dan ke pelacak galat sekaligus. Galat yang
menyertakan nilai yang ditolaknya membawa nilai itu ke ketiganya.

`internal/platform/deletion/consumer.go` memotong alasan kegagalan sampai 500
karakter sebelum menyimpannya, dengan alasan yang sama: galat pgx yang panjang
bisa membawa seluruh pernyataan SQL beserta parameternya, dan parameter di jalur
itu adalah id pengguna.

### 3. Data uji bukan pengecualian

Nama, tanggal lahir, dan jawaban klinis di berkas test **ter-commit selamanya**.
Nilai yang dipakai harus jelas-jelas karangan:

- surel di domain `.test` atau `contoh.test`, tidak pernah domain sungguhan
- nama seperti "Uji Dasbor", tidak pernah nama orang
- tanggal lahir bulat seperti `1970-05-10`
- kata sandi yang jelas bukan milik siapa pun (`correct-horse-battery`)

Aturan ini bukan soal privasi orang fiktif; ia soal **kebiasaan**. Berkas test
yang berisi data nyata dimulai dari seseorang yang menyalin satu baris dari
produksi karena "cuma untuk mereproduksi".

### 4. Rahasia tidak punya nilai bawaan

ADR-016. Setiap kredensial dibaca dari environment tanpa fallback, dan aplikasi
**menolak start** bila salah satunya kosong. Nilai bawaan untuk lingkungan lokal
adalah nilai bawaan yang suatu saat berjalan di tempat lain.

## Bagaimana aturannya ditegakkan

| Aturan | Penegaknya |
| :--- | :--- |
| Rahasia tidak bocor ke repositori | `gitleaks` di CI (`task security`); 89 commit dipindai, nol temuan |
| Rahasia tidak punya bawaan | `LoadConfig` tiap service menolak yang kosong; diuji di test konfigurasinya |
| Kata sandi tidak pernah tercetak | `domain.Password.String()` mengembalikan `[REDACTED]`, dan `GoString()` juga - `%v` maupun `%#v` sama-sama aman |
| Data pribadi tidak masuk log | `TestNoPersonalDataInLogCalls` di bawah |

Yang **tidak** ditegakkan otomatis: isi pesan galat yang ditulis tangan. Itu
tinjauan manusia, dan disebutkan di sini supaya tidak terlihat tertutup padahal
tidak.

## Yang disimpan, dan di mana

| Skema | Isinya |
| :--- | :--- |
| `identity` | surel, hash kata sandi, generasi token |
| `profile` | nama, tanggal lahir, jenis kelamin, negara, bahasa |
| `assessment` | jawaban kuesioner, nilai klinis, hasil risiko, laporan LLM |
| `coaching` | program, tugas, dan percakapan coaching |
| `chat` | seluruh riwayat percakapan asisten |
| `nutrition` | catatan alergi, preferensi kuliner, riwayat menu |
| `dashboard` | SALINAN persentase risiko, kategori, dan riwayat analisis |

Yang terakhir patut diperhatikan: read-model tidak memiliki satu fakta pun,
tetapi ia **memuat salinan** data kesehatan. "Hanya proyeksi" bukan alasan
memperlakukannya lebih longgar - dan justru lebih mudah terlupakan saat akun
dihapus, karena tidak ada yang menganggapnya sumber.

## Saat akun dihapus

Seluruh tujuh skema di atas menghapus datanya, dan itu dibuktikan kueri, bukan
diklaim — lihat [`runbook/account-deletion.md`](runbook/account-deletion.md).
Cache pun ikut: cuplikan profil di `assessment`, bahasa di `nutrition`, dan
seluruh proyeksi di `dashboard`.

Sistem lama meninggalkan sebagian cache itu utuh. Dua baris pembersih cache di
`DeleteUserAccountAction` ditulis lalu dikomentari, dan cache riwayat panduan
menu yang disimpan `rememberForever` tidak pernah disebut sama sekali.
