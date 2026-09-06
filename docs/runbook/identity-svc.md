# Runbook on-call — identity-svc

Pemilik akun, kata sandi, token, dan saga penghapusan akun. Satu-satunya
pemegang kunci PRIVAT token (ADR-020). Bergantung keras pada Postgres (skema
`identity`) dan Redis (penghitung generasi token); bergantung lunak pada
profile-svc (pembuatan profil saat daftar), Kafka (relay outbox dan konfirmasi
saga), dan SMTP (reset kata sandi).

## Siapa yang terdampak bila ia mati

- **Pendaftaran, login, logout, reset kata sandi, hapus akun: mati.**
- **Pengguna yang sudah login: tidak terdampak** selama Redis hidup. Gateway
  memverifikasi token dengan kunci publik dan memeriksa pencabutan di Redis;
  identity-svc tidak disentuh per permintaan.
- Saga penghapusan yang sedang berjalan menggantung sampai ia kembali; tidak
  ada yang hilang — konfirmasi unit lain menunggu di topic.

## Gejala dan cara membacanya

| Gejala | Yang hampir pasti terjadi | Periksa |
| :--- | :--- | :--- |
| Proses menolak start dengan `... is not set` | variabel wajib kosong (ADR-016): DSN, `JWT_SIGNING_KEY`, `REDIS_URL`, `KAFKA_BROKERS` | log baris pertama; `.env.example` |
| Start gagal `dirty database version` | migrasi sebelumnya terputus di tengah | `cmd/migrate -service identity`; lihat catatan F8 tentang memaksa versi setelah memverifikasi objeknya ada |
| `/register` 5xx, RPC `Register` `UNAVAILABLE` | profile-svc mati — pendaftaran membuat profil kosong lewat gRPC (F1-31) | runbook profile-svc; `PROFILE_GRPC_TARGET` |
| `/register` dan `/login` lambat (p95 > 1 s) tetapi tidak gagal | antrean argon2id: hasher membatasi dua derivasi serentak, masing-masing ~190 ms; pemanggil ketiga menunggu | wajar di bawah burst pendaftaran; naikkan replika, bukan batasnya — batasnya menjaga memori |
| RSS menempel di plafon container, lalu OOM-kill | batas serentak dilepas atau `GOMEMLIMIT` hilang; argon2id 64 MiB per derivasi | `GOMEMLIMIT` di compose/Helm; `crypto.DefaultMaxConcurrent` |
| Logout tidak "menendang" sesi lain | Redis kehilangan penghitung generasi (flush/restart tanpa persistensi) | ADR-020: gateway gagal-tertutup saat Redis tidak terjangkau, tetapi penghitung yang HILANG terbaca sebagai nol — semua token generasi lama kembali sah sampai pengguna login lagi |
| Saga penghapusan `in_progress` lebih dari beberapa menit | satu unit tidak mengonfirmasi | `docs/runbook/account-deletion.md`; `LogOutstandingSagas` di log start-up menyebut sagalah yang menggantung |
| Surel reset tidak tiba | SMTP; di lokal, Mailpit di `:18025` | log `sending the reset mail`; kredensial SMTP |

## Triase dalam lima menit

1. `curl :9102/readyz` — 503 berarti dependensi keras (Postgres/Redis) tidak
   terjangkau saat start; lihat log start-up.
2. Grafana → panel gRPC: `identity.v1.Identity/*` dengan status bukan OK.
   `UNAVAILABLE` pada `Register` saja = profile-svc. `INTERNAL` di semua RPC =
   Postgres.
3. Ambil `trace_id` dari log galat → Tempo. Span `identity.v1.Identity/Register`
   yang anaknya `profile.v1.Profile/CreateEmptyProfile` bergalat = profile.
4. Saga: `SELECT id, status, started_at FROM identity.deletion_sagas WHERE
   status = 'in_progress'` lalu `deletion_confirmations` untuk melihat unit
   mana yang belum menjawab.

## Cara pulih

- **Proses mati**: nyalakan lagi. Relay outbox dan konsumen konfirmasi
  melanjutkan dari offset dan baris yang belum terkirim; tidak ada yang perlu
  diulang manual.
- **Redis kehilangan data**: tidak ada yang bisa "dipulihkan" — penghitung
  generasi adalah keadaan yang boleh hilang menurut ADR-020, dengan harga yang
  disebut di tabel. Bila ada kecurigaan token dicuri, minta pengguna login
  ulang (menaikkan generasi) — atau putar kunci tanda tangan, yang mencabut
  SEMUA token sekaligus.
- **Saga macet**: prosedur di `docs/runbook/account-deletion.md` — tutup
  sebagai `failed` supaya pengguna bisa mencoba lagi; jangan menghapus baris
  pengguna manual.

## Yang JANGAN dilakukan

- Jangan menurunkan parameter argon2id (`DefaultParams`) untuk mempercepat
  login. Hash yang tersimpan membawa parameternya sendiri, jadi penurunan
  hanya memperlemah akun BARU — diam-diam.
- Jangan menyalin `JWT_SIGNING_KEY` ke unit lain. Hanya identity-svc yang
  boleh mencetak token (ADR-020).

## Metrik yang membuktikan pulih

`rpc_server_call_duration_seconds{rpc_method=~"identity.*"}`: status `OK`
kembali mendominasi; `Register` p95 kembali ke ratusan milidetik, bukan detik.
Jumlah saga `in_progress` turun ke nol.
