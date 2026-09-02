# ADR-016 — Bangun seolah produksi, bukan seolah latihan


**Konteks.** Tujuan proyek ini portofolio, dan itu melahirkan godaan yang halus: menurunkan
standar dengan alasan "tidak ada pengguna nyata". Alasan itu sudah menyelinap ke beberapa
keputusan sebelumnya — `bcrypt` dipertahankan atas nama paritas yang tidak ada, dan lensa
Security sempat diturunkan karena datanya bukan data sungguhan.

**Keputusan.** **"Tidak ada pengguna nyata" DILARANG dipakai sebagai alasan menurunkan standar
rekayasa.** Sistem dibangun sebagaimana engineer membangunnya untuk produksi sungguhan.
Alasannya bukan idealisme: yang dinilai dari portofolio bukan fiturnya — fitur ini sudah ada di
Laravel — melainkan **apakah sistemnya dibangun seperti sistem yang benar-benar dipakai orang.**
Jalan pintas adalah persis hal yang paling terlihat.

**Batas yang sama pentingnya — dan lebih sering dilanggar.** "Semirip industri nyata" **bukan**
berarti memakai setiap teknologi yang dipakai perusahaan besar. Industri nyata tidak melakukan
sharding pada sebelas tabel tanpa pengguna. Meniru artefak sistem berskala besar tanpa memiliki
masalah yang melahirkannya adalah *cargo cult*, dan pembaca berpengalaman membacanya sebagai
tanda ketidakdewasaan — bukan kematangan.

Dua sisi aturan ini, dan keduanya mengikat:

| Sisi | Aturan |
| :--- | :--- |
| **Jangan turunkan standar rekayasa** | Keamanan, penanganan galat, observability, backup, dan disiplin data diperlakukan seolah ada pengguna sungguhan |
| **Jangan naikkan skala buatan** | Kompleksitas hanya sah bila ada masalah yang melahirkannya. Solusi tanpa masalah adalah beban, dan terbaca sebagai *resume-driven development* |

**Uji yang dipakai untuk membedakan keduanya.** Sebelum menambahkan sesuatu, jawab:
*"Pada skala sistem ini, dengan sebelas tabel dan 32 endpoint, apakah engineer berpengalaman
akan memasang ini di sistem produksi sungguhan?"* Bila jawabannya tidak, ia tidak masuk —
seberapa pun mengesankan namanya.

**Konsekuensi yang langsung berlaku.**

1. `bcrypt` diganti **`argon2id`** — bukan karena "tidak ada hash lama", melainkan karena itu yang
   dipilih untuk sistem baru di produksi.
2. Data uji diperlakukan sebagai data sensitif: tidak masuk log, tidak masuk pesan galat, tidak
   ikut ter-commit.
3. Backup dan pemulihan **diuji**, bukan diasumsikan — pemulihan yang tidak pernah dicoba bukan
   backup.
4. Kredensial tidak pernah berbentuk nilai bawaan di kode, sekalipun untuk lingkungan lokal.
5. Sebaliknya: sharding, service mesh, dan multi-region **tetap ditolak**, karena tidak ada
   masalah pada skala ini yang melahirkannya.

**Konsekuensi.** Positif: menghapus seluruh kelas jalan pintas yang paling merusak nilai
portofolio; membuat keputusan bisa dipertahankan tanpa embel-embel "ini kan cuma latihan".
Negatif: beberapa pekerjaan jadi lebih mahal daripada yang dibutuhkan proyek tanpa pengguna —
uji pemulihan backup salah satunya; dan aturan ini bisa disalahgunakan untuk membenarkan
kompleksitas apa pun bila sisi keduanya diabaikan.

**Pembatal.** Bila sebuah tuntutan "seolah produksi" ternyata memakan waktu yang mengancam R1
(proyek mangkrak), ia diturunkan prioritasnya dan alasannya ditulis — bukan dihapus diam-diam.

---

