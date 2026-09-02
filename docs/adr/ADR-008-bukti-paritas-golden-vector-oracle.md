# ADR-008 — Bukti paritas: golden vector oracle


**Konteks.** E1 memastikan tidak ada test yang bisa dijadikan jaring pengaman. E13 dan E14
menunjukkan `ClinicalRiskService` adalah **fungsi murni** dengan konstanta terpisah — kelas kode
yang paling berbahaya bila salah, sekaligus paling mudah dibuktikan benar.

**Opsi.** (a) generate golden vector dari Laravel sebagai oracle · (b) tulis test Go dari
membaca kode PHP · (c) bandingkan manual beberapa kasus · (d) tulis test suite lengkap di
Laravel dulu.

**Keputusan.** (a). Sebelum satu baris Go ditulis, Laravel yang ada menghasilkan
`golden_vectors.json` berisi pasangan input-output pada presisi penuh.

**Aturan yang mengikat:**

1. Vektor menyapu ketiga model: SCORE2, SCORE2-OP, SCORE2-Diabetes, di seluruh kombinasi
   `sex` x `risk_region` x kelompok umur, ditambah kasus batas usia yang memindahkan model.
2. Perbandingan dilakukan pada nilai **sebelum** pembulatan ke `float(5,2)` milik kolom database.
3. Toleransi absolut ditetapkan di akhir B2 dari sebaran selisih yang benar-benar terukur,
   **bukan** dikarang di awal. Kalau selisih ternyata nol, toleransinya nol.
4. Test Go wajib **disaksikan merah** sebelum implementasi (Iron Law). Test yang tidak pernah
   gagal tidak membuktikan apa pun.
5. **Oracle wajib diperiksa sebelum dipercaya.** Bila sebuah cacat terbukti (lihat B1 di
   [`03-legacy-findings.md`](03-legacy-findings.md)), vektor DILARANG di-generate dari kode apa
   adanya — berlaku ADR-013 beserta aturan pemutusnya.
6. Estimator proksi ikut diuji terpisah: `estimateSbp`, `estimateTotalChol`, `estimateHdl`,
   `estimateScr`, `estimateHba1c`, `calculateEgfr`, `applyCalibration`.

**Konsekuensi.** Positif: paritas jadi terbukti, bukan diklaim; regresi ketahuan seketika;
menutup E1 tepat di titik yang paling berbahaya tanpa harus menulis test suite Laravel lengkap.
Negatif: menulis generator butuh waktu sebelum ada satu baris Go pun; oracle hanya sekuat
kebenaran Laravel — kalau Laravel sudah salah, Go akan salah dengan setia.

**Pembatal.** Kalau ditemukan `ClinicalRiskService` ternyata bergantung pada state di luar
argumennya, ia bukan fungsi murni dan pendekatan oracle harus dirancang ulang.

---

