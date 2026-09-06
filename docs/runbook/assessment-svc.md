# Runbook on-call — assessment-svc

Pemilik penilaian risiko: menghitung SCORE2 / SCORE2-OP / SCORE2-Diabetes
(paritas 288 golden vector, ADR-008), menyimpan hasilnya, dan mengantre
personalisasi ke llm-worker. Menerbitkan `assessment.completed` (dibaca
dashboard) dan `personalization.requested`; mengonsumsi `llm.results` untuk
laporan personalisasi dan `profile.updated` untuk cache profil lokal
(ADR-007). Bergantung keras pada Postgres (skema `assessment`); pada Kafka
hanya untuk hasil yang datang belakangan.

## Siapa yang terdampak bila ia mati

- **Mati**: memulai penilaian, membaca riwayat dan detail penilaian, meminta
  personalisasi, `/profile` bagian wilayah risiko (gateway memanggil
  `ResolveRiskRegion` — gagalnya dicatat, profil tetap dijawab).
- **Bertahan**: dashboard (read-model sendiri, ADR-009) menampilkan
  penilaian terakhir dari salinannya; coaching, chat, nutrition, akun.
- Hasil personalisasi yang tiba saat ia mati menunggu di topic; offset tidak
  dikomit sampai diproses.

## Gejala dan cara membacanya

| Gejala | Yang hampir pasti terjadi | Periksa |
| :--- | :--- | :--- |
| `POST /risk-assessments` 422 "usia/jenis kelamin tidak diketahui" | profil belum lengkap, atau cache profil belum terisi karena `profile.updated` belum tiba | `SELECT * FROM assessment.profile_cache WHERE user_id = ...`; outbox profile |
| `personalization_status` `pending` lebih dari satu menit | llm-worker mati, atau job `dead` tanpa event gagal sampai | `llm.llm_jobs` menurut `aggregate_id` = id penilaian; runbook llm-worker |
| `personalization_status` `failed` | llm-worker menyerah setelah 3 percobaan (chaos F9-14) atau laporan bukan JSON | `personalization_error` (kolom internal, tidak dipublikasikan); pengguna boleh meminta lagi |
| Hasil skor berbeda dari yang diharapkan klinisi | ini gawat: paritas rusak | `go test ./internal/assessment/domain/score/ -run Golden` HARUS hijau; kalau merah, konstanta model berubah tanpa vektor baru (B2-05) |
| Kategori risiko "salah" | bukan model bahasa lagi (B19): `CategoryFor(age, risk)` deterministik | test `category_test.go`; tabelnya: <50: 2,5/7,5 · 50–69: 5/10 · ≥70: 7,5/15 |
| Dashboard tidak memuat penilaian baru | `assessment.completed` tertahan di outbox atau projector dashboard tertinggal | `assessment.outbox` tertunda; runbook dashboard-svc |

## Triase dalam lima menit

1. `curl :9302/readyz`.
2. Grafana → gRPC `assessment.v1.Assessment/*`; `StartAssessment` p95 wajar
   17 ms (laporan kinerja).
3. `trace_id` dari log → Tempo. Trace personalisasi yang lengkap punya
   enam span: gateway → `RequestPersonalization` → `llm.jobs process` →
   `llm.generate` → `llm.results process` (lihat `docs/observability.md`).
   Span yang hilang menunjuk unit yang tidak bekerja.
4. Outbox dan job: dua kueri di tabel di atas.

## Cara pulih

- Nyalakan lagi; tidak ada keadaan di memori.
- Personalisasi yang `failed`: pengguna meminta lagi lewat endpoint yang
  sama — kunci idempotensinya `personalization:<id>` sehingga job lama yang
  `dead` tidak menghalangi (klaim baru dengan job baru).
- Cache profil yang tertinggal: tunggu event, atau isi ulang dari
  profile-svc dengan perintah backfill yang sepadan dengan
  `cmd/backfill-nutrition`.

## Yang JANGAN dilakukan

- Jangan "memperbaiki" angka risiko di basis data. Yang benar adalah kode
  yang lulus golden vector; angka yang tidak cocok adalah bug yang harus
  direproduksi, bukan data yang harus disunting.
- Jangan menghapus baris `llm_jobs` yang `dead` untuk memancing ulang;
  kuncinya adalah permintaan baru dari pengguna, bukan baris yang hilang.

## Metrik yang membuktikan pulih

`rpc_server_call_duration_seconds{rpc_method=~"assessment.*"}` OK; jumlah
`personalization_status = 'pending'` yang lebih tua dari satu menit kembali
nol (kueri ad hoc; belum ada metriknya — dicatat sebagai celah).
