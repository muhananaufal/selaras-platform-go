# ADR-013 — Kebijakan port: apa yang direplikasi, diperbaiki, dan dibuang


**Konteks.** Pembacaan seluruh basis kode (E18) menemukan 10 masalah keamanan, 11 cacat
fungsional, dan 11 utang arsitektur. ADR-008 memilih Laravel sebagai oracle — dan konsekuensi
negatif yang sudah tertulis di sana kini punya contoh konkret: **oracle hanya sekuat kebenaran
Laravel.** Tanpa kebijakan eksplisit, port yang setia akan memindahkan cacatnya dengan rapi.

**Opsi.** (a) replikasi setia, cacat ikut · (b) perbaiki semuanya sambil menulis ulang ·
(c) klasifikasi per temuan dengan aturan yang dinyatakan di muka.

**Keputusan.** (c), dengan tiga kelas dan satu aturan pemutus.

| Kelas | Aturan | Berlaku untuk |
| :--- | :--- | :--- |
| **REPLIKASI** | Perilaku ikut menyeberang apa adanya, dan diuji | Seluruh aturan domain (D1–D12) dan kontrak API yang dipakai frontend |
| **PERBAIKI** | Perilaku sengaja diubah; perubahannya dicatat di RFC dan diuji | Seluruh temuan keamanan (S1–S10) dan cacat fungsional (B1–B11) |
| **BUANG** | Tidak ikut di-port sama sekali | T8 middleware mati, B10 endpoint hantu, B11 log debug, S10 field debug |

**Aturan pemutus — golden vector tidak boleh membekukan cacat.** Bila sebuah temuan menyentuh
mesin risiko, ia **wajib dibuktikan lewat eksekusi lebih dulu**, baru diputuskan. Vektor
di-generate dari Laravel **setelah** cacatnya diperbaiki di sisi Laravel, dalam branch terpisah
yang tidak pernah di-merge. Ini berlaku langsung pada B1: bila `$profile->age` memang `null`,
maka `estimateSbp` menghasilkan nilai konstan, dan membekukannya ke Go berarti menulis ulang
kalkulator risiko kardiovaskular yang salah — dengan bukti paritas yang tampak meyakinkan.

**Konsekuensi.** Positif: cacat tidak menyeberang diam-diam; setiap perbedaan Go terhadap Laravel
punya alasan tertulis; daftar D1–D12 menjadi kriteria penerimaan yang konkret. Negatif: paritas
tidak lagi bisa diklaim menyeluruh, sehingga laporan paritas wajib menyebut **di mana Go sengaja
berbeda**; memperbaiki oracle menambah kerja di B2; ada risiko perbaikan itu sendiri
memasukkan galat baru ke dalam oracle.

**Pembatal.** Bila ternyata B1 tidak benar — `$profile->age` terisi lewat jalur yang belum saya
temukan — maka aturan pemutus tidak berlaku untuk temuan itu, dan vektor di-generate langsung
dari kode yang ada tanpa perubahan apa pun.

---

