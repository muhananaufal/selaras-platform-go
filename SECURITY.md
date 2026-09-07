# Kebijakan keamanan

## Melaporkan celah

Jangan membuka issue publik untuk celah keamanan. Kirim laporan lewat
[GitHub Security Advisories](https://github.com/muhananaufal/selaras-platform-go/security/advisories/new)
(privat, hanya terlihat pemelihara). Sertakan: versi/commit, langkah
reproduksi, dan dampak yang Anda amati. Laporan yang bisa direproduksi
dijawab dalam tujuh hari; perbaikan diprioritaskan menurut dampak, bukan
urutan masuk.

## Yang dijaga, dan di mana dibuktikan

| Jaminan | Mekanisme | Bukti |
| :--- | :--- | :--- |
| Token tidak bisa dipalsukan atau ditukar algoritmanya | JWT EdDSA; hanya identity-svc memegang kunci privat; `WithValidMethods`, `exp` wajib | `internal/identity/adapter/token/jwt.go`, ADR-020 |
| Service tidak mempercayai `user_id` yang sekadar dikirim | Interceptor gRPC memverifikasi token dan mencocokkan `sub` dengan `user_id` lewat refleksi proto | `internal/platform/authn`, ADR-026, `test/e2e/security_test.go` |
| Logout dan reset kata sandi berlaku seketika | Penghitung generasi di Redis, **gagal-tertutup** saat Redis tak terjangkau | `internal/edge/middleware/auth.go` |
| Sumber daya orang lain terlihat tidak ada, bukan terlarang | Setiap handler memasangkan slug dengan `sub` token; 404, bukan 403 (S9) | `test/acceptance/rules_e2e_test.go` |
| Reset kata sandi tidak bisa ditebak atau dipakai ulang | Token 32 byte `crypto/rand`, disimpan sebagai SHA-256, sekali pakai, kedaluwarsa | `internal/identity/domain/password_reset.go` |
| Login tidak membocorkan apakah alamat terdaftar | Hash umpan pada pengguna yang tidak ada; galat kirim surel tidak sampai ke pemanggil | `internal/identity/app/login.go`, `reset_password.go` |
| OAuth Google tidak bisa dibajak | `aud` dan `iss` diperiksa; `state` acak sekali pakai terikat provider; token tidak pernah di URL (S6) | `internal/identity/adapter/social/google.go`, `internal/edge/oauth/store.go` |
| Kredensial tidak punya nilai bawaan | Proses menolak start tanpa variabelnya | ADR-016 |
| Skema per service ditegakkan basis data | Peran `svc_<unit>` hanya melihat skemanya | ADR-006, `deploy/compose/initdb/` |
| Rahasia tidak masuk repositori | gitleaks di CI; `.env` ter-gitignore | `.github/workflows/ci.yml` |
| Dependensi berkerentanan diketahui | `govulncheck` di CI; Trivy pada image di CD; Dependabot | `.github/workflows/`, `.github/dependabot.yml` |

## Yang belum dijaga, dan dinyatakan

- **gRPC internal kini berautentikasi (ADR-026).** Setiap service memverifikasi
  token pengguna dengan kunci publik dan menuntut `sub` sama dengan `user_id`;
  dibuktikan `test/e2e/security_test.go` langsung ke port gRPC. Yang belum:
  pencabutan (generasi) hanya diperiksa gateway, dan token melintas PLAINTEXT
  di jaringan internal - mTLS menjadi keharusan bila jaringan itu tidak lagi
  sepenuhnya dikuasai.
- **NetworkPolicy ada di chart** (`templates/networkpolicy.yaml`): ingress
  ditolak untuk semua unit, dibuka hanya sesuai grafik `*_GRPC_TARGET` dan
  port probe dari namespace observability. Egress sengaja terbuka; penegakan
  bergantung pada CNI (k3s menegakkannya, belum dibuktikan hidup di sini).
- Tinjauan keamanan terakhir (2026-09-07) tidak menemukan temuan HIGH/MEDIUM;
  dua observasi di bawah ambang dicatat di RFC-999.

## Versi yang didukung

Hanya `main`. Tidak ada rilis bernomor; setiap commit di `main` lulus CI
penuh (unit + integrasi dengan `-race`, e2e, kontrak, pindaian rahasia).
