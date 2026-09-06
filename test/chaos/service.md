# Chaos F9-13 — satu service dimatikan

Skrip: `test/chaos/service.sh [nama-service]`. Dijalankan 2026-09-07
terhadap stack compose lokal dengan korban **profile-svc** — unit yang paling
banyak dipanggil sinkron oleh unit lain (gateway, identity saat pendaftaran,
assessment saat penilaian dimulai), sehingga blast radius-nya paling besar
yang mungkin.

## Hasil (setelah perbaikan)

```
04:18:15  MEMATIKAN profile-svc (docker stop -t 0 selaras-profile)
04:18:19  --- saat profile-svc mati
    GET    /me                                      -> 200
    GET    /profile                                 -> 504
    PATCH  /profile                                 -> 504
    GET    /risk-assessments                        -> 200
    POST   /risk-assessments                        -> 201
    GET    /risk-assessments/h7jbmip4wfqlg6z3       -> 200
    GET    /dashboard                               -> 200
    GET    /culinary/hub-data                       -> 200
    GET    /chat/conversations                      -> 200
    GET    /coaching/programs/tidak-ada             -> 404
    POST   /register                                -> 201
04:18:32  MENYALAKAN profile-svc kembali
04:18:35  GET /profile kembali 200 setelah 2 detik
```

Sebelum dan sesudah gangguan seluruh baris identik dengan keadaan sehat
(200/201/404 sesuai endpointnya).

## Blast radius

| Ikut mati | Bertahan | Mengapa bertahan |
| :--- | :--- | :--- |
| `GET /profile`, `PATCH /profile` → **504** dalam 5 detik | `GET /me` | dijawab dari klaim token, tanpa memanggil siapa pun |
| | `POST /risk-assessments` → 201 | assessment membaca usia dan jenis kelamin dari **cache profil lokalnya** yang diisi event `profile.updated` (ADR-007), bukan dengan memanggil profile-svc |
| | `POST /register` → 201 | identity mencoba membuat profil kosong lewat gRPC, gagal, mencatatnya, dan tetap mendaftarkan akunnya — profil dibuat saat pengguna mengisinya (B7) |
| | dashboard, riwayat penilaian, chat, coaching, nutrition | tidak pernah memanggil profile-svc; bahasa untuk panduan menu juga dari cache event |

Dua endpoint dari sepuluh. Itu ukuran kopling sinkron yang tersisa setelah
ADR-007, dan keduanya memang milik unit yang mati.

## Yang ditemukan, dan diperbaiki, karena skenario ini

Larian PERTAMA (image sebelum perbaikan) berhenti di baris kedua:

```
04:09:53  --- saat profile-svc mati
    GET    /me                                      -> 200
    GET    /profile                                 -> TIMEOUT(28)
```

`GET /profile` **tidak menjawab apa pun** — curl menyerah setelah sepuluh
detik. Gateway tidak menjawab 503; ia menggantung. Sebabnya: klien gRPC yang
alamat tujuannya baru saja hilang tetap berada dalam keadaan menyambung, dan
RPC tanpa tenggat menunggu selama itu (connect timeout grpc-go 20 detik).
Tidak ada satu pun panggilan lintas unit yang membawa tenggat.

Perbaikan: `rpc.WithUpstreamDeadline` (commit `fix(rpc): batas waktu per
panggilan gRPC`) membungkus setiap panggilan unary tanpa tenggat dengan
`DefaultUpstreamTimeout`, dipasang di ketiga tempat unit memanggil unit lain.
Test dengan dialer yang tidak pernah menyambung: DeadlineExceeded dalam
~300 ms; mutasi tanpa tenggat menunggu 20 detik (merah disaksikan).

Larian KEDUA (tenggat 10 detik) masih menunjukkan `GET /profile` habis di
curl `--max-time 10` — tenggatnya sama panjang dengan kesabaran klien — lalu
`PATCH /profile` → 503 karena klien gRPC kini sudah tahu tujuannya mati.
Tenggat diturunkan ke **lima detik**; larian ketiga di atas menjawab **504**
untuk keduanya. Lima detik masih sepuluh kali RPC terlama yang pernah
diukur (Register, p99 < 0,5 s).

Dua kegagalan ini tidak akan ditemukan test unit mana pun: kedua ujungnya
benar sendiri-sendiri. Ia hanya terlihat dengan mematikan sesuatu sungguhan.

## Pemulihan

Dua detik dari `docker start` sampai `GET /profile` 200 — waktu proses
menyala, menyambung ke Postgres, dan menyatakan siap. Tidak ada yang perlu
disentuh di unit lain; koneksi gRPC gateway menyambung ulang sendiri.

## Batas skenario ini

- `docker stop -t 0` mengirim SIGTERM lalu SIGKILL: service tidak sempat
  merapikan apa pun, tetapi Docker sempat melepas alamat jaringannya. Node
  yang lenyap seluruhnya (alamat tetap ada, tidak ada yang menjawab)
  berperilaku lain: SYN tidak dijawab, dan tenggat 5 detik itulah yang
  menyelamatkan pemanggil. Skenario itu diuji oleh `TestACallToAServiceThat
  NeverAnswersEndsWithinTheDeadline`, bukan di sini.
- Korban lain (`bash test/chaos/service.sh assessment-svc`) mengikuti tabel
  yang sama dengan endpoint yang berbeda; yang paling bernilai diuji dulu.
