# Runbook on-call — coaching-svc

Pemilik program coaching, kurikulum mingguan, tugas harian, thread, dan
laporan kelulusan. Tidak memanggil unit lain secara sinkron. Mengantre tiga
jenis pekerjaan LLM lewat outbox (kurikulum, balasan thread, laporan
kelulusan) dan mengonsumsi `llm.results` (aggregate `coaching_program` dan
`coaching_thread`). Menerbitkan `coaching.program.updated` untuk dashboard.
Bergantung keras pada Postgres (skema `coaching`).

## Siapa yang terdampak bila ia mati

- **Mati**: seluruh `/coaching/*`.
- **Bertahan**: semua unit lain. Dashboard menampilkan program dari
  salinannya (ADR-009); hasil LLM untuk coaching menunggu di topic sampai
  ia kembali.

## Gejala dan cara membacanya

| Gejala | Yang hampir pasti terjadi | Periksa |
| :--- | :--- | :--- |
| Program baru `pending` lebih dari satu menit, kurikulum tidak muncul | llm-worker mati, atau job `dead` | `llm.llm_jobs` menurut `aggregate_id` = id program; runbook llm-worker |
| Program `failed` | worker menyerah setelah 3 percobaan, atau kurikulum bukan JSON yang bisa dibaca (`week ... has a task with an unreadable date`) | log `a curriculum result was unusable`; template prompt `curriculum.v1` |
| 409 saat memulai program | aturan D2/D3: satu program aktif per pengguna, satu program per penilaian | bukan bug; klien harus menutup yang lama atau memakai penilaian lain |
| 409 saat menyelesaikan tugas/mengirim pesan | D4/D5: program tidak `active` (dijeda atau selesai) | bukan bug |
| Konsumen berputar `rewinding so failed ... are redelivered` | hasil untuk program/thread yang SUDAH DIHAPUS diperlakukan sebagai galat sementara — **sudah diperbaiki** (B22); bila muncul lagi, ada jenis galat baru yang dianggap sementara | log galatnya; `terminal()` di `internal/coaching/adapter/consumer/results.go` |
| Hari program tidak bertambah | `DayOn(now)` menghitung dari `started_at` dan zona; bukan pekerjaan terjadwal | jam server; tidak ada cron yang bisa "macet" |

## Triase dalam lima menit

1. `curl :9403/readyz`.
2. Grafana → gRPC `coaching.v1.Coaching/*`, dan panel lag llm-worker.
3. `trace_id` dari log → Tempo: trace kurikulum yang utuh punya span
   `llm.jobs process` → `llm.generate` → `llm.results process` di
   coaching-svc.
4. `SELECT status, count(*) FROM coaching.coaching_programs GROUP BY 1` —
   lonjakan `pending` = worker; lonjakan `failed` = prompt/model.

## Cara pulih

- Nyalakan lagi; konsumen melanjutkan dari offset yang belum dikomit.
- Program `failed` karena kurikulum tidak bisa dibaca: perbaiki templatnya
  (checksum terdaftar di `internal/llm/prompt`), lalu pengguna memulai
  program lagi. Tidak ada mekanisme "coba ulang" server-side untuk hasil
  yang sudah dinyatakan tidak bisa dibaca — itu disengaja: hasil yang sama
  akan gagal dengan cara yang sama.

## Yang JANGAN dilakukan

- Jangan mengubah status program langsung di basis data untuk "membuka"
  interaksi; constraint dan aturan D4/D5 ada di dua lapis dan akan
  membuatnya tidak konsisten.

## Metrik yang membuktikan pulih

`rpc_server_call_duration_seconds{rpc_method=~"coaching.*"}` OK; lag konsumen
llm-worker turun ke nol; jumlah program `pending` tua kembali nol.
