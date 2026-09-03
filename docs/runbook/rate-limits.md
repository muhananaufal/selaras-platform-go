# Runbook — batas laju

Dua jalur dibatasi, dan alasannya berbeda.

| Jalur | Batas bawaan | Kunci | Yang dilindungi |
| :--- | :--- | :--- | :--- |
| `POST /register`, `POST /login`, `POST /password-reset/*`, `DELETE /delete-account` | **5 per menit** | alamat IP | penebakan kata sandi |
| endpoint yang mengantre pekerjaan LLM | **10 per menit** | pengguna | tagihan |

Jalur LLM yang dibatasi: personalisasi penilaian, memulai program coaching,
membuka thread, mengirim pesan coaching, membuat percakapan chat, mengirim pesan
chat, dan meminta panduan menu harian. Semuanya menerbitkan pekerjaan yang
dibayar per token.

## Mengapa kuncinya berbeda

**Autentikasi dibatasi per IP, bukan per akun.** Pembatasan per akun justru
memberi penyerang cara mengunci akun orang lain: cukup mencoba masuk berulang
kali dengan alamat surel korban, dan korbannya yang terkunci.

**Jalur LLM dibatasi per pengguna, bukan per IP.** Yang dilindungi adalah
tagihan, dan tagihan mengikuti akun. Membatasinya per IP akan menghukum satu
kantor yang berbagi satu alamat keluar.

## Mengapa 5 dan 10

Lima percobaan per menit adalah 7.200 sehari. Ruang kata sandi yang memenuhi
aturan minimum jauh lebih besar dari itu, jadi penebakan menjadi tidak praktis
tanpa membuat orang yang salah ketik tiga kali ikut terkunci.

Sepuluh permintaan LLM per menit jauh di atas yang dilakukan seseorang
sungguhan — membaca satu jawaban saja memakan waktu lebih lama — dan cukup ketat
untuk membatasi kerugian bila sebuah token dicuri.

Keduanya angka awal, bukan hasil pengukuran. Ia dinyatakan begitu di sini alih-
alih dibungkus dengan pembenaran yang tidak ada datanya.

## Perilaku saat ditolak

```
HTTP/1.1 429 Too Many Requests
Retry-After: 60

{"success":false,"message":"Too many requests. Try again in a moment.","code":"RATE_LIMITED"}
```

Bentuk galatnya sama dengan penolakan lain, sehingga klien tidak perlu cabang
khusus. `Retry-After` dalam detik.

## Saat Redis mati

Pembatasan **gagal-terbuka**: permintaannya diloloskan, dan itu dicatat sebagai
ERROR.

Ini kebalikan dari pemeriksaan pencabutan token, yang gagal-TERTUTUP (ADR-020).
Perbedaannya disengaja: pencabutan menjaga **siapa** yang boleh masuk, dan ragu
di sana berarti menolak. Pembatasan laju menjaga **seberapa sering**, dan Redis
yang mati tidak boleh menutup seluruh aplikasi untuk semua orang.

Cari baris ini kalau pembatasan tampak tidak bekerja:

```
ERROR rate limiting is unavailable; requests are passing unchecked limit=auth error=...
```

## Mengubah batasnya

| Variabel | Bawaan |
| :--- | :--- |
| `RATE_LIMIT_AUTH_REQUESTS` | 5 |
| `RATE_LIMIT_AUTH_WINDOW` | `1m` |
| `RATE_LIMIT_LLM_REQUESTS` | 10 |
| `RATE_LIMIT_LLM_WINDOW` | `1m` |

Nilai yang tidak bisa dibaca atau tidak positif **jatuh ke bawaan** dan dicatat
sebagai peringatan — bukan menjadi nol. Batas nol berarti setiap permintaan
ditolak, dan satu salah ketik di environment akan mematikan seluruh aplikasi.

### Lingkungan pengembangan

`deploy/compose/apps.yml` menyetel keduanya ke **500**. Alasannya disebutkan di
sana: suite test ujung ke ujung mendaftarkan puluhan akun dari satu alamat dalam
hitungan detik, dan itu persis bentuk yang ingin ditolak di produksi.

Yang dinaikkan hanya angkanya. Pembatasan **tidak dimatikan**, dan itu bukan
kebetulan: pembatasan yang mati di satu lingkungan adalah pembatasan yang tidak
pernah diuji di lingkungan mana pun, dan yang pertama kali menjalankannya
sungguhan adalah produksi.

## Batas yang perlu diketahui

- **Jendelanya tetap, bukan meluncur.** Seseorang bisa mengirim 5 permintaan di
  detik terakhir sebuah menit dan 5 lagi di detik pertama menit berikutnya.
  Jendela meluncur lebih adil tetapi menuntut penyimpanan per permintaan;
  jendela tetap muat dalam satu `INCR` dan cukup untuk yang dilindungi di sini.
- **Alamat IP berasal dari `gin.ClientIP()`**, yang menghormati
  `X-Forwarded-For` hanya dari proxy yang dipercaya. Bila gateway dipasang di
  belakang proxy baru, `SetTrustedProxies` harus ikut disetel — kalau tidak,
  pembatasan per IP membatasi alamat proxy-nya, bukan pemanggilnya.
- **Alamat yang tidak bisa diurai berbagi satu penghitung** bernama `unknown`.
  Itu terlalu ketat bagi mereka, dan itu pilihan yang disengaja: pembatasan yang
  bocor karena satu alamat gagal diurai tidak melindungi apa pun.
