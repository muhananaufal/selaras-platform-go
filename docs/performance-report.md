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
   keras sebelum plafon, bukan sesudahnya.

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
