# ADR-026 — Setiap service memverifikasi token pengguna sendiri

**Status.** Diterima. Menuntaskan bentuk yang ADR-023 tetapkan tetapi
tinggalkan sebagai "belum dilakukan hari ini".

---

**Konteks.** ADR-023 memutuskan service tidak boleh mempercayai identitas
yang sekadar dikirimkan, dan memilih `user_id` sebagai yang dikirimkan
karena ia *bisa* diturunkan ke sesuatu yang dapat diverifikasi: `sub` di
token, dengan kunci publik yang sudah dipegang gateway. Yang tidak pernah
terjadi adalah verifikasinya sendiri. Sampai F9, tujuh service menerima
`user_id` dari siapa pun yang bisa menjangkau port gRPC-nya; tinjauan
keamanan gerbang keluar dan SECURITY.md sama-sama mencatatnya sebagai jurang
terbesar yang tersisa, dan test `TestAServiceRefusesARequestWithoutAToken`
membuktikan pod bocor bisa membaca profil siapa pun hanya dengan menebak id.

**Opsi yang ditimbang.**

| Opsi | Kelebihan | Kekurangan |
| :--- | :--- | :--- |
| A. mTLS / SPIFFE antar pod | Identitas pemanggil di tingkat transport; standar mesh | Infrastruktur baru (CA, rotasi, sidecar atau pustaka), dan ia menjawab "service mana yang memanggil", bukan "atas nama pengguna mana" - ADR-023 butuh yang kedua |
| B. Rahasia bersama antar service (HMAC) | Kecil | Sembilan unit memegang satu rahasia yang bisa mencetak identitas siapa pun; persis yang ADR-020 hindari dengan EdDSA |
| **C. Token pengguna diteruskan, service memverifikasi dengan kunci publik, `sub` harus sama dengan `user_id`** | Nol rahasia baru, nol panggilan jaringan, satu interceptor untuk 60-an RPC lewat refleksi proto; jawabannya tepat pertanyaan ADR-023 | Panggilan yang terjadi sebelum pengguna punya token (pendaftaran, login) butuh token yang dicetak identity-svc; seluruh worker tanpa pengguna tetap di luar skema ini |

**Keputusan.** Opsi C, di `internal/platform/authn`:

1. Gateway meneruskan token akses mentah sebagai metadata gRPC
   `authorization` (interceptor klien membacanya dari ctx yang diisi
   middleware, atau dari metadata masuk saat service memanggil service).
2. Setiap service memasang `UnaryServerInterceptor`: token diverifikasi
   EdDSA dengan `JWT_VERIFY_KEY` (kunci yang sama dengan gateway, tanpa
   nilai bawaan - ADR-016), lalu bidang `user_id` permintaan dibaca lewat
   refleksi protobuf dan harus sama dengan `sub`. Tidak ada token ->
   `Unauthenticated`; token orang lain -> `PermissionDenied` (403, bukan
   404: yang salah pemanggilnya, dan ia berhak tahu). Permintaan tanpa
   bidang `user_id` - Register, Login, ResolveRiskRegion - adalah RPC
   publik; token yang ikut tetap diverifikasi.
3. identity-svc mencetak token berumur tiga puluh detik atas nama pengguna
   untuk dua panggilannya ke profile-svc yang terjadi sebelum pengguna
   memegang token. profile-svc memverifikasinya seperti token lain; tidak
   ada jalur khusus.
4. Health dan reflection gRPC dilewati.

**Yang dibayar.** Satu verifikasi Ed25519 per RPC (mikrodetik). Dua
tanda tangan tambahan per pendaftaran dan login. Setiap service kini butuh
`JWT_VERIFY_KEY` untuk menyala. Token pengguna kini melintasi jaringan
internal di metadata gRPC - PLAINTEXT di compose dan k3d, seperti seluruh
lalu lintas internal di sana; pembatal di bawah menyebut kapan itu berhenti
cukup.

**Yang TIDAK ditutup.** Pencabutan (generasi) hanya diperiksa gateway;
service menerima token yang sah secara kriptografis walau generasinya sudah
dicabut. Jendela penyalahgunaannya sebesar umur token (`JWT_ACCESS_TTL`) dan
hanya berlaku bagi yang sudah melewati jaringan internal. `Principal` di
ctx membawa generasi, jadi service yang perlu lebih ketat bisa memeriksanya
sendiri ke Redis. Worker dan konsumen event tetap di luar skema ini: mereka
bekerja atas event, bukan atas permintaan, dan event sudah ditandatangani
oleh transaksi yang menulisnya (ADR-004).

**Bukti.** `internal/platform/authn` (unit test tiap cabang, mutasi tanpa
pemeriksaan kepemilikan merah), `test/e2e/security_test.go` (gRPC langsung
ke profile-svc: tanpa token `Unauthenticated`, token orang lain
`PermissionDenied`, pemilik sendiri lolos), seluruh e2e dan acceptance
tetap hijau lewat gateway.

**Pembatal.** Bila lalu lintas internal melintasi jaringan yang tidak
sepenuhnya dikuasai (multi-klaster, penyedia berbeda), token di metadata
PLAINTEXT tidak lagi cukup dan mTLS (opsi A) menjadi keharusan - sebagai
tambahan, bukan pengganti: opsi A tetap tidak menjawab "atas nama siapa".
Bila kelak ada RPC berpengguna yang memang harus dipanggil tanpa pengguna
(pekerjaan latar atas nama sistem), bentuk token layanan yang dicetak
identity-svc di butir 3 harus dinaikkan menjadi keputusan sendiri, bukan
dibiarkan tumbuh diam-diam.
