# ADR-025 — Kuota penyedia LLM memarkir pekerjaan, bukan mematikannya

**Status.** Diterima. Dibuat setelah larian nyata pertama dengan Gemini
(2026-09-07, B30), saat sepuluh pekerjaan mati dalam dua menit karena kuota
tingkat gratis habis — bukan karena satu pun pekerjaan itu salah.

---

**Konteks.** llm-worker memperlakukan setiap galat penyedia dengan cara yang
sama: coba lagi tiga kali (klien Gemini mencoba tiga kali di dalamnya), lalu
tandai pekerjaan `dead` dan terbitkan `LlmJobFailed`. Untuk galat yang memang
milik pekerjaan itu — prompt ditolak filter, jawaban terpotong, JSON rusak —
itu benar: mengulangnya tidak akan mengubah apa pun, dan pengguna berhak tahu.

Kuota bukan galat milik pekerjaan. `429 RESOURCE_EXHAUSTED` berarti *penyedia*
sedang tidak menerima siapa pun; mengulang pekerjaan yang sama dalam dua detik
hanya membakar percobaan, dan menandainya `dead` berarti kurikulum seorang
pengguna hilang karena pengguna lain lebih dulu menghabiskan jatah. Yang
terlihat 2026-09-07: kuota gratis 20 permintaan/hari/model, satu suite e2e
memakainya habis, dan setiap pekerjaan berikutnya mati dalam hitungan detik
dengan alasan yang tidak ada hubungannya dengan pekerjaan itu.

**Opsi yang ditimbang.**

| Opsi | Kelebihan | Kekurangan |
| :--- | :--- | :--- |
| A. Biarkan `dead`; pemilik menjalankan ulang lewat perintah | Tidak ada kode baru; sederhana | Pekerjaan pengguna hilang tanpa salah; butuh manusia yang tahu kapan kuota pulih; `LlmJobFailed` sampai ke pengguna sebagai "gagal" padahal bukan |
| B. Tabel tunda (`deferred_until`) di llm_jobs, penjadwal terpisah | Partisi Kafka tetap mengalir; pekerjaan lain jalan terus | Urutan per agregat hilang (pekerjaan tertunda bisa disalip); penjadwal kedua yang membaca tabel adalah mesin antrean kedua di samping Kafka; dua sumber kebenaran untuk "apa yang menunggu" |
| **C. Parkir: lepas klaim, tahan offset, diam sebentar, ulangi** | Tidak ada yang mati; urutan per partisi utuh; nol tabel dan nol penjadwal baru; jeda berlipat sampai batas | Seluruh worker diam selama jeda — pekerjaan yang bukan kuota pun ikut menunggu; kuota harian membuat worker mencoba sia-sia setiap 15 menit sepanjang hari |

**Keputusan.** Opsi C. Saat penyedia menjawab `llm.ErrRateLimited`:

1. klaim idempotensi pekerjaan dilepas (`release`), penghitung percobaan
   tidak disentuh — pekerjaan itu tidak gagal;
2. offset record ditahan dan dimundurkan (`kafka.Rewinder`), sehingga poll
   berikutnya membawanya lagi;
3. loop worker diam selama `DefaultQuotaCooldown(n)`: satu menit untuk
   penolakan pertama, berlipat dua per penolakan beruntun, paling lama lima
   belas menit; satu jawaban berhasil mengembalikan `n` ke nol;
4. metrik `llm_jobs_total{outcome="parked"}` dan log `parking the queue`
   menyebutkan berapa lama.

Klien Gemini sendiri sudah menghormati `retryDelay` dari penyedia (B30, bagian
pertama) — itu untuk kuota per-menit yang pulih cepat; parkir ini untuk yang
tidak pulih dalam satu panggilan.

Kebetulan yang menguntungkan: langkah 2 menutup cacat lain yang belum pernah
terlihat — worker mengomit offset walau `handle` gagal (Postgres tidak
terjangkau saat klaim, misalnya), sehingga pekerjaan hilang diam-diam.
Konsumen lain sudah memakai `Rewinder` sejak F6; worker belum.

**Yang dibayar.** Satu worker yang diparkir tidak mengerjakan apa pun,
termasuk pekerjaan yang mungkin akan diterima penyedia (model lain, misalnya
— tidak ada di sistem ini hari ini). Kuota per-hari berarti sampai 96
permintaan sia-sia sehari pada jeda maksimum; itu lebih murah daripada satu
pekerjaan yang mati. Angka satu menit dan lima belas menit adalah kebijakan,
bukan pengukuran; keduanya konstanta yang bisa diganti lewat
`WithQuotaCooldown` tanpa menyentuh loop.

**Konsekuensi.** Positif: `dead` kembali berarti "pekerjaan ini salah",
bukan "penyedia sedang sibuk"; `LlmJobFailed` tidak lagi terbit karena kuota;
partisi dan urutan per agregat utuh. Negatif: latensi pekerjaan saat kuota
habis menjadi tidak terbatas atas — pengguna melihat `pending` selama kuota
belum pulih, dan tidak ada yang memberi tahunya mengapa. Itu masalah
antarmuka yang belum diselesaikan di sini.

**Pembatal.** Bila kelak ada lebih dari satu penyedia atau model yang bisa
dipilih per pekerjaan, "diam seluruh worker" menjadi salah — pekerjaan yang
kuotanya masih ada ikut menunggu — dan opsi B (tabel tunda per penyedia)
harus ditinjau ulang. Bila pengguna butuh jawaban "kapan", `pending` tanpa
alasan tidak cukup dan status pekerjaan perlu membawa `parked_until`.
