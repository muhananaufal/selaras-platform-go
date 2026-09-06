# Laporan kinerja — k6 terhadap platform Go

F9-10 dan F9-11. Seluruh angka diukur 2026-09-07 pada stack compose lokal;
tidak ada yang diperkirakan.

## Yang tidak ada di laporan ini, dan mengapa

**Kolom Laravel kosong.** B2-07 (baseline k6 terhadap Laravel) tidak pernah
dijalankan: ia menuntut Laravel dan MySQL menyala di mesin yang sama, dan itu
menyentuh lingkungan kerja yang sedang dipakai pemiliknya. Keputusan
menjalankannya milik pemilik mesin, bukan milik rencana ini. Konsekuensinya
disebut terbuka:

- Kriteria selesai #9 ("angka Go tersanding dengan angka Laravel") **tidak
  terpenuhi** sampai B2-07 dijalankan. Skenario di `test/k6/scenarios/`
  menargetkan endpoint yang bentuknya identik di kedua sistem (ADR-005), jadi
  begitu Laravel diukur, tabelnya tinggal diisi.
- SLO di bawah **diturunkan dari pengukuran Go sendiri**, bukan dari Laravel
  seperti yang direncanakan B2-08. Cara penurunannya dinyatakan supaya bisa
  ditinjau, bukan dibungkus sebagai hasil pengukuran yang tidak ada.

## Lingkungan

| | |
| :--- | :--- |
| Mesin | Windows 11, 4 processor logis, 15,7 GB RAM; Docker di WSL dengan plafon 8 GB |
| Yang menyala | 9 unit Go, Postgres, Kafka, Redis, Mailpit, **dan** stack observability penuh (collector, Tempo, Prometheus, Loki, Alloy, Grafana) |
| Beban | k6 1.8.1 sebagai container di jaringan compose, memanggil `edge-gateway:8080` |
| Penyedia LLM | `fake` — skenario ini sengaja tidak menyentuh jalur LLM |
| Sampling trace | 100% (bawaan SDK); setiap permintaan di bawah ini juga menghasilkan span |

k6 dan seluruh sistem berbagi empat core yang sama. Angka di bawah adalah
angka **mesin pengembangan yang sedang menjalankan segalanya sekaligus**,
bukan angka kapasitas.

## Hasil

Tiga skenario, masing-masing ramp 30 detik → tahan → turun 15 detik, satu
detik jeda per iterasi. Latensi dari k6 (`http_req_duration`); rincian per
rute dari Prometheus (`histogram_quantile` atas
`http_server_request_duration_seconds` di gateway, jendela 2 menit).

### Baca — 20 VU, 5 GET per iterasi

| | Go | Laravel (B2-07) |
| :--- | ---: | :---: |
| Permintaan | 8.200 dalam 105 s (77,7/s) | belum diukur |
| Gagal | **0** | |
| p50 | 2,16 ms | |
| p95 | 4,37 ms | |
| p99 | 11,03 ms | |
| maks | 358 ms | |

| Rute | p50 | p95 | p99 |
| :--- | ---: | ---: | ---: |
| `GET /me` | 2,5 ms | 4,8 ms | 5,0 ms |
| `GET /profile` | 2,6 ms | 4,9 ms | 10,6 ms |
| `GET /risk-assessments` | 2,6 ms | 4,9 ms | 9,6 ms |
| `GET /dashboard` | 2,6 ms | 4,9 ms | 9,7 ms |
| `GET /culinary/hub-data` | 2,6 ms | 4,9 ms | 12,6 ms |

Lima rute yang menyentuh lima unit berbeda (identity, profile, assessment,
dashboard, nutrition) punya p95 yang nyaris identik. Itu bukan kebetulan:
semuanya satu gRPC ke satu service yang membaca satu tabel berindeks. Bentuk
sistemnya seragam, dan latensinya ikut seragam.

### Tulis — 10 VU, 3 penulisan per iterasi

| | Go | Laravel (B2-07) |
| :--- | ---: | :---: |
| Permintaan | 2.414 dalam 105 s (22,8/s) | belum diukur |
| Gagal | **0** | |
| p50 | 7,85 ms | |
| p95 | 14,46 ms | |
| p99 | 25,98 ms | |
| maks | 325 ms | |

| Rute | p50 | p95 | p99 |
| :--- | ---: | ---: | ---: |
| `PATCH /profile` | 8,1 ms | 21,6 ms | 24,7 ms |
| `POST /risk-assessments` (hitung SCORE2 + outbox, satu transaksi) | 7,9 ms | 20,6 ms | 32,0 ms |
| `PATCH /culinary/preferences` | 7,8 ms | 18,0 ms | 24,2 ms |
| `POST /register` (sekali per VU) | 175 ms | **375 ms** | 475 ms |

Di sisi gRPC, `identity.v1.Identity/Register` p95 375 ms sementara RPC
lain 5–17 ms. Itu argon2id: 64 MiB dan tiga iterasi per hash, dengan sengaja
(RFC 9106). Pendaftaran memang lambat, dan harus lambat.

### Campuran — 16 VU pembaca + 4 VU penulis

| | Go | Laravel (B2-07) |
| :--- | ---: | :---: |
| Permintaan | 5.806 dalam 135 s (42,8/s) | belum diukur |
| Gagal | **0** | |
| p50 | 3,28 ms | |
| p95 | 9,74 ms | |
| p99 | 14,2 ms | |
| maks | 1,17 s | |

## Yang ditemukan karena mengukur

**RSS identity-svc menempel di plafonnya.** Saat skenario tulis dan campuran
berjalan, `docker stats` menunjukkan identity-svc di **191,8 / 192 MiB** —
tepat di batas container — sementara unit lain 15–28 MiB. Sebabnya: setiap
argon2id memegang 64 MiB, pendaftaran dari beberapa VU berjalan bersamaan, dan
GC Go tidak mengembalikan memori sebelum menabrak batas cgroup. Container
tidak sempat di-OOM-kill selama pengukuran ini, tetapi itu keberuntungan
dengan sepuluh VU, bukan jaminan dengan seratus.

Dua perbaikan, keduanya di commit yang sama dengan laporan ini:

1. Hasher membatasi derivasi serentak ke **dua** (`crypto.DefaultMaxConcurrent`),
   untuk `Hash` maupun `Verify`. Puncak memori hashing menjadi ~128 MiB dan
   pemanggil ketiga menunggu — ratusan milidetik, jauh lebih murah daripada
   proses yang mati di tengah pendaftaran orang lain. Test-nya menyuntik
   fungsi derivasi dan mengukur puncak serentak; mutasi "batas 64" membuatnya
   merah.
2. `GOMEMLIMIT=160MiB` di compose untuk identity-svc, supaya GC mulai bekerja
   keras sebelum plafon, bukan sesudahnya. Di k3d angka ini terbukti masih
   kurang — cgroup Kubernetes membunuh pod di 192 MiB dua kali saat suite e2e
   mendaftar akun berurutan (B25) — dan keduanya dinaikkan menjadi
   `GOMEMLIMIT=256MiB` dengan plafon 320 MiB, di chart dan compose.

Angka ini juga masuk ke F9-26 (koneksi dan memori per replika) dan FinOps:
identity-svc adalah unit termahal per permintaan, dan bukan karena Postgres.

**Maksimum 1,17 s di skenario campuran** adalah satu permintaan; p99-nya
14 ms. Dengan sampling trace 100%, satu permintaan pertama setelah collector
menerima batch besar bisa terhambat — belum diselidiki, dan dinyatakan
begitu.

## SLO (F9-11) — diturunkan dari angka di atas

Ambang di `test/k6/lib/slo.js` membuat k6 keluar dengan galat bila dilanggar,
dan `task k6 -- <skenario>` adalah perintah yang menjalankannya di CI.

Cara penurunannya: **lima kali p95 terukur, dibulatkan ke atas ke angka yang
mudah dibaca**. Lima, karena yang ingin ditangkap adalah regresi satu orde —
query yang kehilangan indeks, panggilan gRPC yang menjadi dua — bukan
gangguan mesin pengembangan yang sedang membangun image di sebelahnya.
Faktor yang lebih ketat akan membuat CI merah karena cuaca; yang lebih
longgar tidak menangkap apa pun.

| Skenario | Terukur p95 | SLO p95 | SLO p99 | Gagal |
| :--- | ---: | ---: | ---: | ---: |
| Baca | 4,4 ms | **< 25 ms** | < 60 ms | < 1% |
| Tulis (tanpa pendaftaran) | 14,5 ms | **< 75 ms** | < 150 ms | < 1% |
| Campuran | 9,7 ms | **< 50 ms** | < 100 ms | < 1% |
| `POST /register` | 375 ms | **< 1.500 ms** | — | < 1% |

Pendaftaran diberi ambang sendiri (empat kali, bukan lima, karena batas
serentak yang baru akan MENAMBAH antrean di bawah beban dan angkanya belum
diukur ulang), supaya argon2id yang memang lambat tidak menyeret ambang rute
lain ke atas.

Ambang ini **provisional**. Ia dinyatakan dari satu mesin, satu hari, tanpa
pembanding Laravel. Saat B2-07 dijalankan, tabel ini dan `slo.js` ditinjau
bersama — dan kalau Laravel ternyata lebih cepat di sebuah rute, itu ditulis
di sini apa adanya.

## Cara mengulang

```
task up:full            # seluruh sistem, termasuk observability
task k6 -- read
task k6 -- write
task k6 -- mixed
```

Rincian per rute: Grafana → dashboard "Selaras — ikhtisar platform", atau
kueri Prometheus yang disebut di atas.

## Autoscaling di k3d (F9-23, F9-24, F9-25)

Diukur 2026-09-07 pada klaster k3d satu node (`task k3d:all`), stack yang
sama dengan compose ditambah metrics-server bawaan k3s dan KEDA 2.20.

### Scale-to-zero dan cold-start (F9-23)

llm-worker turun ke **0 replika** 60 detik setelah antrean kosong
(`cooldownPeriod`). Lalu satu pesan chat dikirim ke sistem yang workernya
tidak ada:

```
05:57:37.286  llm-worker readyReplicas= (kosong)
05:57:37.423  pesan dikirim, percakapan 7deeqcwtw7pjmp2u
05:57:45.752  balasan tiba setelah 8348 ms
```

**Cold-start job pertama: 8,3 detik**, terurai kira-kira menjadi polling
KEDA (≤ 10 s, rata-rata 5 s), pod dijadwalkan dan menyala (~1–2 s), relay
outbox (≤ 1 s), dan pekerjaannya sendiri (milidetik dengan penyedia `fake`).
Dengan pekerjaan yang sudah punya worker, balasan tiba dalam ~1 s (F7).
Delapan detik adalah harga scale-to-zero, dan itulah alasan
`values-cloud.yaml` memakai `minReplicaCount: 1` — cold-start ini layak di
lingkungan lokal, tidak layak untuk pengguna sungguhan (ADR-014 aturan 5).

Dua hal yang ditemukan karena mencobanya: trigger berbasis metrik
`kafka_consumer_lag` dari Prometheus **tidak bisa membangunkan worker** —
metriknya diterbitkan worker, dan worker yang tidak ada tidak menerbitkan
apa pun (B24); diganti scaler `kafka` KEDA yang membaca lag dari broker. Dan
startup probe llm-worker gagal 404 karena ia hanya punya `/healthz` (kini
ada `/readyz`).

### Naik lalu turun kembali (F9-24)

k6 skenario baca dengan **60 VU** selama 4 menit terhadap
`http://127.0.0.1:28080` (NodePort edge), HPA CPU target 60 % dari request.
Jumlah replika disampel setiap 10 detik:

| Waktu | edge | identity | profile | assessment | dashboard | nutrition |
| :--- | ---: | ---: | ---: | ---: | ---: | ---: |
| 05:58:20 (sebelum beban) | 1 | 2¹ | 1 | 1 | 1 | 1 |
| 05:59:22 (+45 s beban) | **2** | 1 | **2** | 1 | 1 | 1 |
| 05:59:32 | **3** | **3** | 2 | **2** | **3** | **3** |
| 05:59:56 | **4** (maks) | 3 | 2 | 2 | 3 | 3 |
| 06:00:18 | 4 | 3 | **3** | **3** | 3 | 3 |
| 06:01:23 (dataran) | 4 | 3 | 3 | 3 | 3 | 3 |
| 06:02:43 (beban berakhir) | 4 | 3 | 3 | 2 | 3 | 3 |
| 06:03:17 | 4 | 3 | 2 | 2 | 2 | 3 |
| 06:04:19 | 3 | 3 | 1 | 1 | 1 | 2 |
| 06:06:40 | 2 | 2 | 1 | 1 | 1 | 1 |

¹ identity masih 2 dari pendaftaran suite e2e sebelumnya, sedang turun.

Event HPA menyebut alasannya persis: naik karena
`cpu resource utilization (percentage of request) above target`, turun
karena `All metrics below target`, satu pod per menit sesuai kebijakan
`scaleDown` di chart. **Replika naik lalu turun kembali, terekam** — bukan
`kubectl get hpa` sesaat (ADR-014 aturan 3).

Angka bebannya sendiri:

| | Nilai |
| :--- | ---: |
| Permintaan | 29.290 dalam 4 menit (121/s) |
| Gagal | **5,24 %** (1.536) |
| p50 / p95 / p99 | 17 ms / **5 s** / 5,5 s |
| `POST /register` p95 | 5,1 s |

Lima detik adalah tenggat upstream gateway (`rpc.DefaultUpstreamTimeout`):
pada 60 VU, node empat core ini **jenuh** — k6, edge, tujuh service,
Postgres, Kafka, dan k3s berebut CPU yang sama — dan permintaan yang tidak
sempat dilayani berakhir 504, bukan menggantung. Itu perilaku yang
dirancang (chaos F9-13), tetapi angkanya menunjukkan ambang SLO baca
(p95 < 25 ms) dilanggar jauh sebelum HPA mencapai maksimum.

### Batas jujur k3d (F9-25, ADR-014 aturan 4)

Pada satu node, replika kedua sampai keempat **berbagi CPU host yang sama**
dengan replika pertama, k6, dan seluruh dependensi. Menambah replika tidak
menambah core. Yang dibuktikan di atas adalah **mekanismenya**: metrik
dibaca, ambang dilewati, replika ditambah, beban turun, replika dikurangi
dengan kebijakan yang dinyatakan. Yang TIDAK dibuktikan adalah bahwa
kapasitas bertambah — dan angka p95 5 s di atas adalah bukti bahwa ia
memang tidak bertambah di sini. Grafik replika naik tanpa kalimat ini adalah
klaim yang menyesatkan; kalimat ini adalah bagian dari hasilnya.

Ambang HPA 60 % adalah angka awal, bukan turunan SLO (ADR-014 aturan 2
belum terpenuhi): menurunkannya dari SLO menuntut kurva "utilisasi vs p95"
pada node yang tidak berbagi CPU dengan pemberi bebannya — pengukuran yang
hanya bermakna di klaster dengan lebih dari satu node. Dicatat di RFC
penutup sebagai hutang yang tidak bisa dibayar di laptop ini.
