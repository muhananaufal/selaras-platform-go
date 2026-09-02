# ADR-019 — Temuan baru saat porting: diperbaiki di tempat, dengan pagar


**Konteks.** ADR-013 hanya mengatur temuan yang **sudah** terkatalog di
[`03-legacy-findings.md`](03-legacy-findings.md) — S1–S10, B1–B11, T1–T14. Ia tidak mengatur
kejanggalan **baru** yang pasti bermunculan saat kode dibaca baris demi baris untuk di-port.
Pembacaan awal menyisir 85 berkas dalam satu sesi; porting akan menyisirnya jauh lebih dalam.

**Opsi.** (a) audit menyeluruh ulang sebelum tiap fase · (b) catat saja, perbaiki belakangan
dalam satu gelombang · (c) **perbaiki di tempat saat ditemukan, dengan batas yang tegas**.

**Keputusan.** (c).

**Kenapa (a) ditolak.** Audit menyeluruh di depan setiap fase menunda pekerjaan tanpa menambah
kepastian — kejanggalan hanya benar-benar terlihat saat kodenya di-port, bukan saat dibaca.

**Kenapa (b) ditolak.** "Gelombang perbaikan nanti" adalah gelombang yang tidak pernah datang.
Dan porting yang dengan setia menyalin cacat menghasilkan sistem baru yang membawa utang lama.

**Lima pagar — tanpa ini, (c) berubah jadi scope creep tanpa dasar.**

| # | Pagar | Alasannya |
| :--- | :--- | :--- |
| 1 | **Hanya kode yang sedang di-port di task itu.** Kejanggalan di domain lain **dicatat di [`05-parking-lot.md`](05-parking-lot.md) bagian A2**, tidak dikerjakan | Tanpa batas ini, satu task menyeret seluruh sistem dan fase tidak pernah selesai — itu R1 |
| 2 | **Setiap temuan baru wajib dapat ID** di dokumen temuan (`S12+`, `B12+`, `T15+`) beserta klasifikasi ADR-013: REPLIKASI, PERBAIKI, atau BUANG | Temuan tanpa ID hilang begitu perhatian berpindah |
| 3 | **Dilarang audit menyeluruh di tengah fase** | Itu opsi (a) yang menyelinap masuk lewat pintu belakang |
| 4 | **Bila perbaikan menyentuh kontrak yang sudah dikunci di B1, BERHENTI** | Itu bukan perbaikan lagi, melainkan perubahan arsitektural — butuh persetujuan (`AGENTS.md` §5.6) |
| 5 | **Bila perbaikan lebih besar daripada task yang sedang dikerjakan**, ia masuk [`05-parking-lot.md`](05-parking-lot.md) lalu dipromosikan jadi task di fase yang sesuai — bukan dikerjakan di tempat | Menjaga satu task tetap bisa ditinjau utuh dalam beberapa menit |

**Uji satu kalimat sebelum memperbaiki sesuatu di tengah jalan:**
*"Apakah berkas ini memang sedang saya port sekarang?"* Bila tidak — catat di parking lot, lanjut.

**Konsekuensi.** Positif: cacat tidak menyeberang diam-diam; perbaikan terjadi saat konteksnya
masih segar di kepala, yang jauh lebih murah daripada kembali lagi nanti; dokumen temuan tumbuh
sebagai catatan hidup, bukan artefak sekali tulis. Negatif: tiap fase jadi sedikit lebih lambat
dari perkiraan awal; dan daftar temuan akan terus bertambah, sehingga angka "11 cacat fungsional"
di dokumen temuan wajib dibaca sebagai **kondisi awal**, bukan total akhir.

**Pembatal.** Bila laju temuan baru ternyata begitu tinggi sampai porting berhenti bergerak,
kembali ke opsi (b) untuk domain itu saja — catat semuanya, selesaikan port-nya, lalu kerjakan
perbaikannya sebagai satu task tersendiri.
---

