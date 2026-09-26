# ADR-028 — Test integrasi memakai service nyata dari CI/compose, bukan testcontainers

**Status.** Diterima (2026-09-26).

---

**Konteks.** Rencana Wave 1 memuat testcontainers-go. Keadaan saat keputusan
ini diambil:

- **CI.** Job `build and test` menjalankan Postgres 18.6, Redis 8.8.2,
  Mailpit 1.28.2, dan Kafka 4.3.1 sebagai service container. Test
  integrasi menerimanya lewat `TEST_DSN_*`, `TEST_REDIS_URL`, dan variabel
  sejenis.
- **Test yang dilewati tidak bisa diam-diam.** Setiap helper test integrasi
  gagal (bukan skip) kalau `CI` di-set tetapi variabelnya tidak ada, dan
  gerbang skip (`test/skips/check.sh`, sejak PR #15) menggagalkan job untuk
  setiap skip tanpa alasan tertulis. Run CI terakhir: 722 test di job unit,
  0 skip.
- **Lokal.** Test yang sama berjalan terhadap stack compose (`task up`),
  atau men-skip dirinya kalau stack mati.
- **Docker.** Di mesin pengembangan ini Docker terjangkau dari Windows lewat
  `DOCKER_HOST=tcp://localhost:2375`, jadi testcontainers secara teknis
  bisa jalan.

**Opsi yang ditimbang.**

| Opsi | Kelebihan | Kekurangan |
| :--- | :--- | :--- |
| **A. Service nyata dari CI/compose (keadaan sekarang)** | Satu jalur, dan jalur itu yang diuji CI; versi image sama dengan yang dijalankan compose dan k3d; tanpa dependensi test tambahan; gerbang skip sudah menjamin tidak ada yang terlewat | Lokal butuh `task up` plus variabel lingkungan; satu database dipakai bersama antar-paket, jadi isolasi bergantung pada disiplin `Truncate`/skema per unit |
| B. testcontainers di CI **dan** lokal | `go test ./...` berdiri sendiri; database per paket, isolasi penuh | Migrasi seluruh helper integrasi (Postgres dengan init skema dan migrasi per unit, Redis, Kafka, Mailpit); kontainer per paket menambah waktu CI; pustaka klien Docker masuk ke dependensi test, sehingga permukaan rantai pasok bertambah |
| C. testcontainers hanya lokal | Nyaman untuk pengembang | **Dua jalur, dan jalur lokal tidak pernah diuji CI**. Ini pola yang diaudit dan dibongkar pada 2026-09-25: jalur yang tidak dijalankan CI membusuk tanpa ada yang tahu |

**Keputusan.** Opsi A. Opsi C ditolak secara prinsip. Manfaat B di atas A
terutama kenyamanan lokal, yang tidak sebanding dengan migrasi dan biaya
dependensinya pada ukuran repo sekarang.

**Konsekuensi.** Positif: satu jalur test integrasi, dan jalur itulah yang
dijalankan CI dan dijaga gerbang skip. Negatif: menjalankan test integrasi
lokal tetap butuh stack compose, dan isolasi antar-test bergantung pada
helper yang mengosongkan tabelnya sendiri.

**Pembatal.**
- Test flaky karena state bersama antar-paket (bukan karena kode) muncul
  lebih dari sekali dalam satu bulan → isolasi per paket lewat testcontainers
  (opsi B).
- Kontributor tanpa stack compose menjadi hal biasa (misalnya repo menjadi
  proyek tim) → opsi B, **dengan** CI ikut memakainya.
- Versi service di CI dan compose mulai berbeda tanpa sengaja → opsi B, yang
  menaruh versinya di kode test, atau satu sumber versi untuk keduanya.
