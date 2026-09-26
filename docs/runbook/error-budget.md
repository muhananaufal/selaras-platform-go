# Runbook — kebijakan error budget

SLO bukan angka pajangan: ia menentukan kapan tim boleh merilis fitur dan
kapan tim wajib berhenti untuk memperbaiki reliabilitas. Dokumen ini adalah
kebijakannya; mesin yang menegakkannya disebut di setiap baris.

| Bagian | Isi | Di mana |
| :--- | :--- | :--- |
| Definisi SLO | ketersediaan 99 %, latensi 95 % ≤ 50 ms, periode 30 hari | `deploy/slo/selaras.yml` |
| Aturan turunan | recording rule SLI (5m…30d), burn-rate alert page/ticket | `deploy/compose/observability/slo-rules.yml` (hasil Sloth, jangan disunting) |
| Bukti aturan | tiap alert menyala pada deret yang melanggar dan diam pada yang sehat; budget bertahan melewati jendela sepi | `alerts_test.yml`, job CI `alert rules` |
| Gerbang rilis | `cmd/slo-budget` + `deploy/slo/gate.sh` | langkah `Error budget gate` di `cd.yml`, lokal `task slo:budget` |

## Arti error budget

Target 99 % berarti **1 % request boleh gagal** dalam 30 hari — itulah
anggarannya. `slo:period_error_budget_remaining:ratio` menyatakan sisanya:
`1` belum tersentuh, `0` habis, negatif berarti terlampaui. Rasio 30 hari
dihitung dari event mentah (`rate[30d]`), bukan rata-rata rasio 5 menit,
supaya satu jam tanpa lalu lintas tidak membuat budget `NaN` sebulan
(alasannya di komentar `deploy/slo/selaras.yml`).

## Kebijakan

| Sisa budget | Status | Yang terjadi |
| :--- | :--- | :--- |
| > 0 | normal | rilis berjalan seperti biasa |
| ≤ 0 | **freeze** | rilis fitur berhenti. Yang boleh lewat hanya perbaikan reliabilitas, perbaikan keamanan, dan revert |
| tidak ada data | tanpa vonis | rilis berjalan **dengan peringatan** di log CD |

Selama freeze:

1. Pekerjaan berikutnya adalah penyebab terbesar pemakaian budget — cari
   dengan `slo:sli_error:ratio_rate1d` per SLO, lalu pecah per `http_route`
   di Grafana.
2. Setiap insiden yang memakan lebih dari seperempat budget bulanan
   mendapat postmortem tertulis, lengkap dengan tindakan pencegahannya.
3. Freeze selesai sendiri ketika budget kembali positif, yaitu saat request
   yang buruk keluar dari jendela 30 hari. Tidak ada yang "membuka" freeze
   dengan tangan.

**Kenapa "tidak ada data" tidak memblokir.** Klaster baru, Prometheus yang
baru restart, atau lingkungan tanpa lalu lintas tidak punya vonis. Memblokir
di situ berarti rilis pertama ke klaster baru tidak pernah bisa jalan. Namun
kondisi ini juga tidak boleh lewat diam-diam, jadi ia selalu muncul sebagai
peringatan.

## Gerbang di CD

`cmd/slo-budget` mengembalikan kode keluar yang terpisah untuk tiap vonis:

| Kode | Arti | Tindakan `gate.sh` |
| :--- | :--- | :--- |
| 0 | setiap SLO masih punya sisa | lanjut |
| 1 | minimal satu budget habis | **tolak**, kecuali `budget_override` diisi |
| 2 | tanpa vonis (Prometheus tak terjangkau, rule belum dimuat, atau `NaN`) | lanjut dengan `::warning::` |
| 3 | argumen salah | tolak |

Deploy lewat tag `v*` tidak bisa membawa override. Perbaikan yang harus
keluar saat freeze dijalankan lewat **workflow_dispatch** `cd` dengan tag yang
sudah dibangun, dan `budget_override` diisi alasannya (mis. `revert INC-12`,
`fix CVE-2026-xxxx`). Alasan itu tercatat di log job, sehingga bisa diaudit.

Jalankan secara lokal (Prometheus compose di `127.0.0.1:19090`):

```sh
task slo:budget
# atau, bila exit code-nya dibutuhkan apa adanya (task membungkusnya jadi 201):
go build -o bin/ ./cmd/slo-budget && ./bin/slo-budget -prometheus http://127.0.0.1:19090
```

Contoh keluaran nyata dari stack lokal (2026-09-25), setelah lalu lintas
`GetMe` tanpa token dan data latensi dari sesi uji sebelumnya:

```text
requests-availability         100.0% left
requests-latency               77.4% left
```

## Drill: budget dihabiskan sungguhan

Tanggal 2026-09-25, di stack compose lokal. Prometheus yang dipakai adalah
Prometheus **sekali pakai** di atas tmpfs, dengan scrape 5 s, `slo-rules.yml`
yang sama, dan jaringan `selaras-core_default`. Dengan begitu data Prometheus
lokal yang biasa tidak tercemar selama 30 hari. Pengguna uji login lewat
`Auth/Register` dan `Auth/Login`.

| Waktu | Kejadian | Hasil |
| :--- | :--- | :--- |
| 11:51:49 | 400 × `Auth/GetMe` bertoken | 400 × 200 |
| 11:52:30 | gerbang | `requests-availability 100.0% left`, exit 0 |
| 11:52:38 | `docker stop selaras-identity`, 150 × `GetMe` | 150 × 200. `GetMe` dijawab dari token dan tidak memanggil identity-svc, jadi percobaan ini **tidak** menghasilkan 5xx dan budget tetap utuh |
| 11:53:32 | `docker stop selaras-profile`, 150 × `Profile/GetProfile` | 147 × 503, 3 × 504 |
| 11:54:03 + 25 s | gerbang | `requests-availability -2059.8% EXHAUSTED`, exit **1**; `gate.sh` exit 1 dengan `::error::` |
| idem | `gate.sh` dengan `BUDGET_OVERRIDE="drill: restore profile-svc"` | exit 0 dengan `::warning::` yang memuat alasannya |
| idem | `GET /api/v1/alerts` | `SelarasAvailabilitySLOBurn` **firing**, page dan ticket |
| 11:54:29 | `docker start selaras-profile` | 200 kembali pada 11:54:43 (startup ditambah reconnect gRPC) |

Angka −2059,8 % berarti budget terlampaui sekitar 21 kali lipat. Angkanya
sebesar itu karena total lalu lintas di jendela Prometheus sekali pakai
hanya sekitar 700 request, dan 150 di antaranya gagal. Di produksi, kejadian
yang sama tercampur dengan lalu lintas sebulan. Container drill dihapus
sesudahnya.

## Saat alert burn-rate menyala

| Alert | Artinya | Langkah pertama |
| :--- | :--- | :--- |
| `SelarasAvailabilitySLOBurn` page | 5xx membakar budget ≥ 6× laju aman; budget sebulan habis dalam ≤ 5 hari | `docs/runbook/edge-gateway.md`; cek `SelarasUnitDown` yang menyala bersamaan — unit hulu yang mati tampil sebagai 5xx di edge |
| `SelarasAvailabilitySLOBurn` ticket | pembakaran pelan tapi berkelanjutan (≥ 1× selama 3 hari) | cari route dengan 5xx terbanyak; biasanya satu dependensi yang flaky, bukan kejatuhan |
| `SelarasLatencySLOBurn` page | > 5 % request unary lebih lambat dari 50 ms, berkelanjutan | bandingkan dengan `docs/performance-report.md`; regresi satu orde biasanya query tanpa indeks atau satu panggilan gRPC yang jadi dua |
| `SelarasLatencySLOBurn` ticket | latensi memburuk pelan | cek pertumbuhan tabel dan rencana query route yang paling lambat |

## Mengubah SLO

1. Sunting `deploy/slo/selaras.yml` (bukan `slo-rules.yml`).
2. `task slo:generate`, lalu `task alerts:test`.
3. Test yang menyebut ambang lama akan gagal. Perbarui test-nya dengan
   sengaja: menurunkan target adalah keputusan produk, jadi alasannya
   ditulis di PR.
