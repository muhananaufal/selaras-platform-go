# Koneksi Postgres — diukur dulu, baru PgBouncer

F9-26 dan F9-27. Urutannya disengaja: angka nyata lebih dulu, solusinya
sesudahnya — bukan sebaliknya.

## Batas yang dihadapi

| | |
| :--- | :--- |
| `max_connections` Postgres | **100** (bawaan image; `SHOW max_connections`) |
| Kolam per unit (`pg.DefaultConfig`) | `MaxConns 10`, `MinConns 2` |
| Unit yang memegang kolam | 8 (tujuh service + llm-worker; gateway tidak menyentuh Postgres) |

## Perhitungan sebelum diukur

| Keadaan | Koneksi maksimum ke Postgres |
| :--- | ---: |
| 1 replika per unit, kolam terisi penuh | 8 × 10 = **80** |
| 2 replika per unit (HPA aktif, ADR-014) | 8 × 10 × 2 = **160** |
| 3 replika per unit | 8 × 10 × 3 = **240** |
| + superuser, backup, pemelihara partisi, `psql` operator | +5 sampai +10 |

Dengan dua replika saja, kolam yang terisi penuh melampaui `max_connections`.
Yang membuatnya berbahaya bukan angkanya, melainkan **kapan** ia terjadi:
kolam terisi penuh justru saat beban tinggi, dan itulah saat HPA menambah
replika — replika baru gagal menyambung tepat ketika ia dibutuhkan.

## Yang diukur (sebelum PgBouncer)

Diambil 2026-09-07 dengan `pg_stat_activity`, sampel setiap 3 detik selama
skenario k6 campuran (16 VU pembaca + 4 VU penulis, 42,9 permintaan/detik).

| | Idle | Puncak saat k6 |
| :--- | ---: | ---: |
| Backend klien, seluruhnya | 25 | **27** |
| svc_identity | 3 | 3 |
| svc_profile | 3 | 4 |
| svc_assessment | 3 | 4 |
| svc_dashboard | 5 | 5 |
| svc_coaching / chat / nutrition / llm | 2–3 | 2–3 |

Pembacaannya jujur: pada beban ini kolam TIDAK pernah terisi penuh. Puncak
per peran 3–5 dari 10 yang diizinkan. Ledakan koneksi adalah masalah
**replika**, bukan masalah beban satu replika — dan itu persis yang tidak
bisa diukur di compose dengan satu replika per unit. Perhitungan di atas
tetap berlaku; pengukurannya membuktikan bagian "kolam terisi 3–5 saat
sibuk", bukan bagian "dikalikan replika".

## PgBouncer (F9-27)

`edoburu/pgbouncer` 1.25 di `core.yml`, mode **transaksi**, di depan Postgres.
Seluruh DSN unit di `apps.yml` menunjuk ke `pgbouncer:5432`.

Tiga hal yang harus benar supaya ini bekerja, dan bagaimana masing-masing
diverifikasi:

| Syarat | Cara | Bukti |
| :--- | :--- | :--- |
| Peran per-service dan isolasi skema tetap (ADR-006) | `auth_type=scram-sha-256` + `auth_user` admin + `auth_query` bawaan: PgBouncer memverifikasi kata sandi `svc_*` ke Postgres, klien tetap menyambung sebagai `svc_*` | ini yang dihasilkan image: `* = host=postgres port=5432 auth_user=<admin>`; test integrasi identity/chat/coaching/outbox lulus lewat `:16432` |
| `search_path` per skema | pgx mengirim `search_path` dari DSN sebagai parameter startup dan PgBouncer menolak yang tidak dikenal → `ignore_startup_parameters=search_path`; yang berlaku adalah `ALTER ROLE svc_x SET search_path` dari initdb | test yang sama: kueri tanpa nama skema menemukan tabelnya |
| Prepared statement pgx dalam mode transaksi | `max_prepared_statements=200` (PgBouncer 1.21+ melacaknya per koneksi server) | test yang sama; nol galat "prepared statement does not exist" di log PgBouncer |

### Yang diukur (sesudah PgBouncer)

Skenario k6 yang sama, sampler yang sama:

| | Sebelum | Sesudah |
| :--- | ---: | ---: |
| Backend klien di Postgres, puncak | 27 | **21** |
| svc_identity / profile / assessment | 3 / 4 / 4 | 5 / 3 / 4 |
| svc_dashboard | 5 | 3 |
| svc_chat / coaching / llm | 2 / 2 / 3 | 1 / 1 / 1 |
| k6 p95 (campuran) | 10,9 ms | 13,4 ms |
| k6 gagal | 0 | 5 dari 5.821 (0,08%) — lihat di bawah |

Penurunannya kecil (27 → 21) **karena bebannya kecil**. Nilai PgBouncer
bukan di angka ini, melainkan di aritmetikanya: kini setiap (peran, basis
data) punya kolam server `default_pool_size=8` apa pun jumlah replika yang
menyambung ke PgBouncer. Tiga replika × 8 unit × 10 koneksi klien = 240
koneksi **ke PgBouncer**, yang diterjemahkan menjadi ≤ 8 × 8 = **64 koneksi
ke Postgres**. Batas 100 tidak lagi bergantung pada berapa replika yang
dijalankan HPA.

Biaya yang jujur: p95 naik ~2,5 ms (satu lompatan jaringan tambahan dan
pemetaan prepared statement). Lima permintaan yang gagal pada larian pertama
lewat PgBouncer **tidak terulang**: larian ulang dengan pencatatan kegagalan
k6 (`unexpected()` di `test/k6/lib/api.js`) menghasilkan **0 dari 5.809**
gagal, p95 11,4 ms. Larian pertama itu berimpit dengan test integrasi
pemelihara partisi yang sedang membuat dan membuang partisi `identity.outbox`
(kunci ACCESS EXCLUSIVE sesaat) — penjelasan yang paling masuk akal, dan
dinyatakan sebagai dugaan, bukan bukti.

## Konsekuensi untuk klaster (F9-01, F9-21)

- PgBouncer menjadi Deployment tersendiri di Helm; unit menunjuk ke
  Service-nya, bukan ke Postgres.
- `max_connections` tidak perlu dinaikkan; `default_pool_size` × jumlah
  peran × jumlah replika PgBouncer adalah angka yang dijaga di bawah 100.
- `MaxConns 10` per unit tetap: ia kini membatasi koneksi ke PgBouncer,
  yang murah, bukan ke Postgres.

## Replika baca (F9-32)

`postgres-replica` di `core.yml`: streaming replication asinkron dari primer,
salinan dasar diambil otomatis saat data kosong (`deploy/compose/replica/
entrypoint.sh`), peran `replicator` hanya REPLICATION (`initdb/
02-replication.sh`). Yang membacanya: **dashboard-svc**, lewat
`DASHBOARD_READ_DSN` langsung ke replika — `Find` dan riwayat ke replika,
proyeksi tetap ke primer (`dashboardpg.NewRepositoryWithReader`, dengan test
yang membuktikan pemisahannya). Skenario k6 baca memanggil `GET /dashboard`,
jadi pembacaan k6 ikut ke replika tanpa perubahan skenario.

Diukur 2026-09-07, k6 tulis 10 VU selama 60 detik (1.682 permintaan,
27,7/detik, nol gagal), sampel setiap 2 detik:

| Ukuran | Nilai | Sumber |
| :--- | ---: | :--- |
| `write_lag` / `flush_lag` / `replay_lag` di akhir beban | **0,3 ms / 10,4 ms / 10,4 ms** | `pg_stat_replication` di primer |
| `replay_lag` saat idle sebelum beban | 72 ms | idem |
| `now() − pg_last_xact_replay_timestamp()`, maksimum 30 sampel | 2.683 ms | replika |
| Replika menolak INSERT | ✅ `cannot execute INSERT in a read-only transaction` | |
| `pg_is_in_recovery()` | `t` | |

Dua angka lag itu mengukur hal yang berbeda, dan keduanya disebut supaya
tidak ada yang memilih yang cantik: `pg_stat_replication` mengukur jarak
antara primer dan replika untuk WAL yang SEDANG dikirim (10 ms), sementara
selisih terhadap `pg_last_xact_replay_timestamp` ikut menghitung jeda saat
tidak ada transaksi sama sekali. Dugaan yang paling masuk akal
[inferensi] untuk 2,7 detik itu adalah sampel yang jatuh di celah antar
batch penulisan k6 (satu VU menulis lalu tidur satu detik), bukan replika
yang tertinggal 2,7 detik — `pg_stat_replication` di saat yang sama
melaporkan puluhan milidetik. Belum dibuktikan dengan beban tulis yang
rapat; dinyatakan sebagai dugaan. Yang berlaku untuk dasbor adalah angka
pertama, dan sepuluh milidetik jauh di bawah lag proyeksi dasbor sendiri
(444–920 ms, F7).

Yang BELUM ada: replika di Helm/k3d (memori satu node sudah habis untuk
KEDA dan replika HTTP; `values.yaml` menerima `DASHBOARD_READ_DSN` lewat
Secret bila klaster punya replika), failover otomatis (replika tidak pernah
dipromosikan sendiri), dan PgBouncer di depan replika (satu kolam kecil
langsung, dinyatakan cukup untuk satu pembaca).
