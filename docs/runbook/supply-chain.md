# Runbook — rantai pasok (SBOM, provenance, verifikasi)

Pertanyaan yang harus bisa dijawab untuk setiap image yang berjalan:
**apa isinya, siapa yang membangunnya, dari commit mana, dan apakah ia diubah
sesudahnya.** Jawabannya harus bisa diperiksa mesin, bukan hanya dipercaya.

| Lapisan | Alat | Di mana |
| :--- | :--- | :--- |
| Dependensi diperbarui | Dependabot (gomod, actions, docker) per minggu, dikelompokkan | `.github/dependabot.yml` |
| Action tidak bisa diganti diam-diam | setiap `uses:` dipin ke SHA commit, tag hanya di komentar | semua workflow |
| Kerentanan diketahui | govulncheck (kode), Trivy (image, sebelum push) | job CI `known vulnerabilities`, job CD `build` |
| Isi image | SBOM CycloneDX dari syft v1.51.1 | CD `build`, lalu `actions/attest` dengan `sbom-path` |
| Asal image | provenance SLSA v1: repositori, workflow, commit, runner | CD `build`, `actions/attest` |
| Tanda tangan | keyless Sigstore lewat identitas OIDC workflow; tanpa kunci yang harus disimpan atau dirotasi | idem |
| Penegakan | `deploy/supply-chain/verify.sh` untuk kesebelas image, sebelum klaster disentuh | CD `deploy` |

## Yang diverifikasi, dan apa yang ditolak

`verify.sh <subjek> <workflow>` menjalankan `gh attestation verify` dua kali
(`https://slsa.dev/provenance/v1` dan `https://cyclonedx.org/bom`). Keduanya
wajib memenuhi:

- `--repo` repositori ini, sehingga atestasi dari repositori lain tidak
  dihitung;
- `--signer-workflow <repo>/<workflow>`, sehingga image yang ditandatangani
  workflow lain dari repositori yang **sama** tetap ditolak;
- `--deny-self-hosted-runners`, karena runner yang tidak dikelola GitHub
  tidak bisa dijamin bersih.

Image yang di-push dengan tangan, dibangun workflow lain, atau diubah
sesudah build tidak lolos, karena digest-nya tidak punya atestasi yang cocok.

## Bukti — CD belum pernah jalan, mekanismenya dibuktikan di CI

CD hanya jalan pada tag `v*`, dan belum ada klaster cloud. Karena itu job CI
**supply chain (SBOM, provenance)** menjalankan mekanisme yang sama pada
setiap perubahan, dengan binary edge-gateway sebagai subjek. Hasil pada PR #12
(run 36099327233, commit merge `4202d27`):

| Pemeriksaan | Hasil |
| :--- | :--- |
| SBOM menyebut modul pada versi yang di-resolve `go list -m` | 43 komponen Go; `connectrpc.com/connect v1.21.0`, `google.golang.org/grpc v1.83.2`, `go.opentelemetry.io/otel v1.46.0` — cocok |
| Atestasi dibuat | provenance `attestations/50084932`, SBOM `attestations/50084938`, subjek `sha256:1bda5efb…06b6` |
| Verifikasi `ci.yml` | provenance dan SBOM terverifikasi |
| Atestasi yang sama diperiksa sebagai `cd.yml` | **ditolak** |
| Binary ditambah satu byte | **ditolak** |

Kontrol negatif pemeriksa SBOM, dijalankan lokal dengan syft v1.51.1:

- SBOM binary `keygen` ditolak, karena ketiga modul tidak ada di dalamnya;
- SBOM dengan versi `connect` yang dipalsukan ke `v1.20.0` ditolak.

### Verifikasi mandiri: build yang dapat direproduksi

Laporan hijau dari CI bukan bukti yang cukup, jadi binary yang sama dibangun
ulang di luar GitHub:

1. Container `golang:1.26.8`, clone bersih, checkout
   `refs/pull/12/merge` (`4202d27`).
2. Build dengan `CGO_ENABLED=0 go build -trimpath`.
3. Hasilnya `sha256:1bda5efb79dadec8d5b75437a928acc6904ce447a9bdc177f63b848f7f2b06b6`,
   **identik byte demi byte** dengan binary CI.
4. `verify.sh` dijalankan dari Windows terhadap binary lokal itu: provenance
   dan SBOM keduanya terverifikasi.

Artinya binary edge-gateway **dapat direproduksi** dari source. Siapa pun
bisa membangunnya ulang dan mencocokkannya dengan atestasi yang
ditandatangani CI.

Catatan dari percobaan ini:

- Stempel VCS (`vcs.revision`, `vcs.time`) ikut di dalam binary. Build dari
  commit lain, termasuk commit cabang yang sama sebelum di-merge oleh
  `refs/pull/N/merge`, menghasilkan hash yang berbeda.
- Build `go build` di Windows dari worktree tidak membawa stempel VCS,
  sehingga hash-nya juga berbeda. Reproduksi dilakukan di container Linux
  yang sama jenisnya dengan runner.

## Memeriksa image secara manual

```sh
# butuh gh yang sudah login dan akses baca ke paket ghcr
GITHUB_REPOSITORY=muhananaufal/selaras-platform-go \
  bash deploy/supply-chain/verify.sh oci://ghcr.io/muhananaufal/selaras/edge-gateway:v1.2.0 .github/workflows/cd.yml

# isi SBOM-nya
gh attestation verify oci://ghcr.io/muhananaufal/selaras/edge-gateway:v1.2.0 \
  --repo muhananaufal/selaras-platform-go --predicate-type https://cyclonedx.org/bom \
  --format json --jq '.[0].verificationResult.statement.predicate.components[].name'
```

## Yang belum

- **Penegakan di klaster** (admission controller seperti Kyverno atau
  sigstore policy-controller). Saat ini penegakan terjadi di pipeline. Image
  yang di-`kubectl set image` dengan tangan belum ditolak oleh klaster.
- **Image yang reproducible.** Yang terbukti reproducible baru binary-nya.
  Layer image bergantung pada base image dan timestamp, dan belum diukur.
