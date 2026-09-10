# Flow Reel — showcase interaktif selaras-platform-go

Animasi langkah demi langkah tentang bagaimana sebuah permintaan bergerak melintasi gateway, service gRPC,
outbox, Kafka, dan llm-worker — dengan kartu kode yang menyorot baris sumber yang sebenarnya, dan cabang
"bagaimana kalau" (broker mati, kuota habis, dua pod berebut baris yang sama).

Semua berkas statis, tanpa build step. Buka `index.html` di peramban (langsung dari disk, atau lewat server
statis apa pun). Bahasa antarmuka bisa diganti Indonesia ↔ Inggris dari menu `⋯`; kolom Inggris masih
diisi bertahap.

## Isi folder

| Berkas | Peran |
| :--- | :--- |
| `index.html` | hub: daftar 23 reel (yang sudah jadi bisa diputar, sisanya tertaut ke rencana) |
| `reel.js`, `reel.css` | mesin bersama: kamera, langkah, cabang, kartu kode, keterangan, dua bahasa |
| `manifest.js` | daftar reel, ADR, runbook, dan dokumen yang dirujuk; dibangun oleh `tools/build-manifest.js` |
| `reel-*.html` | satu reel = data saja (`REEL`, `CODE`, `NODES`, `EDGES`, `STEPS`, `BRANCHES`, …) |
| `runtime.html` | peta sistem: unit, port, topic, skema, dan bagaimana semuanya tersambung |
| `plan.html` | rencana tiga lapisan (cerita, alur, mekanisme) dan status tiap reel |
| `blueprint.md` | cara menulis reel baru: konvensi data, kamera, keterangan, gerbang mutu |

Reel yang sudah jadi:

- `reel-alur-04-personalisasi.html` — lapisan 2, alur 04: permintaan personalisasi laporan dari HTTP sampai
  hasil LLM kembali ke dasbor.
- `reel-mekanisme-01-outbox.html` — lapisan 3, M1: outbox transaksional dan relay, mode debugger.
- `reel-mekanisme-02-idempotensi.html` — lapisan 3, M2: kunci idempotensi tiga lapis dan `processed_messages`.

## Cuplikan kode

`CODE.*.src` di tiap reel adalah cuplikan dari repo ini, **diringkas tangan** (statement digabung, baris
kosong dibuang) supaya muat di kartu. Rujukan `file: 'path:baris'` menunjuk ke baris pertama cuplikan di
sumber. Komentar di dalam cuplikan (`<c>…</c>`) mengikuti komentar sumbernya; baris kunci ditandai `<b>`.

## Gerbang mutu

Jalankan dari folder ini. Semua hanya butuh Node; `tonton.js` juga butuh Chrome.

```
node tools/validate-reel.js reel-mekanisme-01-outbox.html   # rujukan, proof = nama test yang ada, kesinambungan, simulasi ZOOM
node tools/sync-snippets.js  reel-mekanisme-01-outbox.html . # cuplikan masih cocok dengan sumber; rujukan baris diperbarui
node tools/tonton.js         reel-mekanisme-01-outbox.html   # Chrome headless memutar jalur utama + tiap cabang, memeriksa invarian tampilan
node tools/build-manifest.js                                 # setelah menambah reel atau dokumen rujukan
node tools/audit-cakupan.js                                  # butir repo yang belum disebut rencana maupun manifes
```

`sync-snippets.js` dan `validate-reel.js` membaca repo lewat path relatif (`../../..`); `tonton.js` mencari
Chrome di lokasi bawaan Windows dan bisa diarahkan lewat `CHROME=<path>`. Laporan dan tangkapan layar
`tonton.js` ditulis ke folder sementara sistem, bukan ke repo.
