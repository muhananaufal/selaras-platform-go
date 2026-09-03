# ADR-024 — Pemilik sebuah sumber daya adalah pengguna, bukan profilnya

**Status.** Diterima. Dibuat saat mengerjakan F4-16, setelah kekeliruan yang sama muncul untuk
kelima kalinya.

---

**Konteks.** Lima kali sekarang, sebuah kontrak menetapkan `user_profile_id` sebagai kunci
kepemilikan, dan lima kali itu keliru:

1. `profile.v1` mengunci RPC-nya pada `user_profile_id` — diperbaiki di ADR-022.
2. `assessment.v1` melakukan hal yang sama — diperbaiki di ADR-023.
3. `ProfileUpdated` hanya membawa `user_profile_id`, sehingga cache yang dibangun darinya tidak
   bisa dicari — diperbaiki saat F2-16.
4. `coaching.v1` mengunci seluruh dua belas RPC-nya pada `user_profile_id`.
5. Skema `coaching_programs` di sistem lama memakai `user_profile_id` sebagai pemilik, sementara
   `chat` di sistem yang sama memakai `user_id`.

Yang kelima itu bukan kebetulan: ia **temuan S9**, dan bunyinya persis "dua pola identitas,
bukan satu".

**Mengapa id profil salah sebagai kunci kepemilikan.** Tiga alasan, dan masing-masing cukup
sendirian.

*Pertama, profil boleh belum ada.* ADR-002 aturan 2 menyatakan profil bisa kosong seluruh
bidangnya, dan ADR-002 aturan 1 menyatakan pendaftaran yang pembuatan profilnya gagal tetap
sah. Sumber daya yang dikunci pada id profil akan mengunci keluar justru pengguna yang
aturan-aturan itu izinkan ada.

*Kedua, ia harus diterjemahkan lebih dulu.* Identitas yang terverifikasi di setiap permintaan
adalah `user_id` — ia yang ada di dalam token. Memakai id profil berarti setiap permintaan
menerjemahkannya dulu, dan penerjemahan itu adalah panggilan jaringan yang tidak menambah
apa pun kecuali satu cara baru untuk gagal.

*Ketiga, ia mengundang kepercayaan yang salah.* Id profil tidak ada di dalam token, jadi ia
harus datang dari badan permintaan — dan sesuatu yang datang dari badan permintaan tidak boleh
menjadi dasar otorisasi (ADR-023).

**Opsi.** (a) `user_id` sebagai kunci kepemilikan di seluruh kontrak · (b) `user_profile_id`,
dengan setiap service menerjemahkannya sendiri · (c) keduanya, masing-masing untuk service yang
berbeda.

**Keputusan.** (a). **Kepemilikan selalu dinyatakan dengan `user_id`.** Id profil tetap ada di
tempat ia memang berarti — misalnya sebagai isi cuplikan profil — tetapi ia tidak pernah menjadi
jawaban atas pertanyaan "siapa pemilik ini".

Konsekuensinya untuk coaching: `coaching_programs.user_id`, dan seluruh dua belas RPC di
`coaching.v1` di-key pada `user_id`.

**Yang membatalkan keputusan ini.** Kalau kelak ada sumber daya yang benar-benar dimiliki
sebuah profil dan bukan penggunanya — misalnya profil bersama yang dipakai beberapa akun.
Sampai itu ada, satu pengguna punya paling banyak satu profil, dan dua kunci untuk satu
pertanyaan hanya menambah cara untuk keliru.

**Konsekuensi yang tidak menyenangkan.** Kontrak `coaching.v1` berubah sebelum ada satu pun
klien yang memakainya. Itu murah sekarang dan mahal nanti — dan alasan ia ditemukan sekarang
adalah karena kontraknya dibaca sebelum ada yang dibangun di atasnya, bukan karena ada yang
memeriksanya belakangan.

**Rujukan.** ADR-002, ADR-022, ADR-023 · temuan S9 · `03-legacy-findings.md`.
