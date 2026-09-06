# Runbook on-call — profile-svc

Pemilik profil pengguna: nama, tanggal lahir, jenis kelamin, negara, bahasa.
Dipanggil sinkron oleh gateway (`/profile`, `/me`), identity-svc (membuat
profil kosong saat daftar, F1-31), dan assessment-svc (membaca usia dan
jenis kelamin saat penilaian dimulai). Setiap perubahan profil menerbitkan
`profile.updated`, yang diisi ke cache lokal assessment dan nutrition
(ADR-007). Bergantung keras hanya pada Postgres (skema `profile`).

## Siapa yang terdampak bila ia mati

Diukur oleh `test/chaos/service.sh profile-svc` (hasil di
`test/chaos/service.md`):

- **Mati**: `GET/PATCH /profile`, `POST /register` (pembuatan profil gagal),
  `POST /risk-assessments` (butuh usia dan jenis kelamin — kecuali cache
  profil di assessment sudah terisi untuk pengguna itu).
- **Bertahan**: login, `/me`, membaca penilaian yang sudah ada, dashboard,
  coaching, chat, nutrition. Semua unit lain membaca salinan lokalnya, bukan
  memanggil profile-svc.
- Event `profile.updated` yang tertunda menumpuk di outbox-nya dan terkirim
  saat ia kembali; cache unit lain tertinggal selama itu.

## Gejala dan cara membacanya

| Gejala | Yang hampir pasti terjadi | Periksa |
| :--- | :--- | :--- |
| Gateway 504 `The request took too long.` pada `/profile` | service tidak terjangkau; batas waktu 10 s per panggilan (`rpc.WithUpstreamDeadline`) yang bekerja | `curl :9202/readyz`; log start-up |
| Gateway 503 `UNAVAILABLE` pada `/profile` | service mati dan klien gRPC sudah menyadarinya | idem |
| Penilaian memakai usia lama setelah tanggal lahir diubah | event `profile.updated` belum sampai ke cache assessment | `SELECT count(*) FROM profile.outbox WHERE published_at IS NULL`; relay dan Kafka |
| Panduan menu berbahasa salah | cache bahasa di nutrition tertinggal | sama: outbox profile → topic `profile.updated` → konsumen nutrition |
| `NOT_FOUND` untuk pengguna yang baru daftar | profil kosong belum dibuat karena profile-svc mati saat pendaftaran | pengguna bisa mengisi profil lewat `PATCH /profile` (dibuat saat itu, B7); tidak ada yang perlu diperbaiki manual |

## Triase dalam lima menit

1. `curl :9202/readyz`.
2. Grafana → gRPC `profile.v1.Profile/*` status bukan OK; `GetProfile`
   `NOT_FOUND` dalam jumlah kecil adalah normal (profil belum diisi).
3. `trace_id` dari log gateway → Tempo: span `profile.v1.Profile/GetProfile`
   dengan durasi tepat 10 s adalah batas waktu yang bekerja — service-nya
   tidak menjawab sama sekali.
4. Outbox: baris `published_at IS NULL` yang tua (> 1 menit) berarti relay
   atau broker, bukan service ini.

## Cara pulih

- Nyalakan lagi. Tidak ada keadaan di memori; relay melanjutkan outbox.
- Cache unit lain menyusul sendiri begitu event terkirim — jangan menulis
  ulang cache-nya manual. Bila ragu apakah cache tertinggal:
  `cmd/backfill-nutrition` dan padanannya di assessment membaca ulang dari
  sumber (lihat `docs/runbook/backfill-nutrition.md`).

## Yang JANGAN dilakukan

- Jangan membuat unit lain memanggil profile-svc sinkron "supaya selalu
  segar". Itu persis kopling yang dilarang ADR-007, dan chaos di atas
  menunjukkan mengapa: satu unit mati akan mematikan semuanya.

## Metrik yang membuktikan pulih

`rpc_server_call_duration_seconds{rpc_method=~"profile.*"}` kembali OK dengan
p95 di bawah 20 ms (laporan kinerja: `UpdateProfile` 16 ms, `GetProfile`
5 ms); `profile.outbox` tanpa baris tertunda.
