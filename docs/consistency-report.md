# Laporan konsistensi — read-model dasbor

Dasbor tidak lagi dibaca dari sumbernya. Ia **proyeksi**: salinan yang dibangun
dari event, dan salinan selalu tertinggal. Dokumen ini menyatakan seberapa jauh,
**diukur, bukan ditaksir**.

Kejujuran itu bagian dari rancangannya: respons `GET /dashboard` membawa
`projected_at`, waktu peristiwa terakhir yang sudah masuk. Jeda yang
disembunyikan tampak seperti bug.

## Apa yang digantikan

Sistem lama membaca dasbor dari empat repository sekaligus, membungkusnya dengan
`Cache::remember` selama 15 menit, lalu memasang **empat listener** yang
menghapus cache itu secara manual saat sesuatu berubah (E17).

Dua sifatnya patut dibandingkan dengan yang sekarang:

| | Sistem lama | Read-model |
| :--- | :--- | :--- |
| Jeda terburuk | **15 menit** — bila listener-nya tidak jalan atau tidak ada | di bawah satu detik (diukur di bawah) |
| Cara gagalnya | listener yang lupa ditambahkan → cache basi, **tanpa tanda apa pun** | proyektor menahan offset dan mencatat galat tiap detik |
| Menambah fakta baru | tulis listener kelima, dan ingat menuliskannya | tambah satu cabang di proyektor; yang lupa menghasilkan kolom kosong yang terlihat |

Perbedaan yang penting bukan angkanya, melainkan **cara gagalnya**. Cache yang
basi terlihat persis seperti cache yang benar.

## Cara diukur

Diukur ujung ke ujung, lewat HTTP, terhadap tujuh service yang berjalan di
Docker pada satu mesin pengembangan:

1. `POST /api/v1/risk-assessments` — dicatat waktunya saat permintaan dikirim.
2. `GET /api/v1/dashboard` diulang tiap 50 ms sampai `total_assessments` naik.
3. Selisihnya dicatat.

Jadi angkanya memuat **seluruh rantai**, bukan hanya proyeksinya: penulisan
penilaian, penulisan baris outbox, relay yang mengambilnya, perjalanan lewat
Kafka, proyektor yang menerapkannya, dan pembacaan dasbor lewat gateway.

## Hasil

| Sampel | Jeda |
| :--- | ---: |
| 1 | 444 ms |
| 2 | 782 ms |
| 3 | 920 ms |
| 4 | 781 ms |
| 5 | 798 ms |

Lima sampel, rentang **444–920 ms**. Terlalu sedikit untuk menyebut persentil,
dan tidak disebut.

Satu pengukuran sebelumnya, terpisah, menghasilkan 1204 ms — pengukuran pertama
setelah `dashboard-svc` baru dinyalakan, saat consumer group-nya masih
bergabung. Angka itu disertakan karena ia keadaan yang sesungguhnya terjadi
setelah penggelaran, bukan pencilan yang dibuang.

## Dari mana jedanya berasal

Sumber terbesarnya **bukan** Kafka maupun proyeksinya, melainkan relay outbox:
ia menyapu tabelnya setiap **1 detik** (`RelayOptions{Interval: time.Second}`).
Sebuah penilaian yang ditulis tepat setelah satu sapuan menunggu hampir satu
detik penuh sebelum eventnya berangkat. Itu menjelaskan rentangnya: 444 ms untuk
yang beruntung, ~800–900 ms untuk yang tidak.

Menurunkan interval itu akan menurunkan jedanya secara langsung, dengan harga
kueri yang lebih sering ke tabel outbox. Angka satu detik dipilih saat F3 dan
belum ada alasan mengubahnya — **tetapi sekarang alasannya bisa diukur**, bukan
diperdebatkan.

## Yang TIDAK dijamin

- **Bukan read-your-writes.** Pengguna yang menyelesaikan analisis lalu langsung
  membuka dasbor bisa melihatnya belum ada. Klien sebaiknya menampilkan hasil
  penilaian dari respons `POST` itu sendiri, bukan menunggu dasbor.
- **Angka di atas dari satu mesin pengembangan**, satu broker, satu partisi
  aktif, tanpa beban lain. Ia bukan angka produksi dan tidak boleh dikutip
  sebagai itu.
- **Tidak ada batas atas yang dijamin.** Broker yang mati membuat jedanya
  sepanjang matinya. Yang dijamin adalah tidak ada event yang HILANG: outbox
  menahan yang belum terkirim, dan proyektor menahan offset yang gagal.

## Bagaimana kalau proyeksinya salah

Bangun ulang. Read-model tidak memiliki satu fakta pun, dan itu bukan klaim -
itu diuji:

```sh
DASHBOARD_DATABASE_DSN='...' KAFKA_BROKERS='...' \
  go run ./cmd/dashboard-rebuild -yes
```

Perintah itu menghapus seluruh isi read-model lalu memutar ulang ketiga topic
dari awal, memakai consumer group sendiri sehingga proyektor yang sedang
berjalan tidak terganggu.

**Bukti pada data sungguhan sesi ini:** `GET /dashboard` disimpan sebelum
rebuild, rebuild dijalankan (29 pesan dibaca, 19 diterapkan, 10 dilewati karena
event lama terbit sebelum `user_id` ada di kontraknya), lalu `GET /dashboard`
dibaca lagi. Hasilnya **identik, byte per byte**, dan `events_applied` kembali ke
19 yang sama.

Sepuluh event yang dilewati itu sendiri layak diperhatikan: proyektor
**mencatat** setiap satunya beserta id programnya, bukan menebak pemiliknya.
Menebak berarti menulis program seseorang ke dasbor orang lain.

## Cara mengukur ulang

Tidak ada perkakas khusus; yang di atas dilakukan dengan `curl` dan `date`.
Yang perlu dijaga saat mengulanginya:

- Ukur dari **luar**, lewat gateway. Mengukur dari dalam proyektor hanya
  mengukur proyektornya.
- Jangan mengukur permintaan pertama setelah service dinyalakan, kecuali memang
  itu yang ingin diketahui — consumer group yang baru bergabung menambah waktu.
- `projected_at` di respons adalah waktu **peristiwa**, bukan waktu pemrosesan.
  Selisihnya terhadap jam sekarang adalah lag proyeksi secara keseluruhan, dan
  itu angka yang berbeda dari yang diukur di atas.
