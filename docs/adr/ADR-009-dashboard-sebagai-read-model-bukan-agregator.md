# ADR-009 — Dashboard sebagai read-model, bukan agregator


**Konteks.** E17: `DashboardRepository` memanggil repository lain, membungkusnya
`Cache::remember` 15 menit, dan empat listener manual bertugas membatalkan cache itu. Empat
listener invalidasi adalah gejala, bukan solusi.

**Opsi.** (a) read-model yang dimaterialisasi dari event · (b) agregasi di gateway plus cache
Redis, setara perilaku sekarang · (c) panggilan gRPC paralel tanpa cache.

**Keputusan.** (a).

**Konsekuensi.** Positif: pembacaan dashboard jadi satu query ke satu tabel; empat listener
invalidasi hilang seluruhnya; menjadi contoh CQRS dengan pembenaran yang bisa ditunjukkan.
Negatif: dashboard jadi eventually consistent sehingga lag harus diukur dan dinyatakan; ada
duplikasi data; kalau read-model rusak, dibutuhkan mekanisme rebuild dari awal.

**Pembatal.** Kalau lag terukur ternyata mengganggu, turun ke (b) — kodenya sebagian besar
dipakai ulang.

---

