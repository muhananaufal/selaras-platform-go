# ADR-029 — Stream `Watch*` dibangunkan sinyal Redis dari pemilik data, dengan poll cadangan

**Status.** Diterima (2026-09-26).

---

**Konteks.** Lima stream `Watch*` di edge (`WatchAssessment`,
`WatchConversation`, `WatchProgram`, `WatchThread`, `WatchDailyGuide`)
menunggu hasil LLM dengan *poll di sisi server*: setiap stream yang terbuka
membaca service pemiliknya lewat gRPC setiap 2 detik, selama paling lama 5
menit (`internal/edge/service/watch.go`). Cara ini stateless dan benar di
berapa pun replika edge. Kelemahannya, biaya baca tumbuh bersama jumlah
stream yang terbuka, bukan bersama jumlah hasil yang benar-benar datang.
Satu stream menanggung 150 pembacaan dalam 5 menit meskipun hasilnya hanya
berubah sekali. Latensinya juga sampai 2 detik setelah hasil tersimpan.

Alur hasil saat ini:

1. `llm-worker` menerbitkan ke `llm.results`.
2. Consumer di service pemilik (assessment, chat, coaching, nutrition)
   menyimpan hasilnya dalam transaksi.
3. Edge baru melihat hasil itu pada tick poll berikutnya.

Setiap record `llm.results` membawa identitas agregatnya: header
`aggregate_type` (`assessment`, `conversation`, `coaching_program`,
`coaching_thread`, `meal_guide`) dan key partisi berupa id agregat. Keduanya diisi relay outbox, dan
consumer keempat service sudah membacanya untuk memilih record miliknya.

Yang harus dijaga: **sinyal tidak boleh mendahului commit pemilik data.**
Edge yang dibangunkan sebelum hasil tersimpan akan membaca keadaan lama, lalu
menunggu lagi.

**Opsi yang ditimbang.**

| Opsi | Kelebihan | Kekurangan | Yang membatalkannya |
| :--- | :--- | :--- | :--- |
| **A. Redis Pub/Sub: consumer pemilik menerbitkan sinyal `aggregate_type:id` setelah handle-nya commit; tiap replika edge punya satu subscriber; poll cadangan tetap ada** | Sinyal pasti sesudah commit; latensi hampir nol; siaran ke semua replika adalah perilaku bawaan Pub/Sub; Redis sudah ada di stack; pesannya hanya petunjuk (edge tetap membaca dari pemilik), jadi tidak ada data kesehatan yang lewat Redis | Pub/Sub *fire-and-forget*: sinyal hilang kalau Redis atau subscriber putus, sehingga poll cadangan tetap wajib; empat service mendapat dependensi Redis baru; setiap replika menerima semua sinyal lalu menyaringnya sendiri | Laju hasil LLM naik sampai penyaringan lokal terasa (lihat Pembatal) |
| B. Outbox transaksional → topic Kafka `watch.hints` → tiap replika edge membaca semua partisi tanpa consumer group | Sinyal atomik dengan datanya; tidak hilang selama edge tersambung; memakai outbox dan relay yang sudah ada | Latensi bertambah satu siklus relay (sampai 1 detik saat outbox kosong), hampir menghapus manfaat latensinya; edge mendapat klien Kafka dan membaca semua partisi di setiap replika; satu topic baru plus satu event per hasil di empat service | Sinyal yang hilang terbukti membuat pengguna menunggu sampai poll cadangan, cukup sering untuk dikeluhkan |
| C. Edge membaca `llm.results` langsung, tiap replika tanpa group | Tidak ada perubahan di service pemilik | **Balapan yang dijamin kalah:** record yang sama dibaca consumer pemilik dan edge bersamaan, sehingga edge sering bangun sebelum commit. Selain itu isi hasil LLM ikut mengalir ke setiap replika edge | — ditolak |
| D. Tetap poll, interval diperpanjang atau adaptif | Nol komponen baru | Latensi memburuk tepat di jalur yang ditunggu pengguna; biaya baca per stream tetap sebanding dengan waktu terbuka | Jumlah stream terbuka tetap kecil sampai Wave 5 (tidak ada tanda itu sekarang) |

**Keputusan.** Opsi A, dengan aturan berikut:

1. **Isi sinyal hanyalah petunjuk.** Pesannya `aggregate_type:aggregate_id`
   di satu channel (`watch.hints`). Edge yang menerimanya tetap membaca
   keadaan dari service pemilik lewat jalur gRPC yang sudah diotorisasi.
   Sinyal palsu atau salah hanya menambah satu pembacaan; sinyal tidak bisa
   membocorkan atau mengubah data.
2. **Diterbitkan consumer pemilik, setelah handle berhasil.** Record yang
   gagal lalu di-rewind tidak menerbitkan sinyal. Kegagalan menerbitkan
   hanya dicatat di log dan tidak mengembalikan offset: datanya sudah benar,
   dan poll cadangan akan menemukannya.
3. **Satu subscriber per replika edge,** dengan hub lokal yang memetakan
   kunci agregat ke stream yang menunggunya. Setiap resubscribe (go-redis
   menyambung ulang dan berlangganan ulang sendiri
   [`go-redis v9.22.0 pubsub.go:22-23`]) **membangunkan semua stream lokal**,
   karena sinyal selama putus tidak bisa diketahui. Pesan subscription dari
   `ChannelWithSubscriptions` [`pubsub.go:592-594`] yang menjadi pemicunya.
4. **Poll cadangan tetap ada, 10 detik** (sebelumnya 2 detik sebagai satu-
   satunya mekanisme). Ia menutup sinyal yang hilang di sisi penerbit: commit
   berhasil tetapi Redis sedang mati, atau proses mati di antara commit dan
   publish. Batas terburuk latensi turun dari "tidak pernah" menjadi 10
   detik, dan kasus normal naik dari "sampai 2 detik" menjadi hampir nol.
5. **Stream tetap selalu membaca sekali saat dibuka.** Pesan pertama stream
   selalu keadaan terkini, jadi sinyal sebelum stream dibuka tidak relevan.

**Konsekuensi.**

Positif:
- Pembacaan per stream turun dari sekitar 30 per menit menjadi 6 per menit
  (cadangan) ditambah satu per hasil yang benar-benar datang.
- Latensi hasil ke klien turun dari rata-rata sekitar setengah interval poll
  menjadi waktu satu publish Redis.
- Replika edge tetap setara: tidak ada afinitas sesi, dan replika mana pun
  bisa melayani stream mana pun.

Negatif:
- Assessment, chat, coaching, dan nutrition mendapat dependensi Redis
  (`REDIS_URL`). NetworkPolicy chart hanya membatasi ingress, jadi tidak ada
  aturan jaringan baru; yang bertambah adalah satu hal lagi yang bisa salah
  konfigurasi.
- Hilangnya Redis tidak mematikan stream, tetapi latensinya kembali ke batas
  cadangan 10 detik.

**Bukti yang wajib ada sebelum ADR ini dianggap terlaksana.**
- Test integrasi dengan dua hub (dua "replika") di atas Redis nyata:
  - sinyal untuk satu agregat membangunkan stream agregat itu di kedua
    replika, dan tidak membangunkan stream lain;
  - resubscribe membangunkan semua stream;
  - saat Redis mati, stream tetap selesai lewat poll cadangan.
- Setiap consumer: sinyal diterbitkan setelah handle berhasil dan tidak
  diterbitkan saat handle gagal.
- Suite e2e yang sudah menunggu lewat stream (kurikulum, thread, chat,
  panduan) tetap hijau.
- Metrik `edge_watch_fetches_total{reason}` (`open`, `hint`, `fallback`,
  `resubscribe`) supaya biaya baca terukur, bukan diasumsikan.

**Pembatal.**
- Laju sinyal membuat CPU penyaringan di edge terlihat di profil → channel
  per agregat atau per shard, bukan satu channel.
- Sinyal hilang cukup sering sampai pengguna menunggu batas cadangan secara
  teratur → opsi B (outbox transaksional).
- Redis menjadi terkluster → `SSUBSCRIBE` (sharded Pub/Sub) dengan kunci
  shard yang sama.
