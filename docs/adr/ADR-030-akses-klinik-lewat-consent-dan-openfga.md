# ADR-030 — Akses klinik: pemilik tetap pengguna, klinisi membaca lewat consent yang diperiksa OpenFGA

**Status.** Diterima (2026-09-26). Melengkapi ADR-024, tidak membatalkannya.

---

**Konteks.** ADR-024 menjawab satu pertanyaan: *siapa pemilik* sebuah sumber
daya. Jawabannya `user_id`, dan setiap service menegakkannya dengan satu
aturan: `sub` token harus sama dengan `user_id` permintaan (ADR-026,
`internal/platform/authn`). Aturan itu tidak memberi ruang bagi orang lain
untuk membaca, dan selama ini memang tidak ada yang membutuhkannya.

Wave 2 menambah klinik. Keputusan pemilik (2026-09-25) menetapkan aturannya:

- Klinik punya tiga peran: **pemilik, admin, klinisi**.
- Pasien adalah akun yang sudah ada. Tidak ada akun kedua dan tidak ada salinan
  data.
- Klinisi hanya boleh membaca **penilaian risiko** dan **progres coaching**
  pasien yang memberi consent **kepada klinisi itu**.
- **Chat tidak pernah terlihat.**
- Consent berlaku per klinisi, bisa dicabut, dan **setiap akses diaudit**.

Pertanyaan yang ditambahkan adalah *siapa yang boleh membaca atas izin
pemiliknya*. Pertanyaan itu berbeda dari kepemilikan dan tidak boleh
dijawab dengan melonggarkan aturan kepemilikan.

**Opsi mesin otorisasi.**

| Opsi | Kelebihan | Kekurangan | Yang membatalkannya |
| :--- | :--- | :--- | :--- |
| **A. OpenFGA (ReBAC gaya Zanzibar), tuple sebagai proyeksi dari consent ledger** | Model relasi tertulis sebagai kode dan diuji; pertanyaan "boleh baca?" punya satu jawaban untuk semua service; pencabutan dan keanggotaan klinik menjadi relasi, bukan `if` yang tersebar; pola standar untuk berbagi antar-pengguna | Komponen runtime baru (server dan datastore-nya); tuple adalah salinan kedua dari consent, jadi ada jeda proyeksi; satu lompatan jaringan di jalur baca klinisi | Model tetap datar (satu relasi, tanpa hierarki) setelah Wave 3 → opsi B lebih murah |
| B. RPC `Check` di clinic-svc atas tabelnya sendiri | Tanpa komponen baru; consent dan jawabannya di satu transaksi, tanpa jeda | Setiap aturan baru adalah kode baru di satu service; hierarki (klinik → klinisi → pasien) ditulis tangan sebagai JOIN | Model mulai punya hierarki atau lebih dari satu jenis sumber daya |
| C. Grant di dalam token (klaim JWT) | Nol panggilan saat membaca | Pencabutan menunggu token kedaluwarsa, **bertentangan dengan "bisa dicabut"**; token membesar per pasien | — ditolak |
| D. Service pemilik membaca tabel consent langsung | Tanpa lompatan jaringan | Membongkar isolasi skema per service (ADR-006) | — ditolak |

**Keputusan.**

1. **Kepemilikan tidak berubah.** RPC milik pengguna tetap `sub == user_id`.
   Akses klinisi masuk lewat **RPC baru yang terpisah**, misalnya
   `ListPatientAssessments`. RPC ini tidak melonggarkan interceptor
   kepemilikan. Pada RPC klinisi, `sub` adalah klinisi dan `patient_user_id`
   adalah subjek yang diperiksa.
2. **OpenFGA (opsi A)**, server v1.21.0 dan go-sdk v0.8.3 (Apache-2.0).
   Datastore-nya Postgres di basis data terpisah dengan perannya sendiri,
   karena `openfga migrate` membuat tabelnya sendiri. Modelnya:

   ```
   type user
   type clinic
     relations
       define owner: [user]
       define admin: [user] or owner
       define clinician: [user]
   type patient
     relations
       define care_clinic: [clinic]
       define consented_clinician: [user]
       define can_view_assessments: consented_clinician and clinician from care_clinic
       define can_view_coaching_progress: consented_clinician and clinician from care_clinic
   ```

   Model ini ada di `deploy/openfga/model.fga` dan diuji oleh
   `deploy/openfga/model.fga.yaml` dengan CLI resmi `fga` v0.8.1 (`fga model
   test`, keluar dengan kode 1 bila satu asersi gagal; diukur dengan asersi
   yang sengaja dibalik).

   - **Klinisi yang keluar dari klinik** kehilangan akses tanpa consent
     disentuh, karena syaratnya adalah irisan consent dan keanggotaan.
   - **Admin bukan klinisi.** Admin mengelola keanggotaan, bukan membaca data.
   - **Tidak ada relasi untuk chat,** jadi tidak ada tuple yang bisa
     membukanya. Test model memastikan tidak ada relasi `can_view_*` di luar
     kedua relasi di atas.
3. **clinic-svc** (skema `clinic`) memegang klinik, keanggotaan, **consent
   ledger**, dan **audit akses**. Kedua tabel terakhir *append-only*: peran
   runtime tidak punya `UPDATE`/`DELETE`, dan trigger menolak keduanya.
   - **Pemilik tabel bukan peran runtime.** Di service lain, migrasi berjalan
     dengan peran runtime (`svc_<svc>`, `deploy/compose/initdb/01-schemas.sh`),
     sehingga peran itu memiliki tabelnya. Pemilik bisa `DISABLE TRIGGER`
     dan `NO FORCE ROW LEVEL SECURITY` dengan DDL, jadi trigger dan RLS hanya
     sekuat peran yang tidak memiliki tabelnya. Skema `clinic` karena itu
     dimiliki peran migrasi `clinic_owner`, dan `svc_clinic` hanya mendapat
     `SELECT, INSERT` pada ledger dan audit.
   - Pencabutan adalah baris baru, bukan penghapusan.
   - Tuple OpenFGA adalah **proyeksi** ledger lewat outbox. Consent dan event
     proyeksinya ditulis dalam satu transaksi, lalu consumer menulis atau
     menghapus tuple. Consumer yang sama bisa membangun ulang semua tuple dari
     ledger.
4. **Service pemilik data memeriksa sendiri** (ADR-026 tetap berlaku):
   - Assessment dan coaching memanggil `Check` dengan **`HIGHER_CONSISTENCY`**.
     Nilai bawaan go-sdk adalah `MINIMIZE_LATENCY`
     [`go-sdk v0.8.3 model_consistency_preference.go:20-33`], yang boleh
     menjawab dari cache sesudah consent dicabut.
   - **OpenFGA tidak terjangkau → tolak** (gagal-tertutup, seperti pencabutan
     token di ADR-020).
5. **Setiap akses klinisi diaudit sebelum datanya dikembalikan.** Service
   pemilik menulis event akses ke outbox-nya.
   - Penulisan gagal → permintaan ditolak. Akses yang tidak tercatat tidak
     boleh terjadi.
   - clinic-svc menyimpannya di audit append-only, dan pasien bisa
     membacanya.
6. **Row Level Security** mulai dari skema `clinic`. Setiap transaksi
   menyetel `SET LOCAL app.user_id`, dan kebijakan membatasi baris consent
   dan audit ke pasien atau klinisi yang bersangkutan. `svc_clinic` bukan
   pemilik tabel, jadi kebijakan itu mengikatnya tanpa bisa dimatikan dari
   peran runtime; `FORCE ROW LEVEL SECURITY` tetap dipasang untuk pemiliknya.
   - `SET LOCAL` hidup selama transaksi, jadi ia aman di PgBouncer mode
     transaksi. Keadaan ini dibuktikan dengan test lewat PgBouncer, bukan
     diasumsikan.
   - RLS pada tabel lama service lain **tidak** masuk ADR ini. Semua query
     mereka harus lebih dulu berjalan dalam transaksi yang menyetel
     subjeknya, dan itu migrasi tersendiri.

**Konsekuensi.**

Positif:
- Kepemilikan dan akses terdelegasi adalah dua jalur yang tidak saling
  melonggarkan.
- Pencabutan berlaku begitu tuple terhapus, tanpa menunggu token
  kedaluwarsa.
- Pasien bisa melihat siapa membaca datanya dan kapan.

Negatif:
- Komponen runtime baru: server OpenFGA beserta datastore dan migrasinya.
- Ada jeda proyeksi (outbox → tuple) antara consent diberikan atau dicabut
  dan berlakunya. Untuk pencabutan, jeda ini adalah jendela di mana klinisi
  masih bisa membaca. Batasnya diukur dan dicatat di runbook, bukan
  diasumsikan.
- Jalur baca klinisi bergantung pada OpenFGA dan pada penulisan audit. Kalau
  salah satunya mati, klinisi tidak bisa membaca. Itu disengaja.

**Bukti yang wajib ada.**
- Test model OpenFGA:
  - Klinisi dengan consent dan keanggotaan boleh membaca.
  - Tanpa salah satunya, tidak boleh.
  - Admin tanpa consent tidak boleh.
  - Tidak ada relasi chat.
- Test kebocoran per jalur. Setiap RPC klinisi menolak:
  - klinisi tanpa consent;
  - klinisi yang consent-nya dicabut;
  - klinisi yang keluar dari klinik;
  - pasien lain.
- Pencabutan diukur dari commit ledger sampai `Check` menolak.
- Append-only dibuktikan: `UPDATE`/`DELETE` pada ledger dan audit gagal, dan
  `svc_clinic` tidak bisa `ALTER TABLE` (menonaktifkan trigger atau RLS).
- RLS dibuktikan lewat PgBouncer: dua transaksi berurutan di koneksi server
  yang sama tidak saling melihat baris.

**Pembatal.**
- Model tetap satu relasi datar sampai akhir Wave 3 → opsi B, dan OpenFGA
  dicabut dari stack.
- Jeda proyeksi pencabutan terukur melebihi yang bisa diterima pemilik →
  tuple ditulis sinkron setelah commit, dengan ledger tetap sebagai sumber
  kebenaran dan rekonsiliasi berkala.
- Tim di luar repo ini membutuhkan kebijakan berbasis atribut (jam kerja,
  lokasi) → condition OpenFGA atau mesin kebijakan terpisah, lewat ADR baru.
