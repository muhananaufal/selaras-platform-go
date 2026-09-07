# RFC-999 — Retrospektif: yang berhasil, yang gagal, dan biayanya

Penutup untuk RFC-000. Ditulis 2026-09-07 setelah F9 selesai. Setiap klaim
di sini merujuk ke berkas yang memuat buktinya; yang tidak punya bukti
ditulis sebagai yang tidak punya bukti.

## 1. Apa yang dijanjikan, apa yang terjadi

RFC-000 memecah satu monolit Laravel menjadi sembilan unit Go dengan tiga
taruhan: outbox transaksional untuk konsistensi, skema-per-service untuk
isolasi, dan Kafka sebagai jalur event. Empat belas kriteria selesai di
master plan §3 adalah ukurannya.

| # | Kriteria | Status | Bukti |
| :--- | :--- | :--- | :--- |
| 1 | 32 endpoint di gateway, tervalidasi kontrak | ✅ | `api/openapi/edge-v1.yaml`, `vacuum lint` di CI |
| 2 | Paritas SCORE2 pada seluruh golden vector | ✅ | 288 vektor, `internal/assessment/domain/score` |
| 3 | Test + CI hijau, lint bersih, nol TODO/kredensial/`interface{}` | ✅ lokal, **⚠️ CI di GitHub baru berjalan sekali** (2 Sep, `develop`: build+lint+kontrak gRPC+secret scan hijau, kontrak REST merah - kini lulus lokal; commitlint merah karena aturan yang tidak cocok praktik, disesuaikan) | 47 paket hijau, `golangci-lint` 0 isu; CI hanya terpicu di `main`/`develop` |
| 4 | Satu perintah menyalakan sistem, satu lagi menjalankan k6 | ✅ | `task up:full` / `task k3d:all`, `task k6 -- read` |
| 5 | Trace satu request menembus ≥ 3 unit, utuh di Tempo | ✅ | `docs/observability.md`: 6 span, 3 unit |
| 6 | Job LLM dua kali dengan kunci sama → satu hasil | ✅ | `internal/llmworker` (F3), e2e |
| 7 | Broker dimatikan paksa, nol event hilang | ✅ | `test/chaos/broker.md`: 18 tertahan, 0 tersisa, 8/8 selesai |
| 8 | Penghapusan akun menyapu seluruh unit | ✅ | `cmd/deletion-verify` 14 probe, e2e |
| 9 | Angka k6 Go tersanding Laravel | **❌ separuh** | Go lengkap; **Laravel tidak pernah diukur** (B2-07) |
| 10 | RFC penutup | ✅ | berkas ini |
| 11 | S1–S11 ditutup dengan test | ✅ | `test/acceptance` |
| 12 | D1–D12 punya test penerimaan bernama | ✅ | `test/acceptance`; **D6 ternyata belum dijaga** dan ditambahkan; **D3 ternyata tidak pernah ditegakkan** (B27) dan diperbaiki di gerbang keluar |
| 13 | Autoscaling terbukti di bawah beban, batas k3d dinyatakan | ✅ | `docs/performance-report.md` §autoscaling |
| 14 | Backup dipulihkan, bukan diasumsikan | ✅ | `docs/runbook/restore-drill.md`: 29 detik, e2e hijau sesudahnya |

Dua belas setengah dari empat belas. Yang setengah dan yang tanda seru
dibahas di §3.

## 2. Yang berhasil — dan mengapa

**Outbox transaksional (ADR-004) tidak pernah menjadi masalah.** Dari F3
sampai chaos F9-12, tidak ada satu pun event yang hilang, dan tidak ada
kode yang perlu berubah untuk itu. Harganya adalah relay satu detik di
setiap unit dan latensi asinkron ~140 ms + ~880 ms yang kini terlihat span
demi span di Tempo. Itu harga yang diketahui, bukan harga yang mengejutkan.

**Skema-per-service yang ditegakkan basis data (ADR-006).** Tidak ada satu
pun "join lintas unit yang tidak sengaja" selama sembilan fase, karena
Postgres menolaknya. PgBouncer, pemelihara partisi, dan pemulihan dari
backup semuanya harus menghormati kepemilikan per peran — dan tiga-tiganya
menemukan cacatnya sendiri karena aturan itu (partisi milik admin, `--no-owner`
saat restore). Aturan yang menggigit adalah aturan yang bekerja.

**Chaos dan trace menemukan cacat yang test tidak temukan.** Empat cacat
nyata muncul hanya karena sesuatu dijalankan sungguhan:
- konsumen yang berputar selamanya pada hasil milik entitas yang sudah
  dihapus (B22) — terlihat satu menit setelah Tempo menyala;
- gateway yang menggantung, bukan gagal, saat satu service mati (F9-13) —
  tidak ada tenggat di panggilan lintas unit;
- scale-to-zero yang tidak bisa bangun (B24) — metrik lag diterbitkan oleh
  yang seharusnya dibangunkan;
- Kafka yang menulis ke `/tmp` (B23) — chaos `kill`+`start` lulus dan tetap
  tidak menangkapnya;
- slug analisis yang tidak pernah diresolusi (B27) — F4-06 berstatus ✅
  selama lima fase; test penerimaan D3 yang menemukannya, dan itu pun baru
  setelah test D2-nya sendiri dikoreksi;
- compose yang menyala tanpa topic (B28) — gerbang keluar F9 merah di tiga
  suite tanpa satu baris kode yang salah.
Keenamnya punya test atau penjaga sekarang. Tidak satu pun bisa ditulis
sebelum kejadiannya.

**Penyedia LLM palsu dengan mode gangguan.** Seluruh jalur percobaan ulang,
mati, dan 202-yang-tetap-instan dibuktikan tanpa satu pun panggilan Gemini
(`test/chaos/llm.md`). Biaya token nol selama sembilan fase.

**Mutasi sebagai kebiasaan.** Sekitar lima puluh kali sepanjang F6–F9
sebuah test dibuat merah dengan mutasi sebelum dipercaya. Beberapa
mutasi bertahan dan menyingkap test yang tidak menguji apa-apa (pemeriksaan
enum sisi baca, penjaga ganda yang berlebihan). Praktik ini murah dan
menangkap kelas kegagalan yang review tidak tangkap.

## 3. Yang gagal, atau belum

**Baseline Laravel tidak pernah diukur (kriteria 9).** B2-07 menuntut
Laravel dan MySQL menyala di mesin yang sedang dipakai bekerja, dan itu
keputusan pemilik mesin. Akibatnya SLO (F9-11) diturunkan dari Go sendiri
(5 × p95 terukur) dan dinyatakan provisional. Ini kegagalan RENCANA, bukan
kegagalan sistem: rencana menaruh pengukuran yang menyentuh lingkungan orang
lain di jalur kritis tanpa alternatif.

**CI di GitHub baru berjalan sekali, di `develop`, lima hari lalu.** Pemicunya
hanya push ke `main`/`develop` dan PR, sementara seluruh F2-F9 hidup di satu
feature branch - jadi job unit yang hanya memigrasi empat skema sejak F4
tidak pernah ketahuan merah di runner. Diperbaiki di F9-17 dan dibuktikan
lokal; pembuktian di runner menunggu merge ke `develop`. Sembilan fase tanpa
CI luar adalah hutang yang tidak terlihat justru karena semuanya berjalan di
satu laptop.

**Pipeline CD belum pernah men-deploy ke cloud.** `cd.yml` teruji di
bagian yang bisa diuji di k3d (Dockerfile, chart, Job migrasi, urutan);
kubeconfig, registri, dan `--atomic` di klaster sungguhan adalah rancangan.

**Ambang HPA bukan turunan SLO (ADR-014 aturan 2).** 60 % adalah angka
awal. Menurunkannya dari SLO menuntut kurva utilisasi-vs-latensi pada node
yang tidak berbagi CPU dengan pemberi bebannya; di satu node k3d, k6 dan
tujuh service bersaing untuk empat core yang sama, dan p95 5 detik pada
60 VU adalah bukti kejenuhan node, bukan bukti ambang. Aturan 4 (nyatakan
batasnya) dipenuhi; aturan 2 tidak.

**Konsumen tidak pulih dari topic yang dibuat ulang (B26)** tanpa restart.
Skenario ini seharusnya tidak terjadi di produksi, tetapi "seharusnya tidak"
adalah kalimat yang RFC-000 janjikan untuk tidak dipakai. Belum diperbaiki.

**Token LLM baru terukur sebagian.** FinOps menghitung biaya per pekerjaan
dari ukuran templat ÷ 4 sampai gerbang keluar; metrik `llm_tokens_total`
kini membaca `usageMetadata` penyedia, dan larian nyata pertama
(gemini-3.8-flash, kunci tingkat gratis 20 permintaan/hari) mengukur dua
dari enam templat sebelum kuotanya habis — sekaligus menyingkap B30 (jeda
`retryDelay` diabaikan, kuota harian membunuh pekerjaan). Empat templat
menunggu kuota berikutnya; token "pikiran" model 3.x ternyata 3–4× token
jawaban, sesuatu yang taksiran ÷ 4 tidak mungkin tahu.

## 4. Yang akan dikerjakan berbeda

1. **Dorong ke GitHub pada minggu pertama.** Seluruh cacat CI di atas adalah
   akibat "nanti saja". Pipeline yang tidak berjalan tidak ada.
2. **Ukur baseline sebelum menyentuh apa pun**, di mesin yang tidak
   dipakai orang. B2-07 seharusnya pekerjaan hari pertama dengan Laravel
   di container terpisah, bukan pekerjaan yang menunggu izin.
3. **Chaos harus membuat ulang, bukan hanya mematikan.** `docker kill` +
   `start` lulus sementara `--force-recreate` akan menghapus seluruh topic.
   Setiap skenario chaos seharusnya punya varian "hilang, lalu lahir baru".
4. **Tenggat pada setiap panggilan lintas unit sejak F1.** Satu baris opsi
   gRPC yang baru dipasang di F9 setelah chaos menyingkapnya; ADR-002 sudah
   menuliskan alasannya sejak awal.
5. **Sinyal autoscaling tidak boleh datang dari yang di-scale.** Kesalahan
   B24 adalah kesalahan rancangan yang terlihat jelas setelah kejadian dan
   tidak terlihat sebelumnya; aturan umumnya layak masuk ADR-014.
6. **Batas memori dari k8s, bukan dari compose.** Compose tidak membunuh
   container di plafonnya dengan cara yang sama; identity-svc "selamat" di
   192 MiB selama tiga fase karena keberuntungan akuntansi.
7. **Ukur token sejak hari pertama penyedia palsu ada.** Menambahkan
   penghitung token pada `llm.Response` sepele; tanpanya FinOps menebak.

## 5. Biaya sesungguhnya

Yang bisa dihitung dari repositori dan sesi ini:

| Ukuran | Nilai |
| :--- | ---: |
| Commit di `feature/20260902-f1-identity-domain` | 120 (`git rev-list --count HEAD`) |
| ADR | 25 |
| Temuan sistem lama dan milik sendiri | B1–B26, S1–S11, T1–T14, D1–D12 |
| Paket Go dengan test | 47 |
| Runbook | 15 |
| Waktu kalender F1–F9 | 2026-09-02 → 2026-09-07 |
| Biaya token LLM sungguhan selama pengembangan | **$0** — penyedia palsu sepanjang jalan |
| Biaya komputasi | satu laptop; angka cloud yang setara ada di `docs/finops.md` ($48/bulan untuk bentuk yang sama) |

Yang tidak bisa dihitung dari repositori: jam manusia. Sebagian besar
pekerjaan F9 dilakukan oleh agen dengan pemilik yang meninggalkan mesinnya
menyala — dan itu bagian dari eksperimennya. Yang bisa dikatakan jujur:
setiap langkah yang menyentuh kenyataan (chaos, k3d, restore, k6) menemukan
sesuatu yang tidak ditemukan langkah yang hanya menyentuh kode. Rasionya
sekitar satu temuan per skenario. Biaya sesungguhnya dari proyek ini adalah
biaya MENJALANKAN sesuatu berulang kali, dan itu biaya yang layak.

## 6. Yang diminta pada gerbang ini

- Menerima bahwa kriteria 9 tidak terpenuhi dan memutuskan: jalankan B2-07
  (satu sore dengan Laravel di container), atau nyatakan SLO Go sebagai SLO
  final.
- Mendorong repositori dan membiarkan CI berjalan; memperbaiki apa yang
  merah di sana sebelum menyebut kriteria 3 selesai.
- Memutuskan nasib B26 dan pengukuran token: keduanya kecil, keduanya
  hutang yang dinyatakan.

RFC-000 meminta izin memecah sistem. RFC-999 melaporkan bahwa pecahannya
bekerja, bahwa setiap taruhan besarnya terbayar, dan bahwa hutang yang
tersisa punya nama, ukuran, dan berkas yang memuatnya.
