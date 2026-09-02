# ADR-023 — Service tidak mempercayai identitas yang sekadar dikirimkan

**Status.** Diterima. Dibuat saat mengerjakan F2-15, setelah aturan yang sama muncul untuk
ketiga kalinya.

---

**Konteks.** Tiga kali dalam satu fase, pertanyaan yang sama muncul: sebuah service menerima
id pengguna di dalam permintaan, dan harus memutuskan apakah mempercayainya.

1. `Logout` menerima id pengguna. Kalau ia dipercaya, siapa pun yang bisa menjangkau
   identity-svc bisa mengeluarkan pengguna mana pun dari sesinya hanya dengan menebak id.
2. `GetAssessment` dan `ListAssessments` menerima `user_profile_id` sebagai pemilik. Kalau ia
   dipercaya, id orang lain akan membaca penilaian orang lain — kelas kelemahan yang sama
   dengan temuan S9.
3. `StartAssessment` menerima `user_profile_id` sebagai sasaran penyimpanan. Kalau ia
   dipercaya, penilaian bisa ditulis ke profil orang lain.

Ketiganya sama-sama dijawab "gateway sudah memverifikasi tokennya". Itu benar, dan itu bukan
alasan yang cukup: ia mengubah gateway menjadi satu-satunya penjaga, dan menjadikan setiap
service di belakangnya terbuka bagi apa pun yang bisa menjangkau jaringan internal.

**Keputusan.** Sebuah service TIDAK memakai identitas yang sekadar dikirimkan kepadanya sebagai
dasar otorisasi. Ia menurunkannya dari sesuatu yang bisa ia verifikasi sendiri, atau dari
sesuatu yang tidak bisa dipalsukan pemanggilnya.

**Penerapannya, tiga bentuk.**

| Kasus | Yang diterima | Yang dipakai |
| :--- | :--- | :--- |
| `Logout` | Token akses | identity-svc memverifikasi tanda tangannya sendiri, lalu memakai `sub` |
| Assessment, tulis dan baca | `user_id` | assessment-svc menanyakan `user_profile_id` ke profile-svc |
| Klaim `user_profile_id` di token | — | Tetap ada, tetapi untuk unit yang meng-FK `user_profiles` — bukan sebagai dasar otorisasi |

**Alasan `user_id` dipilih sebagai yang dikirimkan.** Ia tetap datang dari pemanggil, jadi ini
bukan pemindahan kepercayaan yang gratis — tetapi berbeda dari `user_profile_id`, ia bisa
diturunkan kembali ke sesuatu yang bisa diverifikasi: ia adalah `sub` di token, dan setiap
service bisa memeriksa tanda tangan token dengan kunci publik. Yang belum dilakukan hari ini
adalah memverifikasinya di setiap service; ADR ini menetapkan bentuknya, dan pembatal di bawah
menyebut kapan langkah itu wajib diambil.

**Yang dibayar.** Satu panggilan ke profile-svc pada setiap pembacaan riwayat penilaian.
Riwayat bukan jalur terpanas — ia dibuka beberapa kali per pengguna, bukan beberapa kali per
detik — jadi ADR-007 tidak terlanggar. Kalau kelak ia menjadi panas, cache dari event
(F2-16) menutupnya tanpa mengembalikan kepercayaan pada id yang dikirimkan.

**Konsekuensi.** Positif: bocornya jaringan internal tidak langsung berarti bocornya data
setiap pengguna; setiap service bisa menjelaskan sendiri mengapa ia percaya. Negatif: satu
panggilan tambahan di jalur baca; dan `user_profile_id` di kontrak assessment berganti menjadi
`user_id`, yang merupakan perubahan breaking.

**Pembatal.** Kalau kelak ada mTLS dengan identitas pemanggil yang bisa diverifikasi di setiap
tepi jaringan internal, "siapa pun yang bisa menjangkau service ini" tidak lagi berarti "siapa
pun", dan sebagian biaya di sini bisa ditinjau ulang. Sebaliknya, kalau satu service pun mulai
menerima permintaan dari luar mesh, verifikasi token per service berubah dari bentuk menjadi
keharusan.

---
