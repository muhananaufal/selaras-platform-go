# ADR-007 — `user_profile_id` diangkat jadi identitas global


**Konteks.** E15: `risk_assessments`, `coaching_programs`, `conversations`, dan
`daily_meal_guides` semuanya ber-FK ke `user_profile_id`, bukan ke `user_id`. Profil bukan domain
pinggiran — ia identitas yang dipakai setiap domain.

**Opsi.** (a) sertakan `user_profile_id` di dalam klaim token dan di setiap event ·
(b) tiap service memanggil identity-svc untuk menukar `user_id` jadi `user_profile_id` ·
(c) ganti seluruh FK jadi `user_id` dan buang lapisan profil.

**Keputusan.** (a). Token yang diterbitkan identity-svc membawa `user_id` **dan**
`user_profile_id`. Setiap event domain membawa keduanya di header.

**Konsekuensi pemisahan profile (ADR-002).** Karena `user_profiles` kini dimiliki profile-svc,
identity-svc mengambil `user_profile_id` darinya **saat menerbitkan token** — sekali per login,
bukan sekali per request. Bila profil belum ada (jalur Socialite, B7), klaim itu kosong dan
setiap konsumen wajib menanganinya sebagai state yang sah, bukan sebagai galat.

**Konsekuensi.** Positif: menghapus satu panggilan jaringan wajib dari **setiap** request
terautentikasi; skema yang ada tidak perlu diubah; event bisa dikonsumsi tanpa lookup balik.
Negatif: kalau relasi user-profil berubah, token lama membawa nilai basi sampai kedaluwarsa;
token jadi sedikit lebih besar; `user_profile_id` jadi bagian dari kontrak dan tidak bisa
diubah bebas.

**Pembatal.** Kalau relasi user-profil berubah dari satu-ke-satu, keputusan ini gugur dan (b)
menjadi satu-satunya yang benar.

---

