# Runbook — OpenFGA dan proyeksi consent (ADR-030)

OpenFGA menjawab satu pertanyaan: apakah klinisi X boleh membaca data pasien
Y. Jawabannya diturunkan dari tuple, dan tuple itu **proyeksi** dari
ledger consent dan keanggotaan klinik di skema `clinic`. Sumber kebenarannya
adalah ledger, bukan OpenFGA.

## Alurnya

1. Use case clinic-svc (grant, revoke, tambah/keluarkan anggota, buat
   klinik) menulis perubahannya **dan** perubahan tuple yang ditimbulkannya
   ke `clinic.authz_changes` dalam satu transaksi. Perubahan consent
   diserialkan per pasien dengan advisory lock.
2. Projector di clinic-svc membaca `authz_changes` yang belum diterapkan,
   urut menurut id, menerapkannya satu per satu ke OpenFGA, lalu mengisi
   `applied_at`. Satu perubahan yang gagal menghentikan ronde itu, supaya
   urutan tetap terjaga, dan diulang di ronde berikutnya.
3. Pemeriksaan akses memakai `HIGHER_CONSISTENCY`.

## Angka yang diukur

`TestConsentReachesOpenFGAAndIsMeasured`: Postgres dan OpenFGA sungguhan,
projector berjalan, 10 putaran.

| Kejadian | Median | Maks |
| :--- | ---: | ---: |
| grant → akses terbuka | 243 ms | 318 ms |
| revoke → akses tertutup | 210 ms | 227 ms |

Batas yang ditegakkan test: 2 detik. Jendela pencabutan sebagian besar
adalah interval projector (`projectorInterval`, 200 ms). Smoke di stack
compose lewat gRPC sungguhan menunjukkan angka yang sama: grant 260 ms,
revoke 199 ms.

## Gejala dan tindakan

| Gejala | Penyebab | Periksa |
| :--- | :--- | :--- |
| Klinisi dengan consent tidak bisa membaca | Proyeksi tertinggal atau macet | `SELECT id, op, object, attempts, last_error FROM clinic.authz_changes WHERE applied_at IS NULL ORDER BY id LIMIT 20;` dan log `the OpenFGA projection is behind` |
| Satu baris dengan `attempts` terus naik | Perubahan itu ditolak OpenFGA | `last_error`; perubahan sesudahnya tertahan di belakangnya, dan itu disengaja |
| Log `OPENFGA_URL is not set` | clinic-svc berjalan tanpa proyeksi | Consent tetap tercatat dan diantrekan, tetapi tidak ada klinisi yang bisa membaca apa pun |
| clinic-svc tidak mau start | OpenFGA tidak terjangkau saat bootstrap | `docker compose ps openfga`, log `selaras-openfga-migrate` |

## Model

- **Sumber.** `deploy/openfga/model.fga` (DSL), diuji `model.fga.yaml`.
- **Yang ditulis ke server.** `model.json`, digenerate CLI `fga` dan dijaga
  sinkron oleh `test.sh` (job CI `authorization model`).
- **Saat start.** clinic-svc menulis model hanya bila maknanya berubah.
  Restart memakai store dan model yang sama (terverifikasi di stack lokal).

## Yang belum ada

- **Membangun ulang tuple dari ledger.** Basis data `openfga` tidak ikut
  backup aplikasi karena isinya proyeksi, tetapi alat rebuild-nya belum
  ditulis. Sampai alat itu ada, kehilangan basis data `openfga` berarti
  tidak ada klinisi yang bisa membaca. Arahnya gagal-tertutup, tetapi tetap
  gangguan.
- **OpenFGA di chart/k3d.** Belum ada. clinic-svc di klaster berjalan tanpa
  `OPENFGA_URL`, jadi proyeksi mati dan log mengatakannya.
