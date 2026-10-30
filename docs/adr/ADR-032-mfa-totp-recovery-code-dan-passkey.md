# ADR-032 — MFA: TOTP, recovery code, dan passkey; tantangan setelah kata sandi, sesi baru setelah faktor kedua

**Status.** Diterima (2026-10-30).

---

**Konteks.** Satu-satunya faktor masuk saat ini adalah kata sandi, atau
token ID penyedia sosial. Produk ini menyimpan penilaian risiko
kardiovaskular, progres coaching, dan, sejak ADR-030, membuka data itu
untuk klinisi. Kata sandi yang bocor berarti seluruh data kesehatan
seseorang ikut bocor. Rencana §B menuliskan "MFA dan passkey" sebagai celah
top-1% yang tersisa di lapisan identitas.

Yang dibaca sebagai dasar keputusan (dibuka sesi ini):
- **OWASP Multifactor Authentication Cheat Sheet:**
  - "If the application provides multiple ways for a user to authenticate
    these should all require MFA."
  - "Require reauthentication with an existing enrolled factor before
    allowing changes."
  - OTP: "Ensure OTPs are single use"; "Apply strict attempt limits".
  - Passkey sebagai MFA: "require user verification and validate the
    returned user-verification flag on the server"; "a touch confirming
    user presence alone is not a second factor".
  - "Providing the user with a number of single-use recovery codes when they
    first setup MFA."
- **NIST SP 800-63B-4:**
  - §3.1.4.2: "Verifiers SHALL accept a given OTP only once while it is
    valid."
  - §3.2.2: kegagalan beruntun pada satu authenticator dibatasi "to no more
    than 100 by disabling that authenticator".
  - §3.1.2.2: recovery code "SHALL be used successfully only once". Bila di
    bawah 112 bit, wajib "stored in a salted and hashed form using a
    suitable password hashing scheme".
  - §3.1.4.2: kunci OTP "SHALL be strongly protected against unauthorized
    disclosure".
  - §3.2.5: WebAuthn "provides phishing resistance through verifier name
    binding".
- **Library, dibaca dari source di module cache:**
  - `go-webauthn/webauthn` v0.18.2 (BSD-3): `CreateCredential` dan
    `ValidatePasskeyLogin` menerima hasil `protocol.Parse…ResponseBytes`,
    jadi payload JSON dari browser bisa lewat Connect dan gRPC tanpa
    `*http.Request`. `SessionData` bisa diserialisasi JSON.
  - `pquerna/otp` v1.5.0 (Apache-2.0) untuk RFC 6238.
  - `descope/virtualwebauthn` v1.0.5 (MIT), hanya untuk test: authenticator
    perangkat lunak, sehingga upacara WebAuthn bisa diuji ujung ke ujung
    tanpa browser.

**Opsi yang ditimbang.**

| Opsi | Kelebihan | Kekurangan | Yang membatalkannya |
| :--- | :--- | :--- | :--- |
| **A. Ditulis di identity-svc dengan library standar: TOTP + recovery code + passkey (sebagai faktor kedua dan sebagai login tanpa kata sandi)** | Tetap satu tempat yang tahu soal kredensial (ADR-020); tanpa layanan baru; data faktor ikut saga hapus akun yang sudah ada; passkey memberi opsi tahan phishing yang diminta NIST di AAL2 | Kode keamanan yang ditulis sendiri harus diuji keras (replay, batas percobaan, enkripsi rahasia); upacara WebAuthn butuh state sementara (Redis) | Muncul aplikasi kedua yang butuh SSO (rencana §B: Hydra) |
| B. Ory Kratos sebagai layanan identitas | MFA, passkey, dan recovery sudah jadi | Mengambil alih identity-svc, saga hapus akun, dan revocation generasi yang sudah terbukti; satu layanan dan satu basis data baru | Tim tidak ingin memelihara alur kredensial sendiri |
| C. Keycloak | Lengkap | Ditolak di rencana §B: aplikasi Java besar yang mengambil alih identity-svc | — |
| D. TOTP saja | Paling kecil | Tidak ada opsi tahan phishing; TOTP bisa di-relay oleh situs palsu | — tidak cukup untuk top-1% |

**Keputusan.** Opsi A, dengan aturan berikut:

1. **Faktor.**
   - TOTP: RFC 6238, SHA-1, 6 digit, langkah 30 detik, toleransi ±1 langkah.
   - Recovery code: 10 kode, ditampilkan satu kali.
   - Passkey: WebAuthn, *user verification* **wajib** dan flag UV diperiksa
     server.
   - Passkey dengan UV adalah dua faktor dalam satu upacara (sesuatu yang
     dimiliki + biometrik/PIN), jadi **login passkey tidak meminta faktor
     lain**.
2. **Tantangan setelah kata sandi, sesi setelah faktor kedua.** Untuk akun
   dengan faktor aktif, Login dan pertukaran sosial **tidak menerbitkan
   token**. Keduanya mengembalikan `MfaChallenge` berisi tiket acak 256
   bit, metode yang tersedia, dan masa berlaku 5 menit. Tiket disimpan di
   Redis sebagai hash, terikat ke user, maksimal 5 percobaan.
   - Tahap kata sandi **tidak** menaikkan generasi token. Kalau menaikkan,
     siapa pun yang tahu kata sandi bisa mengeluarkan pemiliknya dari sesi
     tanpa melewati faktor kedua.
   - Generasi baru dinaikkan dan token diterbitkan hanya saat `VerifyMfa`
     berhasil (D1 tetap berlaku).
   - Jawaban untuk kata sandi salah tetap `invalid credentials` yang sama
     (akun tidak bisa ditebak dari jawabannya). Tantangan hanya muncul
     setelah kata sandi benar.
3. **OTP sekali pakai.** Langkah waktu terakhir yang diterima disimpan per
   akun. Kode dari langkah yang sama atau lebih lama ditolak (NIST
   §3.1.4.2).
4. **Batas percobaan.** Kegagalan faktor kedua dihitung beruntun per akun.
   - Setelah 10 kegagalan, verifikasi ditolak selama 15 menit.
   - Pada 100 kegagalan beruntun, TOTP **dimatikan** (NIST §3.2.2). Akun
     masih bisa masuk dengan recovery code atau passkey, lalu mendaftar
     ulang.
   - Hitungan kembali ke nol setelah verifikasi berhasil.
5. **Rahasia TOTP dienkripsi** dengan AES-256-GCM. Kuncinya
   `MFA_ENCRYPTION_KEY` (32 byte, di luar basis data), dengan AAD = user id
   agar ciphertext tidak bisa dipindah ke akun lain. Kolom `key_version`
   ada untuk rotasi kunci.
6. **Recovery code** terdiri dari 16 karakter base32 (80 bit). Empat
   karakter pertama adalah id pencarian unik per akun; 12 sisanya diverifikasi
   dengan argon2id (hasher kata sandi yang sudah ada). Satu percobaan berarti
   satu verifikasi argon2, bukan sepuluh. Setiap kode sekali pakai.
7. **Mengubah faktor butuh bukti segar.** Mendaftar, mematikan TOTP,
   menghapus passkey, dan membuat ulang recovery code meminta salah satu
   bukti: kata sandi, kode TOTP, atau recovery code. Pengecualiannya akun
   yang belum punya kata sandi maupun faktor (akun sosial murni): pendaftaran
   faktor pertamanya cukup dengan sesi, karena tidak ada yang bisa dibuktikan.
8. **Reset kata sandi tidak mencabut MFA.** Masuk setelah reset tetap
   melewati faktor kedua.
9. **State upacara WebAuthn** (`SessionData`) disimpan di Redis dengan TTL
   5 menit, dikunci id upacara acak, dan dihapus begitu dipakai. Klien
   tidak pernah memegangnya. RP ID dan origin dari konfigurasi
   (`WEBAUTHN_RP_ID`, `WEBAUTHN_RP_ORIGINS`), tanpa default.
10. **Hapus akun** ikut menghapus semua faktor. Tabelnya ber-FK `ON DELETE
    CASCADE` ke `users`, dan saga sudah menghapus baris `users`.

**Konsekuensi.**

Positif:
- Kata sandi yang bocor tidak lagi cukup untuk membuka data kesehatan.
- Ada jalur masuk tahan phishing (passkey).
- Semua jalur masuk (kata sandi dan sosial) memakai tantangan yang sama.

Negatif:
- Dua rahasia baru di konfigurasi identity-svc (`MFA_ENCRYPTION_KEY`, dan
  konfigurasi RP WebAuthn).
- identity-svc menulis ke Redis untuk tiket dan upacara. Bila Redis mati,
  login akun ber-MFA gagal tertutup, sedangkan akun tanpa MFA tidak
  terdampak.
- Pemulihan bila semua faktor dan recovery code hilang belum ada. Jalurnya
  dukungan manusia dengan verifikasi identitas (OWASP), dan itu keputusan
  produk.

**Pembatal.** Ekosistem Selaras punya aplikasi kedua yang nyata (rencana
§B). Pada titik itu login, termasuk MFA, pindah ke penyedia OIDC (Hydra +
identity-svc sebagai halaman login), dan aturan di atas menjadi aturan
halaman login itu.

**Bukti yang wajib ada sebelum ADR ini dianggap terlaksana.**
- Unit: replay kode TOTP pada langkah yang sama ditolak; batas 10/15 menit
  dan pematian pada 100; recovery code sekali pakai; ciphertext yang
  dipindah ke user lain gagal didekripsi.
- e2e lewat gateway:
  - kata sandi benar tanpa faktor kedua tidak menghasilkan token, dan sesi
    lama pemilik tetap hidup;
  - TOTP benar membuka sesi;
  - recovery code membuka sekali saja;
  - passkey (authenticator virtual) mendaftar, login tanpa kata sandi, dan
    ditolak bila flag UV tidak ada;
  - tiket kedaluwarsa atau habis percobaan ditolak.
