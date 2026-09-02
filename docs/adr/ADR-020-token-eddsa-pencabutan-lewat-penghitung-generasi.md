# ADR-020 — Token ditandatangani EdDSA; pencabutan lewat penghitung generasi

**Status.** Diterima, memperinci ADR-012. Dibuat saat mengerjakan F1-05.

---

**Konteks.** ADR-012 memutuskan bentuk token — JWT berumur pendek plus daftar cabut —
tetapi menyisakan dua hal yang baru terasa saat kodenya ditulis: algoritma penandatanganan,
dan bentuk penyimpanan pencabutan.

ADR-012 sudah menyatakan bahwa D1 (satu sesi per pengguna) memaksa pencabutan mengenai
**seluruh** token seorang pengguna sekaligus, dan bahwa karena itu kuncinya per pengguna,
bukan per token. Migrasi identity yang ditulis lebih dulu justru memakai tabel
`revoked_tokens` per token — itu keliru terhadap ADR-012, dan diperbaiki di sini.

---

## Keputusan 1 — EdDSA (Ed25519), bukan HMAC

**Opsi.** (a) HS256 · (b) RS256 · (c) EdDSA/Ed25519.

**Keputusan.** (c).

**Alasan (a) ditolak.** HMAC simetris: kunci yang memverifikasi adalah kunci yang
menandatangani. Pada platform sembilan unit, itu berarti sembilan unit yang semuanya bisa
mencetak token admin, dan bocornya satu unit mana pun setara dengan bocornya identity-svc.
Di sini hanya identity-svc yang memegang kunci privat.

**Alasan (b) ditolak.** RSA memberi pemisahan yang sama, tetapi kuncinya panjang, verifikasi
lebih lambat, dan ada parameter yang bisa dipilih keliru. Ed25519 tidak punya parameter untuk
disalahpilih.

**Yang dibayar.** Rotasi kunci menjadi tanggung jawab nyata: kunci publik harus sampai ke
setiap verifier, dan pergantiannya butuh masa dua kunci berlaku bersamaan. HMAC tidak
menghadapi itu karena rahasianya cuma satu.

---

## Keputusan 2 — Penghitung generasi per pengguna, bukan daftar token

**Opsi.** (a) tabel token yang dicabut · (b) stempel waktu "token sah sejak" per pengguna ·
(c) penghitung generasi per pengguna.

**Keputusan.** (c). Kolom `users.token_generation`, klaim `gen` di token.

**Alasan (a) ditolak.** Membatalkan seluruh sesi menjadi penghapusan sebanyak jumlah token
yang beredar, dan tabelnya tumbuh terus sampai ada penyapu yang membersihkannya. ADR-012
sudah menolak bentuk ini; migrasi awal yang memakainya adalah kekeliruan, bukan pilihan.

**Alasan (b) ditolak.** `iat` pada JWT berpresisi detik. Token yang terbit pada detik yang
sama dengan pencabutan menjadi ambigu, dan jawabannya bergantung pada perbandingan yang
harus dipilih dengan hati-hati. Bilangan bulat tidak punya jam untuk salah.

---

## Keputusan 3 — Pencabutan diperiksa terhadap penyimpanan bersama, bukan identity-svc

**Pembatal ADR-012 berbunyi:** *"Kalau daftar cabut ternyata harus diperiksa di setiap
request juga, keunggulan (b) atas (a) menyusut drastis dan token opaque menjadi pilihan yang
lebih jujur."* Pemeriksaan generasi memang terjadi di setiap request, jadi pembatal itu
wajib dihadapi, bukan dilewati.

**Ia tidak terpicu, dan ini alasannya.** Yang dilarang ADR-007 adalah panggilan jaringan
wajib **ke identity-svc** di setiap request terautentikasi. Pemeriksaan generasi tidak pergi
ke sana; ia dilayani penyimpanan bersama. Dua keuntungan token pembawa klaim tetap utuh:
identity-svc tidak berada di jalur terpanas, dan `user_profile_id` tersedia tanpa pencarian
kedua. Token opaque akan kehilangan keduanya, bukan salah satunya.

**Yang jujur diakui:** keunggulannya memang menyusut. Kalau kelak ternyata penyimpanan
bersama itu sendiri menjadi titik lemah yang menuntut penanganan sebesar identity-svc,
keputusan ini harus ditinjau ulang, bukan dipertahankan karena sudah tertulis.

**Aturan yang mengikat implementasi.** Pemeriksa pencabutan WAJIB gagal-tertutup.
Penyimpanan yang tidak bisa dihubungi berarti pencabutan tidak bisa dibuktikan, dan menerima
token dalam keadaan itu mengubah setiap gangguan menjadi jendela di mana logout tidak
berlaku.

---

**Konsekuensi.** Positif: satu tulisan membatalkan seluruh sesi; hanya identity-svc yang bisa
mencetak token; tidak ada tabel token yang perlu disapu. Negatif: setiap request
terautentikasi menyentuh penyimpanan bersama; rotasi kunci publik menjadi pekerjaan
operasional baru; gagal-tertutup berarti gangguan penyimpanan menjadi gangguan autentikasi.

**Pembatal.** Kalau ternyata satu sesi per pengguna dicabut sebagai aturan produk — pengguna
boleh masuk di beberapa perangkat sekaligus dan mencabut satu per satu — penghitung generasi
tidak lagi cukup, dan bentuk per-sesi harus dipertimbangkan lagi.

---
