# Runbook — penghapusan akun

Penghapusan akun adalah **saga** lintas enam unit yang masing-masing memiliki
basis datanya sendiri. Tidak ada transaksi yang bisa merangkul keenamnya, jadi
yang menggantikannya adalah satu permintaan, enam konfirmasi, dan catatan
tentang siapa yang belum menjawab.

## Apa yang terjadi

```
DELETE /api/v1/delete-account   { "password": "..." }
        |
        |  kata sandi DIVERIFIKASI (S2)
        v
  identity: catat saga + tulis user.deletion.requested  (satu transaksi)
        |
        +--> profile     hapus, konfirmasi  \
        +--> assessment  hapus, konfirmasi   |
        +--> coaching    hapus, konfirmasi   |  masing-masing satu transaksi:
        +--> chat        hapus, konfirmasi   |  penghapusan + konfirmasinya
        +--> nutrition   hapus, konfirmasi   |
        +--> dashboard   hapus, konfirmasi  /
        |
        v
  identity: keenam menjawab berhasil -> akun dihapus, saga 'completed'
            satu atau lebih gagal    -> akun DITAHAN,  saga 'failed'
```

Jawabannya **202**, bukan 204. Penghapusannya belum selesai saat permintaan
dijawab, dan mengatakan "sudah hilang" pada saat datanya masih ada di
mana-mana adalah kebohongan yang akan ditampilkan klien apa adanya.

## Mengapa akun dihapus TERAKHIR

Menghapus akun lebih dulu menghilangkan satu-satunya tempat yang tahu
penghapusan itu sedang berjalan. Unit yang gagal menghapus datanya lalu tidak
punya siapa pun untuk dilapori, dan datanya tertinggal tanpa `user_id` hidup
yang bisa menemukannya lagi.

Karena alasan yang sama, **satu unit gagal berarti akunnya ditahan**.
Penghapusan tidak bisa dibatalkan — data yang sudah hilang di lima unit tidak
kembali — jadi kompensasinya bukan mengembalikan keadaan, melainkan menahan
satu-satunya kunci yang masih bisa menemukan sisanya.

## Melihat saga yang menggantung

identity-svc mencatatnya **saat start-up**, karena saga yang menggantung dari
proses sebelumnya tidak akan menyelesaikan dirinya sendiri:

```
WARN there are unfinished account deletions; see docs/runbook/account-deletion.md count=1
WARN an account deletion is unfinished saga_id=01a0665c-... awaiting=[dashboard] failures=0
```

Atau langsung dari basis datanya:

```sql
SELECT s.id, s.user_id, s.requested_at,
       array_agg(c.service ORDER BY c.service) FILTER (WHERE c.succeeded)     AS confirmed,
       array_agg(c.service ORDER BY c.service) FILTER (WHERE NOT c.succeeded) AS failed
FROM identity.deletion_sagas s
LEFT JOIN identity.deletion_confirmations c ON c.saga_id = s.id
WHERE s.status = 'requested'
GROUP BY s.id
ORDER BY s.requested_at;
```

Alasan kegagalannya:

```sql
SELECT service, failure_reason, confirmed_at
FROM identity.deletion_confirmations
WHERE saga_id = '...' AND NOT succeeded;
```

## Menyelesaikan saga yang macet

Ada dua bentuk macet, dan keduanya ditangani berbeda.

### 1. Satu unit menjawab GAGAL

Sagalnya sudah `failed`, akunnya ditahan. `failure_reason` menyebutkan
sebabnya. Perbaiki penyebabnya, lalu **minta penghapusan ulang lewat API** —
saga baru akan dimulai, dan unit yang sudah menghapus datanya akan menghapus
"tidak ada apa-apa" tanpa galat, karena setiap penghapusan idempoten.

### 2. Satu unit tidak pernah menjawab

Sagalnya masih `requested`, dan selama itu **permintaan penghapusan baru untuk
akun yang sama akan DITOLAK** dengan `FAILED_PRECONDITION` - satu pengguna
hanya boleh punya satu saga berjalan. Itu benar untuk saga yang memang sedang
berjalan, tetapi berarti saga yang macet menghalangi jalan keluarnya sendiri.

Setelah penyebabnya diperbaiki dan permintaannya tetap tidak datang lagi, tutup
sagalnya sebagai gagal - itu keadaan yang sebenarnya - lalu minta penghapusan
ulang lewat API:

```sql
UPDATE identity.deletion_sagas
SET status = 'failed', finished_at = now()
WHERE id = '<saga_id>' AND status = 'requested';
```

Akunnya tetap ditahan oleh langkah itu, dan penghapusan ulang aman: setiap unit
menghapus secara idempoten, jadi yang sudah bersih akan menghapus "tidak ada
apa-apa" tanpa galat.

#### Yang harus diperiksa lebih dulu

Sagalnya masih `requested`, `awaiting` menyebut unitnya. Yang harus diperiksa,
berurutan:

1. **Unitnya berjalan?** `docker ps`.
2. **Konsumen penghapusannya menyala?** Cari `deletion consumer started` di
   log-nya. Kalau tidak ada, `KAFKA_BROKERS` kemungkinan tidak diisi — itu
   dicatat sebagai peringatan saat start-up.
3. **Ia mencatat kegagalan?** Cari `could not delete this unit's data`.
4. **Relay outbox-nya berjalan?** Konfirmasi ditulis ke outbox unit itu dan
   dikirim relay. Unit tanpa relay akan menghapus datanya lalu diam.

```sql
-- Di skema unit yang bersangkutan:
SELECT event_type, published_at, attempts, last_error
FROM outbox
WHERE aggregate_id = '<user_id>'
ORDER BY created_at DESC;
```

Setelah penyebabnya diperbaiki, permintaan penghapusannya akan **dikirim ulang
sendiri** bila offsetnya masih tertahan. Kalau offsetnya sudah terlanjur maju —
lihat catatan di bawah — permintaan itu tidak akan datang lagi, dan jalan
keluarnya adalah meminta penghapusan ulang lewat API.

> **Catatan sejarah.** Versi pertama konsumen ini hanya "tidak mengomit" saat
> gagal. Itu tidak cukup: franz-go tidak mengirim ulang apa pun di dalam sesi
> yang sama, jadi batch berikutnya berhasil dan mengomit **seluruh** yang sudah
> dikonsumsi — termasuk record yang gagal. Sebuah permintaan penghapusan pernah
> hilang persis begitu, terlewati oleh lima konfirmasi yang lewat di topic yang
> sama. Konsumen sekarang **memundurkan** posisinya ke record yang gagal.

## Membuktikan sisa data nol

```sh
# Kata sandi tiap skema dibaca dari SVC_<SKEMA>_PASSWORD, konvensi yang sama
# dengan seluruh platform. Tanpa nilai bawaan (ADR-016): verifikasi yang jatuh
# ke kata sandi tebakan hanya bisa gagal menyambung, dan kegagalan itu akan
# terbaca sebagai "tidak ada sisa data".
set -a; . ./.env; set +a

go run ./cmd/deletion-verify \
  -user-id    <uuid> \
  -profile-id <uuid> \
  -dsn 'postgres://svc_{schema}:{password}@127.0.0.1:15432/selaras?sslmode=disable'
```

Ia menanyai **empat belas tabel di tujuh skema**, masing-masing dengan peran
login-nya sendiri. Menanyainya sebagai superuser akan menemukan baris yang tidak
bisa dilihat service-nya sendiri, dan itu menjawab pertanyaan yang tidak sedang
diajukan.

Keluaran saat bersih:

```
  assessment   risk_assessments             0  clean
  ...
  profile      user_profiles                0  clean
no rows left anywhere
```

**Kode keluar:** 0 bila bersih, 1 bila ada sisa, 2 bila verifikasinya sendiri
gagal berjalan. Ketiganya dibedakan supaya "tidak bisa memeriksa" tidak pernah
terbaca sebagai "tidak ada masalah".

## Batas yang perlu diketahui

- **Penghapusan tidak bisa dibatalkan.** Tidak ada undo, dan tidak ada masa
  tenggang. Kalau masa tenggang dibutuhkan, tempatnya di lapisan atas — sebelum
  saga dimulai — bukan di dalamnya.
- **Akun yang masuk lewat penyedia sosial belum bisa menghapus akunnya lewat
  jalur ini.** Ia tidak punya kata sandi untuk dibandingkan, dan menerima
  permintaan tanpa bukti apa pun justru mengembalikan lubang yang ditutup S2.
  Jalur konfirmasi untuk akun sosial adalah pekerjaan tersendiri.
- **Data di luar Postgres tidak ikut.** Cache Redis milik gateway berumur
  pendek dan hilang sendiri; berkas terlampir belum ada di platform ini.
