# ADR-012 — Bentuk token: JWT berumur pendek + daftar cabut


**Konteks.** Sistem sekarang memakai Laravel Sanctum dengan personal access token yang disimpan
di database, sehingga setiap request melakukan lookup. ADR-007 mensyaratkan token membawa
`user_id` **dan** `user_profile_id` agar tidak ada panggilan jaringan wajib ke identity-svc di
setiap request terautentikasi.

**Opsi.** (a) token opaque di database, setara Sanctum · (b) JWT bertanda tangan berumur pendek
plus daftar cabut · (c) JWT berumur panjang tanpa pencabutan.

**Keputusan.** (b).

**Alasan (a) ditolak.** Token opaque tidak membawa klaim, jadi setiap request harus menukarnya ke
identity-svc — persis panggilan jaringan yang dihapus ADR-007. Memilih (a) berarti membatalkan
ADR-007.

**Alasan (c) ditolak.** Tanpa pencabutan, logout menjadi tipuan: token tetap sah sampai
kedaluwarsa. Sistem sekarang punya `logout` yang benar-benar mematikan token, dan menurunkan
jaminan itu adalah regresi.

**Aturan domain yang wajib ikut (D1).** `LoginUserAction` menghapus SELURUH token lama setiap
kali login berhasil, dan alur Socialite melakukan hal yang sama — satu sesi per pengguna. Daftar
cabut karena itu wajib bisa mencabut **semua token milik satu pengguna sekaligus**, bukan hanya
satu token. Ini mengubah bentuk penyimpanannya: kunci per pengguna, bukan per token.

**Konsekuensi.** Positif: nol lookup di jalur terpanas; klaim `user_profile_id` langsung tersedia
bagi setiap unit; logout tetap bermakna. Negatif: butuh penyimpanan daftar cabut yang terjangkau
seluruh unit; perubahan profil tidak tercermin sampai token diperbarui; rotasi kunci
penandatanganan menjadi tanggung jawab baru.

**Pembatal.** Kalau daftar cabut ternyata harus diperiksa di setiap request juga, keunggulan (b)
atas (a) menyusut drastis dan token opaque menjadi pilihan yang lebih jujur.


---

