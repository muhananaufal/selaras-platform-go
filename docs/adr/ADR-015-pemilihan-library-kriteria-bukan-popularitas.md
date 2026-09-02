# ADR-015 — Pemilihan library: kriteria, bukan popularitas


**Konteks.** Tujuh keputusan library tidak pernah diambil: linter OpenAPI, tool migrasi, cara
akses query, JWT, validasi, container test, dan konfigurasi. Task menyebut hasilnya tetapi tidak
menamai alatnya — sehingga library akan masuk lewat `go get` saat kode ditulis, tanpa pernah
ditimbang.

**Opsi.** (a) pilih berdasarkan popularitas · (b) pilih berdasarkan kriteria teknis yang
dinyatakan · (c) tunda sampai fase eksekusi.

**Keputusan.** (b). Rinciannya di [`04-tech-stack.md`](04-tech-stack.md).

**Kenapa (a) ditolak.** Popularitas tidak bisa diklaim tanpa diukur, dan mengarang peringkatnya
melanggar `AGENTS.md` §3.3. Ia juga kriteria yang buruk untuk kasus ini: dua pilihan terpenting
di sini justru **menolak** opsi yang lebih banyak dipakai, atas dasar kegagalan nyata yang sudah
tercatat — `viper` ditolak karena membaca kosong nilai yang hanya ada di environment, dan
`confluent-kafka-go` ditolak karena cgo-nya bertabrakan dengan image distroless. Keduanya
pelajaran yang sudah dibayar, dan itu bukti yang lebih kuat daripada jumlah pemakai.

**Kenapa (c) ditolak.** Keputusan yang ditunda ke fase eksekusi tidak pernah benar-benar diambil
— ia diselundupkan oleh baris `go.mod` pertama yang kebetulan ditulis.

**Kriteria, berurutan.**

1. `stdlib` menang bila cukup — dasar dipilihnya `slog`.
2. Kegagalan yang sudah tercatat di `LEARNED.md` mengalahkan preferensi apa pun.
3. Murni Go mengalahkan pembungkus cgo — image distroless sudah jadi keputusan (F1-19).
4. Yang tidak menambah runtime bahasa lain ke CI lebih diutamakan — dasar `vacuum` atas `spectral`.
5. Reflection runtime dilarang di `domain`, hanya sah di lapisan terluar.

**Konsekuensi.** Positif: setiap dependency punya alasan tertulis dan setiap penolakan punya
catatan, sehingga alternatif yang sama tidak diusulkan ulang tanpa ada yang ingat kenapa ia
ditolak; `go.mod` jadi konsekuensi keputusan, bukan sumbernya. Negatif: pilihan yang lebih jarang
dipakai berarti contoh dan jawaban komunitasnya lebih sedikit; dan bila salah satu ternyata
tidak dirawat lagi, biaya penggantiannya ditanggung sendiri.

**Pembatal.** Bila B0-02 menemukan salah satu pilihan tidak dirawat, tidak punya rilis stabil,
atau tidak ada sama sekali, pilihan itu gugur dan alternatifnya dicatat di dokumen stack —
bukan diganti diam-diam saat kode ditulis.

---

