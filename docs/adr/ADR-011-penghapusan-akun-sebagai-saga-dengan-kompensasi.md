# ADR-011 — Penghapusan akun sebagai saga dengan kompensasi


**Konteks.** `users` memakai `softDeletes()` dan `DeleteUserAccountAction` menghapus lintas
seluruh domain dalam satu transaksi. Setelah dipecah, satu penghapusan menyentuh 6 unit dan
tidak bisa lagi atomik.

**Opsi.** (a) saga koreografi lewat event `user.deletion.requested` dengan konfirmasi per unit ·
(b) orkestrasi terpusat di identity-svc yang memanggil setiap service · (c) hapus di identity
saja dan biarkan sisanya jadi data yatim.

**Keputusan.** (a), dengan status penghapusan yang dilacak dan **verifikasi akhir** yang
memastikan sisa data benar-benar nol.

**Konsekuensi.** Positif: satu-satunya bentuk yang jujur di sistem terdistribusi; jadi bahan
demonstrasi saga yang nyata, bukan contoh buatan. Negatif: penghapusan tidak lagi seketika;
butuh pelacakan status dan penanganan kegagalan sebagian; menguji jalur kompensasi itu repot.

**Pembatal.** Kalau ternyata cukup soft-delete di identity dan seluruh pembacaan sudah difilter
lewat identitas, (c) secara teknis memadai — **tetapi ADR-016 melarang memilihnya atas dasar belum
adanya pengguna**, dan nilainya sebagai
demonstrasi hilang.

---

