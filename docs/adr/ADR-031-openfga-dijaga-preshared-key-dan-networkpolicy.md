# ADR-031 — OpenFGA dijaga preshared key di semua lingkungan, plus NetworkPolicy di klaster

**Status.** Diterima (2026-10-30).

---

**Konteks.** ADR-030 menjadikan OpenFGA sumber keputusan "klinisi ini boleh
membaca pasien itu". Tuple-nya ditulis clinic-svc dari ledger consent, lalu
dibaca assessment-svc dan coaching-svc pada setiap pembacaan klinisi. Sampai
ADR ini, OpenFGA berjalan **tanpa autentikasi** di compose dan di CI. Siapa
pun yang bisa menjangkau port 8080 bisa menulis
`user:penyerang consented_clinician patient:x` dan tuple keanggotaan, lalu
membaca pasien mana pun lewat RPC klinisi yang sah. Semua pertahanan
ADR-030 (consent, audit sebelum baca, gagal-tertutup) bertumpu pada
kebenaran tuple, dan tuple itu bisa ditulis siapa saja yang satu jaringan.

Tuple tidak bisa dijaga seperti data lain. Unit lain menjangkau datanya lewat
peran Postgres masing-masing, sehingga pod yang bocor hanya membawa hak
perannya sendiri. OpenFGA tidak punya identitas pemanggil kecuali yang kita
konfigurasikan.

Untuk membawa OpenFGA ke klaster (butir tracker "OpenFGA di chart"), perlu
diputuskan dua hal: cara memasangnya, dan siapa yang boleh memanggilnya.

**Yang diverifikasi dari source, bukan dari ingatan.**
- Server v1.21.0 mendukung `OPENFGA_AUTHN_METHOD=preshared` dan
  `OPENFGA_AUTHN_PRESHARED_KEYS` (`cmd/run/flags.go:74-78`, `run.go:562`).
  Kunci dibandingkan sebagai hash SHA-256 dengan
  `subtle.ConstantTimeCompare` tanpa keluar lebih awal
  (`internal/authn/presharedkey/presharedkey.go`).
- Health check gRPC melewati authn (`pkg/server/health/health.go:27`,
  `AuthFuncOverride`), jadi probe tetap berjalan tanpa kunci.
- go-sdk v0.8.3 mengirim kunci sebagai `Authorization: Bearer` lewat
  `credentials.CredentialsMethodApiToken` (`credentials/credentials.go:15-31`).
- Chart resmi `openfga-0.3.15` (app v1.21.0, digest
  `bde4e1fa…0894` cocok dengan index) menjalankan migrasi sebagai Job yang
  ditunggu image `groundnuty/k8s-wait-for` plus RBAC. Chart itu juga membawa
  subchart Bitnami dari arsip lama yang, menurut `values.yaml` chart itu
  sendiri, "no longer actively maintained".

**Opsi yang ditimbang: siapa yang boleh memanggil.**

| Opsi | Kelebihan | Kekurangan | Yang membatalkannya |
| :--- | :--- | :--- | :--- |
| **A. Preshared key di semua lingkungan + NetworkPolicy ingress di klaster** | Berlaku juga di compose dan CI, tidak hanya di klaster; kunci dibandingkan konstan-waktu oleh server; health check tidak terdampak; NetworkPolicy tetap menutup jalur ketika kunci bocor | Satu rahasia baru di `.env`, dua Secret, dan tiga unit; satu kunci dipakai bersama oleh penulis dan pembaca, jadi pembaca yang bocor juga bisa menulis | Ada kebutuhan membedakan hak tulis dan hak baca per unit (lihat Pembatal) |
| B. NetworkPolicy saja | Tanpa rahasia baru | Compose dan CI tetap terbuka; satu kesalahan label pod membuka semuanya; tidak ada lapisan kedua | — tidak cukup sendirian |
| C. OIDC (`authn.method=oidc`) dengan token per unit dari penerbit | Identitas per unit, bisa dibedakan dan dicabut sendiri-sendiri | Butuh penerbit OIDC yang belum ada (Hydra baru bersyarat aplikasi kedua, rencana §B); menambah dependensi pada jalur gagal-tertutup | Hydra atau SPIRE masuk ke stack |
| D. mTLS antar-service | Identitas kriptografis tanpa rahasia bersama | Butuh CA dan rotasi sertifikat (SPIRE menunggu klaster cloud) | SPIRE terpasang (Gelombang 5) |

**Opsi yang ditimbang: cara memasang di k3d.**

| Opsi | Kelebihan | Kekurangan |
| :--- | :--- | :--- |
| **1. Manifest tulis-tangan `deploy/k8s/infra/openfga.yaml`, didaftarkan di OpenTofu** | Satu pola dengan Postgres, Kafka, dan Redis; otomatis ikut uji drift; image, migrasi, dan env sama persis dengan compose; dikeraskan (non-root 65532, read-only, drop ALL) | Upgrade versi dilakukan tangan; probe dan resource ditulis sendiri |
| 2. Chart resmi lewat `helm_release` | Dirawat upstream; upgrade cukup naik versi | Image pihak ketiga `k8s-wait-for` + RBAC untuk menunggu Job; subchart Bitnami arsip; drift per objek tidak terlihat di plan seperti manifest |
| 3. Store per unit (sidecar) | Tidak ada service bersama | Model dan tuple justru harus dibagi; ditolak |

**Keputusan.** Opsi A + pemasangan 1:

1. OpenFGA **selalu** berjalan dengan `preshared` di compose, CI, dan k3d.
   Kuncinya `OPENFGA_API_TOKEN` di `.env`. `task k3d:secrets` menaruhnya di
   `selaras-infra` (dibaca OpenFGA) dan `selaras-secrets` (dibaca unit lewat
   `secretEnv`).
2. Klien `internal/platform/authz` menerima `authz.Server{URL, Token}`, dan
   setiap panggilan mengirim kunci. `authz.ServerFromEnv` **menolak start**
   bila `OPENFGA_URL` diisi tanpa `OPENFGA_API_TOKEN`, atau sebaliknya.
   Tanpa keduanya tetap sah: semua pembacaan klinisi ditolak dan log
   mengatakannya (ADR-030).
3. Di klaster, NetworkPolicy `openfga-ingress` hanya mengizinkan port 8080
   dari pod `clinic-svc`, `assessment-svc`, dan `coaching-svc`.
4. Migrasi berjalan sebagai init container di setiap replika. Ini aman karena
   migrasi OpenFGA memegang lock tingkat sesi, sehingga replika kedua
   menunggu, bukan berlomba. Koneksinya langsung ke Postgres, bukan lewat
   PgBouncer, dengan alasan yang sama.

**Konsekuensi.**

Positif:
- Menulis tuple kini butuh kunci, di mana pun stack berjalan.
- Di klaster, kunci yang bocor saja belum cukup. Penyerang juga harus berada
  di salah satu dari tiga pod yang diizinkan.

Negatif:
- Rotasi kunci saat ini butuh restart OpenFGA dan ketiga unit. Server
  menerima beberapa kunci sekaligus (`authn-preshared-keys` adalah slice),
  jadi rotasi tanpa jeda bisa dilakukan dengan dua kunci. Prosedurnya
  belum ditulis di runbook dan dicatat sebagai sisa.
- `cmd/clinic-rebuild-tuples` tidak menyentuh OpenFGA (ia mengantre ke
  outbox), jadi tidak perlu kunci. Bila kelak ada alat yang memanggil
  OpenFGA langsung dari luar tiga pod itu, NetworkPolicy harus dibuka secara
  eksplisit.
- `values-cloud.yaml` mewarisi `OPENFGA_URL: http://openfga:8080`. Klaster
  cloud harus menyediakan OpenFGA dengan nama itu (Gelombang 5).

**Pembatal.** Ada kebutuhan membedakan hak tulis dari hak baca, misalnya
unit pembaca yang terpapar ke luar. Pada titik itu kunci bersama tidak cukup
dan opsi C (OIDC per unit) atau D (mTLS lewat SPIRE) menggantikannya.

**Bukti yang wajib ada sebelum ADR ini dianggap terlaksana.**
- Test integrasi terhadap OpenFGA berkunci: tanpa kunci dan dengan kunci
  salah, Bootstrap gagal dan Check menjawab error. Kontrol negatif: test
  yang sama merah terhadap server tanpa kunci, dan mutan klien yang tidak
  mengirim kunci membuat seluruh suite merah.
- k3d: OpenFGA siap setelah migrasi, clinic-svc mem-bootstrap store lewat
  kunci, pembacaan klinisi lewat gateway berhasil. Pod di luar tiga unit
  tidak bisa tersambung ke 8080 (NetworkPolicy), sedangkan pod berlabel
  unit yang diizinkan bisa.
