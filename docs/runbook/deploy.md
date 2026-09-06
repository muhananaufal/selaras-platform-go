# Runbook — deploy dan rollback

F9-17. Dua jalur: lokal (k3d) dan cloud (CD). Keduanya memakai chart yang
sama dan hanya berbeda nilai (ADR-010).

## Lokal — k3d

```
task k3d:all        # klaster + Secret + infra + image + chart, dari nol
task k3d:import     # bangun ulang 11 image dan impor ke node
task k3d:deploy     # helm upgrade --install dengan values-local.yaml
task k3d:down       # buang klaster beserta isinya
```

Yang terjadi di `k3d:all`, berurutan: klaster dibuat (`deploy/k3d/cluster.yaml`),
dua Secret ditulis dari `.env` (`secrets.sh`), dependensi dan observability
dipasang lalu migrasi dan topic dijalankan sebagai Job (`infra.sh`), sebelas
image dibangun dua-dua dan diimpor (`import.sh`), chart dipasang
(`deploy.sh`). Edge menjawab di `http://127.0.0.1:28080`, Grafana di
`http://127.0.0.1:23000`.

Diverifikasi 2026-09-07: node Ready, 8 Job migrasi + Job topic Complete,
9 unit Running, 8 HPA membaca CPU dari metrics-server bawaan k3s, ScaledObject
KEDA Ready, suite e2e hijau terhadap klaster (`docs/performance-report.md`
memuat angka autoscaling-nya).

## Cloud — pipeline CD

`.github/workflows/cd.yml`, dipicu tag `v*`:

1. **build** — satu job per unit (matriks 11), `docker build` dari Dockerfile
   yang sama dengan lokal, `load` dulu, **pindai Trivy** (CRITICAL/HIGH yang
   punya perbaikan → gagal), BARU `push` ke `ghcr.io/<owner>/selaras/<unit>:<tag>`.
   Image yang gagal pindai tidak pernah ada di registri.
2. **deploy** — Job migrasi dan topic dari `deploy/k8s/jobs/migrate.yaml`
   dengan tag yang sama, lalu `helm upgrade --install --atomic --wait`
   dengan `values-cloud.yaml` dan `image.tag=<tag>`.

Kubeconfig datang dari Secret lingkungan GitHub `KUBECONFIG_B64`; tanpa itu
job gagal di langkah pertamanya. Tidak ada kredensial di repositori
(ADR-016).

Pemicu manual (`workflow_dispatch`) men-deploy tag yang SUDAH ada di
registri tanpa membangun ulang — itu jalur rollback ke versi lama, dan
jalur "deploy ulang yang sama" saat klaster diganti.

**Belum pernah dijalankan terhadap klaster cloud sungguhan.** Yang teruji
adalah bagian yang bisa diuji tanpa akun cloud: Dockerfile, chart, Job
migrasi, dan urutannya — semuanya di k3d. Sisanya adalah rancangan yang
dinyatakan, dan dicatat begitu di RFC penutup.

## Rollback

Tiga tingkat, dari yang paling murah:

| Keadaan | Tindakan |
| :--- | :--- |
| `helm upgrade` gagal `--wait` (pod tidak pernah siap) | **tidak ada**: `--atomic` sudah mengembalikan rilis ke revisi sebelumnya sendiri. Periksa `helm -n selaras history selaras`. |
| Rilis berhasil, tetapi salah | `helm -n selaras rollback selaras <revisi>` — revisi dari `history`. Migrasi TIDAK ikut mundur (lihat di bawah). |
| Perlu versi tertentu, bukan revisi sebelumnya | jalankan `cd` manual dengan `tag` versi itu; image-nya masih di registri. |

### Migrasi tidak ikut rollback

Migrasi berjalan sebagai Job SEBELUM chart, dan `helm rollback` tidak
menyentuh basis data. Aturan yang dijaga sejak F1: **migrasi harus
kompatibel dengan versi unit sebelumnya** (tambah kolom, jangan hapus;
ubah dalam dua rilis). Bila sebuah migrasi memang harus dibalik:

```
kubectl -n selaras run migrate-down --rm -it --restart=Never \
  --image=ghcr.io/<owner>/selaras/migrate:<tag> \
  --env=MIGRATE_DSN=<dsn langsung ke Postgres, bukan PgBouncer> \
  -- -service <skema> -direction down
```

`down` golang-migrate membalik SELURUH migrasi skema itu, bukan satu
langkah — itu perilaku alatnya, dan alasan migrasi dirancang kompatibel ke
belakang alih-alih mengandalkan `down`.

## Yang JANGAN dilakukan

- Jangan `kubectl set image` langsung. Helm tidak tahu, `history` berbohong,
  dan rollback berikutnya mengembalikan ke sesuatu yang bukan keadaan
  sebelumnya.
- Jangan men-deploy dari mesin pengembang ke cloud dengan `task k3d:deploy`
  dan konteks kubeconfig yang diganti. Jalur cloud hanya lewat CD supaya
  setiap rilis punya tag, pindaian, dan riwayat.
