# ADR-022 — RPC profil berkunci `user_id`, bukan `user_profile_id`

**Status.** Diterima. Dibuat saat mengerjakan F1-28, sebelum handler-nya ditulis.

---

**Konteks.** `GetProfileRequest` dan `UpdateProfileRequest` semula berkunci `user_profile_id`.
Nilai itu datang dari klaim token, dan ADR-002 aturan 2 menyatakan klaim itu **boleh kosong**
ketika profilnya belum ada.

Akibatnya sebuah keadaan yang mustahil dipulihkan: pengguna yang pembuatan profilnya gagal saat
mendaftar — kegagalan yang ADR-002 aturan 1 justru menyatakan boleh terjadi — tidak punya
`user_profile_id`, sehingga tidak bisa memanggil endpoint yang satu-satunya bisa membuatkan
profilnya. Ia terkunci di luar selamanya.

**Keputusan.** Kedua RPC berkunci `user_id`, dan `UpdateProfile` membuat profil bila belum ada,
persis seperti `updateOrCreate` di sistem lama.

**Alasan.** Pemanggilnya selalu punya `user_id` dari klaim `sub`; ia tidak pernah kosong. Biaya
pencariannya sama — satu lewat kunci primer, satu lewat indeks unik — jadi tidak ada yang
ditukar. Yang hilang hanya seluruh kelas keadaan yang tidak bisa dipulihkan.

**Klaim `user_profile_id` tetap berguna, dan ADR-007 tetap berlaku.** Ia bukan untuk service
ini, melainkan untuk unit lain — assessment, coaching, chat, nutrition — yang meng-FK
`user_profiles` dan karena itu butuh id-nya tanpa harus bertanya ke profile-svc lebih dulu.

**Konsekuensi.** Positif: tidak ada pengguna yang bisa terkunci di luar profilnya sendiri; jalur
pemulihan ADR-002 aturan 1 benar-benar ada, bukan hanya dinyatakan. Negatif: `UpdateProfile`
kini punya dua perilaku dalam satu RPC, dan pemanggil tidak bisa membedakan "diperbarui" dari
"baru dibuat" tanpa melihat `created_at`.

**Pembatal.** Kalau kelak pembuatan profil menjadi benar-benar transaksional bersama pendaftaran
— membatalkan ADR-002 aturan 1 — maka profil dijamin ada, `user_profile_id` tidak pernah kosong,
dan alasan utama keputusan ini gugur.

---
