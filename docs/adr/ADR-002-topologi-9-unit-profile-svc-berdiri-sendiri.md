# ADR-002 — Topologi: 9 unit, profile-svc berdiri sendiri


**Konteks.** Keputusan ini sempat dibalik dua kali, dan riwayatnya dicatat karena argumen yang
gugur di tengah jalan adalah bagian dari alasan keputusan akhirnya.

Versi pertama ADR ini memilih **8 unit** — `profile-svc` dilebur ke `identity-svc` — dengan
argumen utama: `RegisterUserAction` menulis `users` dan `user_profiles` dalam satu
`DB::transaction`, sehingga memisahkannya membuat register jadi saga.

**Argumen itu gugur.** Temuan B7 di [`03-legacy-findings.md`](03-legacy-findings.md) menunjukkan
`FindOrCreateUserFromSocialiteAction` **tidak membuat profil sama sekali**, dan sistem tetap
berjalan — `UserProfileController::show` memang menangani `if (!$user->profile)`. Jadi "user
tanpa profil" adalah state yang sudah sah hari ini. Atomicity yang saya kira invarian bisnis
ternyata kebetulan implementasi, dan konsekuensinya besar: **pemisahan ini tidak membutuhkan saga
sama sekali.**

**Opsi.** (a) 8 unit, profile lebur ke identity · (b) **9 unit, profile-svc terpisah** ·
(c) 4 unit (edge, core, llm-worker, dashboard).

**Keputusan.** (b), 9 unit.

**Alasan.** `users` dan `user_profiles` menjawab dua pertanyaan yang berbeda: yang pertama
"boleh masuk, dengan hak apa", yang kedua "orangnya siapa". Dan pembagian kerjanya timpang ke
arah yang menentukan: `user_profiles` adalah **hub** yang di-FK oleh empat domain (E15) dan
satu-satunya sumber masukan mesin risiko (`date_of_birth`, `sex`, `country_of_residence`),
sementara `users` nyaris tidak dipakai siapa pun kecuali edge untuk autentikasi. Melebur keduanya
membuat `identity-svc` melayani data yang bukan urusan identitas.

**Aturan yang mengikat.**

1. **Pembuatan profil bersifat best-effort, bukan transaksional.** Saat register, identity-svc
   membuat user lalu meminta profile-svc membuat profil kosong. Bila langkah kedua gagal, register
   **tetap berhasil** — hasilnya adalah state yang memang sudah sah (B7). Tidak ada saga, tidak
   ada kompensasi. Event `user.registered` dipakai untuk rekonsiliasi belakangan.
2. **`user_profile_id` diambil sekali per login, bukan per request.** identity-svc memintanya ke
   profile-svc saat menerbitkan token, lalu menaruhnya sebagai klaim (ADR-007). Bila profil belum
   ada, klaim itu kosong dan setiap konsumen wajib menangani ketiadaannya.
3. **`risk_region` bukan milik profile-svc.** Ia konsep klinis, bukan demografis: profile-svc
   menyimpan `country_of_residence`, dan assessment-svc memetakannya lewat
   `config/region_mapping.php`. Karena `UserProfileResource` mengekspos `risk_region` ke API,
   **edge-gateway yang menggabungkan keduanya** — konsekuensi langsung dari pemisahan ini, dan
   ia dikerjakan di task F1-30.
4. **Batas modul tetap dijaga meski unitnya terpisah.** Aturan "dilarang query lintas schema"
   (ADR-006) berlaku penuh: assessment-svc tidak boleh menyentuh tabel `user_profiles`, ia
   mengonsumsi event `profile.updated` dan menyimpan salinannya sendiri (F2-16).

**Konsekuensi.** Positif: batas tanggung jawab cocok dengan cara data benar-benar dipakai;
profile bebas tumbuh — dan `culinary_preferences` yang menumpang sebagai kolom JSON adalah gejala
bahwa ia memang akan tumbuh; assessment bergantung pada unit yang benar-benar memiliki datanya.
Negatif: satu unit lagi untuk dibangun, di-deploy, dan dipelihara oleh satu orang, sehingga R1
naik; pengambilan `user_profile_id` menambah satu panggilan pada jalur login; `risk_region` kini
butuh agregasi di edge yang sebelumnya gratis lewat accessor Eloquent.

**Pembatal.** Bila `user_profiles` ternyata akan tetap enam kolom demografi selamanya, peleburan
adalah keputusan yang lebih murah dan ADR ini gugur. Sinyal yang membantah itu sudah ada:
`culinary_preferences` ditempelkan ke sana lewat migrasi belakangan, bukan sejak awal.

---

