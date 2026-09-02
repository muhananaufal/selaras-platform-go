# ADR-021 — Kontrak identity dikoreksi agar tidak melawan ADR-007

**Status.** Diterima. Dibuat saat mengerjakan F1-13, sebelum satu pun handler gRPC ditulis.

---

**Konteks.** Kontrak `identity.v1` dikunci lebih dulu, sebelum use case-nya ada. Saat handler
gRPC-nya mulai ditulis, tiga bagiannya ternyata melawan keputusan yang sudah diambil di tempat
lain. Tidak ada satu pun konsumen yang sudah memakainya dan tidak ada yang ter-deploy, jadi
memperbaikinya sekarang jauh lebih murah daripada menumpuk implementasi di atas kontrak yang
sudah tahu-tahu keliru.

`buf breaking` melaporkan keempat perubahannya. Itu alatnya bekerja benar, bukan alasan untuk
mengurungkan: cek itu berbanding dengan `develop`, dan garis dasarnya bergeser begitu cabang ini
digabung.

---

## Koreksi 1 — `VerifyToken` dihapus, diganti `GetTokenGeneration`

Komentar aslinya berbunyi *"Dipanggil gateway pada setiap request terautentikasi."* Itu persis
panggilan jaringan wajib ke identity-svc yang keberadaan ADR-007 adalah untuk menghapusnya, dan
yang menjadi alasan ADR-012 menolak token opaque.

Gateway memverifikasi tanda tangan token **sendiri** dengan kunci publik — itulah gunanya memilih
EdDSA di ADR-020. Yang tidak bisa ia ketahui sendiri hanya satu: generasi token yang sedang
berlaku bagi seorang pengguna, dan itu pun hanya saat cache pencabutan tidak tahu.

`GetTokenGeneration(user_id) -> generation` adalah bentuk yang jujur dari kebutuhan itu.

## Koreksi 2 — `TokenPair.refresh_token` dihapus

Tidak ada yang menerbitkannya. Bidang yang selamanya kosong di sebuah kontrak lebih buruk
daripada bidang yang tidak ada: ia menjanjikan kemampuan yang tidak dimiliki siapa pun, dan
konsumen pertama yang mempercayainya akan menulis alur perpanjangan yang tidak akan pernah
bekerja.

**Mengapa tidak sekalian dibuat.** Token penyegar adalah kredensial kedua yang harus disimpan,
dicabut, dan dirotasi. Ia dibutuhkan saat masa berlaku token akses jauh lebih pendek daripada
masa sesi yang wajar. Di sini pencabutan sudah diperiksa di setiap request (ADR-020), sehingga
memperpanjang masa berlaku token akses **tidak** melemahkan pencabutan — logout tetap berlaku
seketika. Yang memendekkan masa berlaku hanya membatasi kerugian token curian yang belum
disadari pemiliknya, dan itu tidak sebanding dengan kredensial kedua untuk sistem berskala ini.

**Konsekuensi yang harus dinyatakan:** masa berlaku token akses menjadi parameter konfigurasi
yang harus dipilih sadar, bukan disembunyikan di balik alur perpanjangan.

## Koreksi 3 — `provider_token` menjadi `id_token`

Access token dari penyedia tidak membawa klaim apa pun yang bisa diverifikasi; untuk mengetahui
siapa pemiliknya, penerimanya harus bertanya balik ke penyedia. ID token OIDC **ditandatangani**
penyedia dan membawa `sub`, `email`, dan `email_verified` di dalamnya.

Itu penting justru karena `email_verified` adalah tumpuan pengerasan yang dipasang di F1-11:
alamat yang belum terbukti DILARANG dipakai menautkan akun. Dengan ID token, klaim itu sampai ke
identity-svc dengan tanda tangan penyedia masih utuh, sehingga identity-svc tidak perlu
mempercayai gateway soal alamat siapa yang sudah terbukti.

**Konsekuensi:** identity-svc harus memverifikasi tanda tangan ID token terhadap JWKS penyedia.
Itu pekerjaan tersendiri dan diberi nomor task; sampai ia ada, RPC ini belum bisa dilayani.

---

**Pembatal.** Kalau kelak ternyata pemeriksaan pencabutan harus dimatikan demi latensi, masa
berlaku token akses harus dipendekkan lagi, dan koreksi 2 harus ditinjau ulang bersama-sama —
keduanya adalah satu keputusan, bukan dua.

---
