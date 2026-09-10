# Flow Reel — blueprint untuk reel berikutnya

Acuan: `reel-alur-04-personalisasi.html` (alur 04). Sejak 2026-09-09 mesin dipisah: **`reel.js`** (mesin + kerangka DOM), **`reel.css`**,
**`manifest.js`** (daftar 23 reel, ADR, runbook, dokumen; dibangun `tools/build-manifest.js`), dan tiap reel HTML hanya berisi **data**
(`REEL`, `CODE`, `NODES`, `EDGES`, `RAILS`, `PANEL`, `STEPS`, `BRANCHES`, `CHAPTERS`, `PAYLOAD`, `ZOOM`) lalu memuat `manifest.js` dan `reel.js`.
Folder: `docs/reel` di repo selaras-platform-go. Hub: `index.html`. Rencana lengkap: `plan.html`. Semua tetap offline tanpa build step.

## Dua bahasa

Setiap teks data boleh string (Bahasa Indonesia) atau `{ id, en }`; `T()` di `reel.js` memilih sesuai `localStorage.selaras-lang`
(tombol Bahasa di menu ⋯ atau tombol `L`). Kolom `en` boleh kosong dulu: `T()` jatuh ke `id`.

## Identitas reel

```js
const REEL = { id: '04', tier: 2, num: { id: '04 · alur', en: '04 · flow' }, title: {…}, kicker: {…}, intro: {…} };
```
`tier` mewarnai bab (1 hijau-laut, 2 amber, 3 ungu). Langkah boleh membawa `down: 'M1'` atau 'M1/3' → tautan turun ke reel mekanisme
(aktif hanya bila reel itu sudah ada di manifes). Selesai menonton dicatat ke `localStorage.selaras-watched`; hub menampilkannya.

## Menambah reel baru

1. Salin `reel-alur-04-personalisasi.html`, ganti `REEL` dan data. 2. Tambah/ubah entri di `tools/build-manifest.js` bila judulnya berubah,
lalu `node tools/build-manifest.js` (status 'jadi' otomatis dari keberadaan berkas). 3. `node tools/audit-cakupan.js` untuk memastikan
tidak ada bagian repo yang belum disebut. 4. Validasi + render statis seperti di bawah.

## Anatomi berkas

| Bagian | Isi | Diubah per alur? |
| :--- | :--- | :--- |
| `<style>` | token tema (terang/gelap), layout layar penuh, semua animasi (di bawah `prefers-reduced-motion: no-preference`) | tidak |
| `<div class="app">` | kanvas SVG, bilah atas (judul · linimasa · waktu · putar · ⋯), kartu pembuka, pilihan cabang, penutup, kredit, caption tengah | hanya judul & teks pembuka |
| `CODE` | cuplikan kode Go asli: `{ file: 'path:baris', src: '…' }`, baris kunci dibungkus `<b>`, komentar `<c>` | ya |
| `NODES` | komponen: `{ x, y, label, sub, icon }` dalam viewBox 1600×900; kiri klien, tengah service, kanan penyimpanan/penyedia | ya |
| `RAILS` | rel Kafka: `{ y, label, n }` (n = jumlah partisi; n=1 → rel pendek) | ya |
| `EDGES` | jalur: `{ d, kind, lbl:[teks,x,y], a, b }`; kind ∈ http, grpc, db, evt, ext; `a`/`b` = komponen ujung (dipakai kamera) | ya |
| `PANEL` | isi dalam unit yang "terbuka" saat kamera dekat: `{ side:'below'|'above', blocks:[[nama, sub],…] }` | ya |
| `STEPS` | langkah utama (lihat skema di bawah) | ya |
| `BRANCHES` | cabang "bagaimana kalau": `{ label, resume: bool, steps }` disisipkan setelah langkah bertanda `branch: true` | ya |
| `CHAPTERS` + penanda `ch` | bab (kartu judul + lower-third di caption) | ya |
| `PAYLOAD` | bentuk muatan titik: card/jwt/env/doc/spark/ack/err | jarang |
| `ZOOM` | renderer kartu Rincian per jenis (`http`, `db`, `kafka`, …) dari state `S` | tambah bila ada jenis baru |
| `// MESIN` | kamera, titik, keterangan, cincin, suara, musik, narasi, linimasa, cabang, penutup | tidak |

## Skema satu langkah

```js
{ travel: [{ e:'E1', p:'req' }],            // titik berangkat SEBELUM proses (opsional)
  at: 'gateway',                             // komponen tempat proses berjalan (node atau 'rail-<nama>')
  proc: [                                    // keterangan kuning berurutan; string = tanpa blok
    { t: 'Tanya Redis…', b: 2,               // b = indeks blok PANEL yang menyala
      go: [{ e:'E2', p:'get' }, { e:'E2', rev:true, p:'ack' }], // titik berjalan TEPAT saat keterangan ini
      fx: ['row:pg:1', 'slot:jobs:lit', 'tick:pg:2 baris', 'flash:pg'], // efek kanvas
      bad: true },                           // keterangan merah + bunyi gagal
  ],
  done: '✓ JWT sah',                         // keterangan hijau yang tinggal sebentar
  lat: { u: 0.6 } | { s: 4200 },             // ms: u = pengguna menunggu, s = sistem bekerja (ilustratif berskala)
  title, text, refs: ['path', 'ADR-020'],    // caption; refs juga dipakai kredit penutup
  zoom: 'jwt', code: 'auth', branch: true,   // kartu rincian, cuplikan kode, titik cabang
  mut: S => { show(S,'gateway'); edge(S,'E1'); S.focus = 'gateway'; … } } // perubahan state
```

Aturan yang dijaga validator (jalankan sebelum menyerahkan): setiap `e` ada di `EDGES`, `p` ada di `PAYLOAD`,
`b` < jumlah blok panel `at`, `fx` merujuk rel/node yang ada, **titik selalu berangkat dari posisi terakhirnya**
(kecuali pekerja latar seperti relay), dan `mut` + `ZOOM` tiap urutan (utama + tiap cabang) berjalan tanpa galat.

## Resep alur baru (urutan kerja)

1. Baca kode alurnya: handler edge → interceptor → use case → repo/outbox → relay → topic → konsumen. Catat pesan log
   literal dan nama span (otelgin rute, otelgrpc `paket.Service/Method`, konsumen `<topic> process`).
2. Salin berkas, ganti judul/pembuka, susun `NODES`/`EDGES`/`RAILS` (kiri→kanan). Unit gRPC memakai templat panel
   yang sama: interceptor · use case · aturan · transaksi (repo + outbox) · konsumen.
3. Tulis `STEPS` dengan keterangan awam (≤ 45 karakter), satu ide per keterangan, 2–4 keterangan per langkah.
   Perjalanan titik ditaruh di keterangan yang menyebutnya (`go`), bukan di akhir langkah.
4. Isi `lat` dari `docs/performance-report.md`; sebut "ilustratif berskala" di tooltip.
5. Tambah 1–2 cabang di titik yang benar-benar rawan (penyedia luar, broker, DB), dari perilaku kode nyata.
6. Validasi dengan skrip (sintaks + simulasi tiga urutan), lalu render statis `#N` beberapa posisi di Chrome headless.

## Jebakan yang sudah dibayar

- **`transform` CSS menimpa atribut `transform` SVG.** Semua yang dianimasikan pakai dua grup: luar untuk posisi, dalam
  untuk animasi (node, lencana nomor, keterangan, titik). Menggabungkannya membuat elemen melompat ke (0,0).
- **Chrome headless tidak bisa memverifikasi ritme**: rAF nyaris tidak maju dengan `--virtual-time-budget`, dan tanpa
  itu tangkapan diambil saat muat. Verifikasi statis lewat deep-link `#N`; ritme dilihat di browser sungguhan.
- **Web Speech**: `cancel()` lalu `speak()` langsung sering menjatuhkan ucapan; beri jeda 150 ms dan simpan referensi
  utterance. Suara id-ID hanya ada di Chrome online ("Google Bahasa Indonesia"); Windows tidak menyediakannya offline.
- **Audio** baru boleh berbunyi setelah gestur pengguna; musik dan efek menunggu klik/tombol pertama.
- **Caption menutup tepi bawah**: kamera diberi bias ke atas (`fit16`), komponen di tepi bawah tetap butuh pengecekan.
- Menulis tambalan lewat `node -e` dengan string `$('#id')` di dalam kutip ganda shell akan terinterpolasi; tulis
  skrip tambalan ke berkas dulu.

## Lapisan 3: mode debugger (sejak M1/M2, 2026-09-09)

Reel bertingkat 3 (`REEL.tier: 3`) membuka kartu Rincian dan Kode secara bawaan, dan kamera digeser ke kiri saat kartu terbuka
(`sideBias`) supaya subjek tidak tertutup. Tambahan skema data yang dipakai M1/M2:

- `lines: 'FOR UPDATE SKIP LOCKED'` (string yang dicocokkan per baris) atau `[awal, akhir]` → baris itu disorot di kartu kode
  dan digulir ke tengah. `proof: 'TestNamaTest'` atau daftar → chip hijau ✓ di caption; validator memastikan nama test ada di repo.
- Cabang di banyak titik: `branch: ['nama', 'lain']` di langkah, atau `after: <indeks>` di cabang. Tombol pilihan dan tombol
  "Coba: …" di penutup dibangun dari `BRANCHES[n].label`. `fail: true` → musik minor dan tanpa konfeti.
- `jump: true` pada langkah (atau `jump: true` pada satu perjalanan) = titik baru muncul di awal jalurnya (pelaku terpisah);
  `bg: true` = pelaku latar dengan titiknya sendiri (relay kedua), titik utama tidak berpindah. Keduanya hanya dibaca validator.
- fx baru: `unrow:id:n` (padamkan baris), `off:id` / `on:id` (proses mati/hidup, kotak putus-putus merah), `shake:id`.
  Rel: `hot` (slot panas) dan `note` (teks kanan) boleh ditentukan; `RAIL_X` menggeser semua rel (bawaan 560). Lebar rel n ≤ 4 = 160 + 56n.
- Ikon baru: `relay` (sabuk pengangkut), `table` (tabel dengan baris r1–r3), `proc` (layar prompt).
- State generik yang dibaca `paint`: `S.slots[rel]` ('lit' | 'used' | null), `S.rows[node]` (daftar baris menyala), `S.dead` (node mati).
  Reel 04 masih memakai nama lama (`slotJobs`, `ra`, `outbox`) lewat jatuh balik.
- Penghitung latensi disembunyikan otomatis bila tidak ada langkah dengan `lat`.
- Validator: `node tools/validate-reel.js <berkas>` (rujukan, baris kode, proof, refs, kesinambungan titik, simulasi mut+ZOOM tiap urutan).

Temuan saat menulis M2 [fakta:grep repo 2026-09-09]: `SaveResult`/`Result` dan `Sweep` di paket idempotency belum punya pemanggil di
`cmd/` maupun `internal/` selain test; reel menyatakannya apa adanya, bukan menyembunyikannya.

## Kamera saat kartu Rincian terbuka (perbaikan 2026-09-09)

`sideBias(v, b)` menggeser pandangan ke kanan agar subjek tidak tertutup kartu, tetapi geserannya dibatasi kotak subjek `b`:
tepi kiri subjek tidak boleh keluar dari pandangan. Versi pertama menggeser tanpa batas dan memotong subjek di tepi kiri kanvas
(M1 `#1`). Aturan tata letak yang ikut lahir: kotak subjek termasuk panelnya harus berada di dalam kanvas (`BOX(id).x ≥ 0`);
node dengan panel 4 blok butuh `x ≥ 240`. Pemeriksaan regresi: `tools/tonton.js` (lihat komentar di dalamnya),
dijalankan headless untuk M1 `svc`, M2 `gateway`, 04 `klien` → semua PASS pada muat `#1`, setelah next, setelah prev.

## Keterangan (callout) dan label blok (perbaikan 2026-09-09)

- Teks keterangan > 56 huruf dipecah maksimal tiga baris; lebar gelembung diukur dari `getComputedTextLength()`, bukan taksiran
  per huruf; gelembung digeser agar tetap di dalam pandangan kamera dan di kiri kartu Rincian, tangkainya tetap di jangkar.
  Versi lama menaksir 7,6 px/huruf dan memusatkan gelembung pada node, sehingga pernyataan SQL 80–100 huruf di node tepi kiri
  terpotong (M1 `#1`). Harness: `tools/tonton.js` baris "keterangan" (PASS bila gelembung di dalam pandangan dan
  teks ≤ lebar gelembung); `?hold=1` untuk tangkapan layar.
- Label blok panel yang lebih lebar dari bloknya dirapatkan lewat `textLength` (`txService.Update`, `events(q).Write`).
- Kartu Rincian dan chip caption memakai `overflow-wrap: anywhere`; kolom grid tabel/kv diberi `min-width: 0` supaya kunci
  panjang (`llm-worker␟u_91c2␟k-3f1`) melipat, bukan menimpa kolom sebelahnya.
- Kait debug `window.__reelDebug = { callout, view }` ada hanya untuk harness.

## Penonton otomatis (2026-09-09)

`node tools/tonton.js <reel.html> [folder-keluaran]` menjalankan Chrome sungguhan (`--headless=new`, waktu nyata, bukan virtual)
lewat protokol DevTools tanpa dependensi: memutar reel dari kartu pembuka sampai penutup untuk jalur utama dan tiap cabang
(lewat tombol "Coba: …" di penutup), memeriksa invarian tiap 400 ms, menyimpan tangkapan tiap langkah, dan menulis `laporan.md`.
Invarian: gelembung keterangan di dalam kanvas, di bawah bilah atas, di kiri kartu Rincian, teksnya tidak lebih lebar dari gelembung;
label blok muat; subjek fokus di dalam pandangan dan tidak tertutup kartu; kartu/caption tanpa luapan horizontal; tanpa galat konsol.
Masalah dicatat hanya bila bertahan dua sampel berturut-turut, dan pemeriksaan subjek dilewati saat titik sedang berjalan, kartu bab
terbuka, atau kamera mundur ke penutup (keadaan langkah sebelumnya masih tampil).

Aturan kamera yang lahir dari putaran-putarannya (semua di `reel.js`):
- `sideBias(v, b)`: saat kartu Rincian terbuka, subjek ditempatkan di 64 % kiri layar; pandangan diperlebar bila subjek lebih lebar
  dari ruang itu; subjek di tepi kiri tidak menggeser kanvas.
- Ruang atas 100 px di semua pandangan (fokus/proses/perjalanan) untuk gelembung; `fit16` boleh `Y` sampai -140 dan lebar sampai 1900
  (latar titik menutup kanvas yang lebih luas), supaya subjek tinggi (panel di atas node + tabel di bawahnya) tetap punya ruang gelembung.
- Keterangan "selesai" dibuat setelah kamera menetap, di fokus akhir bila berbeda dari tempat prosesnya.
- Saat kamera mundur ke penutup, `body.ended` menyembunyikan kartu Rincian.
Hasil terakhir: M1 5 urutan dan M2 4 urutan sampai penutup, 0 temuan.
