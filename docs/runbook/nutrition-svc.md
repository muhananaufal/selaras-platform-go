# Runbook on-call — nutrition-svc

Pemilik preferensi kuliner dan panduan menu harian. Tidak memanggil unit lain
secara sinkron: bahasa pengguna dibaca dari cache `user_languages` yang diisi
event `profile.updated` (ADR-007). Mengantre `meal.guide.requested` lewat
outbox dan mengonsumsi `llm.results` (aggregate `meal_guide`). Bergantung
keras pada Postgres (skema `nutrition`). Waktu makan dihitung di zona
`NUTRITION_TIMEZONE` (bawaan `Asia/Jakarta`, B18).

## Siapa yang terdampak bila ia mati

- **Mati**: seluruh `/culinary/*`.
- **Bertahan**: semua unit lain; hasil panduan yang tiba saat ia mati
  menunggu di topic.

## Gejala dan cara membacanya

| Gejala | Yang hampir pasti terjadi | Periksa |
| :--- | :--- | :--- |
| Panduan `pending` lebih dari satu menit | llm-worker mati / job `dead` | `llm.llm_jobs` menurut `aggregate_id` = id panduan; runbook llm-worker |
| Panduan `failed` | worker menyerah, atau `guide_json` bukan JSON yang sah | log `a completed meal guide ...`; template `daily_guide.v1` |
| Waktu makan salah (sarapan pada jam makan siang) | `NUTRITION_TIMEZONE` kosong/salah → dihitung di UTC (B18) | log start-up `meal times will be computed in this zone` |
| Panduan berbahasa salah | cache bahasa belum terisi (event `profile.updated` belum tiba) | `SELECT * FROM nutrition.user_languages WHERE user_id = ...`; outbox profile; `cmd/backfill-nutrition` |
| 422 pada `PATCH /culinary/preferences` | nilai enum di luar `thrifty/standard/flexible` atau gaya masak yang tidak dikenal; tag > 30 | kontrak `edge-v1.yaml`; semua divalidasi sebelum satu pun ditulis |
| Konsumen berputar `holding offsets so failed results are redelivered` | hasil untuk panduan yang SUDAH DIHAPUS — **sudah diperbaiki** (B22); bila muncul lagi, ada galat baru yang dianggap sementara | log galatnya; `terminal()` di konsumen |
| Constraint `daily_meal_guides_ready_has_data` gagal | kode mencoba menandai `ready` tanpa data, atau sebaliknya — itu bug kode, dan constraint-nya bekerja | trace-nya |

## Triase dalam lima menit

1. `curl :9603/readyz`.
2. Grafana → gRPC `nutrition.v1.Nutrition/*` dan lag llm-worker.
3. `trace_id` → Tempo: trace panduan lengkap = `GenerateDailyGuide` →
   `llm.jobs process` → `llm.generate` → `llm.results process`
   (nutrition-svc). Ini trace yang dipakai sebagai contoh di
   `docs/observability.md` untuk assessment; bentuknya sama.
4. `SELECT status, count(*) FROM nutrition.daily_meal_guides GROUP BY 1`.

## Cara pulih

- Nyalakan lagi; konsumen melanjutkan dari offset.
- Cache bahasa yang kosong untuk banyak pengguna (misalnya setelah
  pemulihan dari backup lama): `cmd/backfill-nutrition` dengan prosedur di
  `docs/runbook/backfill-nutrition.md`.

## Yang JANGAN dilakukan

- Jangan menyetel `NUTRITION_TIMEZONE=UTC` "supaya konsisten dengan server".
  Yang konsisten adalah dengan jam pengguna, dan B18 adalah bukti bahwa
  UTC menghasilkan sarapan pada pukul 13.37 WIB.

## Metrik yang membuktikan pulih

`rpc_server_call_duration_seconds{rpc_method=~"nutrition.*"}` OK
(`UpdatePreferences` p95 13 ms di laporan kinerja); lag llm-worker nol;
jumlah panduan `pending` tua kembali nol.
