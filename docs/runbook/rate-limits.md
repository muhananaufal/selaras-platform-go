# Runbook — batas laju

Dua jalur dibatasi, dan alasannya berbeda.

| Jalur | Batas bawaan | Kunci | Yang dilindungi |
| :--- | :--- | :--- | :--- |
| `Auth/Register`, `Auth/Login`, `Auth/RequestPasswordReset`, `Auth/ConfirmPasswordReset`, `Auth/ExchangeSocialSession`, `Auth/DeleteAccount` | **5 per menit** | alamat IP | penebakan kata sandi dan kode |
| endpoint yang mengantre pekerjaan LLM | **10 per menit** | pengguna | tagihan |

Jalur LLM yang dibatasi: personalisasi penilaian, memulai program coaching,
membuka thread, mengirim pesan coaching, laporan kelulusan (memicu laporan baru
setiap kali yang sebelumnya gagal), membuat percakapan chat, mengirim pesan
chat, dan meminta panduan menu harian. Semuanya menerbitkan pekerjaan yang
dibayar per token.

Daftarnya ada di `edge.RateLimitPolicies`, dan dua test menjaganya:
setiap prosedur publik wajib dibatasi per alamat, dan setiap prosedur yang
memakai `Idempotency-Key` (tanda ia mengantre pekerjaan LLM) wajib dibatasi
per pengguna.

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
Retry-After: 12

{"code":"resource_exhausted","message":"too many requests, try again in a moment",
 "details":[{"type":"google.rpc.RetryInfo","value":"…","debug":{"retryDelay":"12s"}}]}
```

Bentuk galatnya sama dengan penolakan lain (galat Connect), sehingga klien
tidak perlu cabang khusus. Tunggunya dikirim dua kali: `Retry-After` dalam
detik untuk klien apa pun, `RetryInfo` untuk klien hasil generate. Contoh di
atas adalah permintaan autentikasi keenam dalam satu menit: kuota lima sudah
habis, dan satu permintaan baru diberikan kembali 12 detik kemudian.

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

- **Algoritmanya GCRA, bukan jendela tetap** (sejak 2026-09-26, pustaka
  `go-redis/redis_rate` v10). Seluruh kuota boleh dipakai sekaligus, lalu satu
  permintaan diberikan kembali setiap `jendela / jumlah`, yaitu 12 detik untuk
  autentikasi dan 6 detik untuk LLM. Status per subjek hanya satu stempel
  waktu, dievaluasi atomik oleh skrip Lua di Redis dengan jam Redis sendiri,
  sehingga semua replika gateway menilai dengan waktu yang sama.
  - **Alasan penggantian.** Jendela tetap direset oleh jam, bukan oleh
    pemanggil. Menghabiskan kuota tepat sebelum batas jendela memberi kuota
    baru tepat sesudahnya. Pengukurannya: 10 permintaan lolos dalam sekitar
    100 ms untuk batas 5 per detik
    (`TestSpendingTheBudgetAtAWindowBoundaryDoesNotDoubleIt`).
  - **Satu kuota lintas replika.** Klien yang membagi percobaannya ke dua
    replika tetap mendapat satu kuota (`TestTwoReplicasShareOneBudget`).
  - **Redis mati tetap gagal-terbuka** (`TestADeadRedisLetsRequestsThrough`).
  - **`Retry-After`** kini waktu sampai permintaan berikutnya diizinkan,
    dibulatkan ke atas ke detik, bukan panjang jendela penuh.
- **`X-Forwarded-For` hanya dipercaya dari `TRUSTED_PROXY_CIDRS`** (Helm:
  `rateLimit.trustedProxyCIDRs`), dibaca dari kanan melewati hop yang
  dipercaya. Kosong - bawaan - berarti memakai alamat peer langsung, benar
  bila gateway dijangkau langsung seperti di compose. Di belakang Ingress,
  nilai ini WAJIB diisi rentang proxy; kalau tidak, semua pengguna berbagi
  satu penghitung. Gateway REST sebelumnya memakai `gin.ClientIP()` tanpa
  `SetTrustedProxies`, dan gin bawaannya memercayai semua alamat: header
  palsu memberi penghitung baru tiap permintaan. Itu ditutup di ADR-027,
  dengan test regresi `TestSpoofedForwardedForDoesNotResetTheLimit`.
- **Alamat yang tidak bisa diurai berbagi satu penghitung** bernama `unknown`.
  Itu terlalu ketat bagi mereka, dan itu pilihan yang disengaja: pembatasan yang
  bocor karena satu alamat gagal diurai tidak melindungi apa pun.
