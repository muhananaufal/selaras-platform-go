# Runbook — infrastructure as code (OpenTofu)

Isi klaster k3d lokal kini dideklarasikan sebagai kode di `deploy/iac/k3d`.
Sebelumnya isinya dibangun oleh `kubectl apply` imperatif di `infra.sh`,
tanpa state, tanpa `plan` yang bisa dibaca sebelum perubahan, dan tanpa cara
mengetahui apakah seseorang mengubah klaster dengan tangan.

## Kenapa OpenTofu, bukan Terraform

Lisensi keduanya dibaca dari berkas `LICENSE` di tag rilis masing-masing:

| | Versi | Lisensi |
| :--- | :--- | :--- |
| OpenTofu | v1.12.6 | Mozilla Public License 2.0 |
| Terraform | v1.16.4 | Business Source License (licensor IBM); produksi boleh, kecuali menawarkan layanan yang bersaing dengan versi berbayar IBM |

Proyek ini tidak menawarkan layanan semacam itu, jadi Terraform pun boleh.
OpenTofu dipilih karena lisensinya terbuka tanpa syarat pemakaian. HCL di
sini cukup sederhana untuk dipakai keduanya, tetapi **hanya OpenTofu yang
diuji** (job CI `infrastructure as code`), jadi hanya OpenTofu yang diklaim.

## Apa yang dikelola, dan apa yang tidak

| Dikelola OpenTofu | Tidak, dan alasannya |
| :--- | :--- |
| Namespace `selaras`, `observability` | **Klaster itu sendiri.** Dibuat k3d dari `deploy/k3d/cluster.yaml`. Bagian ini spesifik platform dan di cloud diganti modul klaster cloud. Provider k3d untuk Terraform (0.0.7) terlalu muda untuk dijadikan fondasi |
| ConfigMap initdb, aturan Prometheus, dasbor Grafana, dari berkas yang **sama** dengan compose | **Secret.** State menyimpan nilai dalam teks biasa, jadi secret tetap dibuat `deploy/k3d/secrets.sh` dari `.env`, dan Secret Grafana dibuat `infra.sh` |
| KEDA, chart resmi 2.20.2 (sama dengan versi rilis yang dipasang sebelumnya) | **Job sekali jalan** (migrasi, topic). Mengikuti rilis, bukan infrastruktur |
| 29 objek dari `deploy/k8s/infra` dan `deploy/k8s/observability`, dibaca apa adanya dengan `manifest_decode_multi`, tidak ditulis ulang di HCL | **Chart aplikasi.** Siklusnya siklus rilis (`deploy/k3d/deploy.sh`) |

YAML di `deploy/k8s` tetap menjadi satu-satunya sumber: OpenTofu membacanya,
bukan menyalinnya.

## Pemilik objek, dan kenapa `force_conflicts`

`kubectl scale` atau `kubectl patch` mengambil alih field yang diubahnya
sebagai *field manager* lain. Apply biasa kemudian **menolak** menimpanya.
Hal ini terukur saat pertama kali diuji: `field manager conflict … with
"kubectl"`. Karena OpenTofu dimaksudkan menjadi pemilik objek-objek ini,
`kubernetes_manifest` memakai `force_conflicts = true`, sehingga apply
berikutnya mengembalikan drift alih-alih gagal. Ini hanya benar karena tidak
ada pihak lain yang memiliki field di objek-objek ini: HPA dan KEDA
ScaledObject menargetkan unit aplikasi (`deploy/helm`), tidak pernah objek
infra.

## Pemakaian

```sh
task k3d:all    # up -> iac -> secrets -> import -> infra -> deploy
task k3d:iac    # apply saja
task k3d:plan   # cek drift: exit 0 sesuai kode, exit 2 ada yang berubah
task k3d:down   # hapus klaster DAN state lokal yang mendeskripsikannya
```

**Urutan `k3d:all` diperbaiki.** Sebelumnya `infra` (yang menjalankan Job
migrasi) jalan sebelum `import`, padahal Job memakai image lokal dengan
`imagePullPolicy: Never`, dan `infra.sh` sendiri menulis "run import.sh
first". Cacat ini ditemukan dengan membaca kode dan belum direproduksi
dengan urutan lama. Yang terbukti adalah urutan baru berjalan dari klaster
kosong (lihat Bukti).

`secrets.sh` tidak lagi membuat namespace. Kalau dijalankan sebelum
`k3d:iac`, ia berhenti dengan pesan, karena namespace yang dibuat di luar
OpenTofu membuat apply berikutnya gagal dengan "already exists".

State bersifat lokal (`deploy/iac/k3d/terraform.tfstate`, di-ignore git) dan
hidup-mati bersama klaster: `k3d:down` menghapusnya. `.terraform.lock.hcl`
**di-commit**, karena file itu mengunci checksum provider (keduanya
ditandatangani, dan key ID-nya tercatat saat `init`). Untuk cloud, state
pindah ke backend jarak jauh dengan locking. Itu bagian dari pekerjaan cloud.

## Bukti (2026-09-26)

**Dari klaster kosong**, `task k3d:down && task k3d:all`, exit 0:

- `Apply complete! Resources: 35 added`;
- kesembilan Job selesai;
- chart `STATUS: deployed`;
- 16 pod `Running`, 9 `Completed`, tidak ada yang lain;
- gateway `readyz` 200 lewat NodePort.

**Drift** (`test/iac/drift.test.sh`, juga di job CI `infrastructure as code`
pada klaster k3d baru di runner):

```text
ok    apply succeeds
ok    plan right after apply shows no changes
ok    a hand-edited ConfigMap is seen as drift
ok    apply puts it back
ok    hand edits to objects from the YAML are seen as drift
ok    apply reclaims the edited fields
ok    plan after the second apply shows no changes
ok    redis is back to the declared replicas
ok    postgres is back to the declared image
```

Instalasi k3d di CI memverifikasi SHA256 dari `checksums.txt` rilis. Berkas
itu menamai biner `_dist/k3d-linux-amd64`, sehingga pola pertama gagal
cocok; ini diperiksa sebelum dipakai. Biner yang diubah satu byte ditolak.
