# Runbook — llm-worker

Untuk orang yang dipanggil saat personalisasi berhenti bekerja. Ia menjelaskan
apa yang harus dilihat, dalam urutan apa, dan apa yang **tidak** boleh
dilakukan.

## Yang dijamin sistem ini, dan yang tidak

| Dijamin | Tidak dijamin |
| :--- | :--- |
| Event yang ditulis bersama perubahan bisnisnya tidak akan hilang | Event tidak akan datang dua kali |
| Pekerjaan dengan kunci idempotensi sama menghasilkan **satu** hasil | Pekerjaan selesai dalam waktu tertentu |
| Pekerjaan yang gagal tiga kali masuk `llm.dlq` | Pekerjaan yang gagal akan berhasil kalau diulang |
| Urutan event per agregat terjaga | Urutan global antar agregat |

Pengirimannya **at-least-once**, dan itu pilihan yang disengaja: menandai baris
outbox terkirim sebelum broker mengakuinya akan kehilangan event selamanya.
Duplikat ditahan penjaga idempotensi di sisi penerima.

## Gejala 1 — status berhenti di `pending`

Klien melihat `"personalization_status":"pending"` dan tidak pernah berubah.

**Periksa berurutan, jangan melompat:**

```sql
-- 1. Apakah eventnya keluar dari assessment-svc?
SELECT id, created_at, event_type, published_at, attempts, left(last_error, 200)
FROM assessment.outbox
WHERE aggregate_id = '<assessment id>'
ORDER BY created_at DESC;
```

- **Tidak ada baris** → permintaannya tidak pernah sampai. Lihat log
  `assessment-svc`; kalau `KAFKA_BROKERS` tidak diset, permintaan personalisasi
  **ditolak** dengan `Unimplemented`, bukan diterima diam-diam.
- **Ada, `published_at` NULL, `attempts` naik** → relay tidak bisa menerbitkan.
  Lihat `last_error`. Broker mati, topic tidak ada, atau kredensial salah.
- **Ada, `published_at` NULL, `attempts` = 0** → relay tidak berjalan sama sekali.
  Periksa apakah proses `assessment-svc` hidup dan lognya memuat
  `"outbox relay started"`. Relay yang berjalan tetapi tidak bisa menerbitkan
  SELALU menaikkan `attempts` dalam sepuluh detik (`PublishTimeout`), jadi
  attempts nol berarti tidak ada yang mencoba - bukan mencoba dan gagal.

```sql
-- 2. Apakah worker mengambilnya?
SELECT id, status, attempts, prompt_version, model, left(last_error, 300)
FROM llm.llm_jobs
WHERE aggregate_id = '<assessment id>'
ORDER BY created_at DESC;
```

- **Tidak ada baris** → pesannya belum sampai ke worker. Periksa consumer lag
  (Gejala 3).
- **`status = 'pending'` atau `'running'` dan tidak berubah** → worker mati di
  tengah pekerjaan. Ia akan dilanjutkan saat worker dinyalakan lagi: offsetnya
  belum dikomit dan klaim idempotensinya dilepas saat proses berhenti rapi.
  **Kalau prosesnya mati mendadak (SIGKILL, OOM), klaimnya TIDAK sempat
  dilepas** — lihat Gejala 4.
- **`status = 'completed'` tetapi penilaiannya masih `pending`** → hasilnya
  tidak sampai kembali. Periksa outbox llm-worker:

```sql
SELECT event_type, published_at, attempts, left(last_error, 200)
FROM llm.outbox
WHERE aggregate_id = '<assessment id>';
```

## Gejala 2 — pekerjaan masuk antrean surat mati

`status = 'dead'` di `llm.llm_jobs`, dan penilaiannya `failed`.

Itu **perilaku yang benar**, bukan kerusakan: pekerjaan sudah dicoba tiga kali
dan penyedianya tetap menolak. `last_error` menyebutkan alasannya.

Baca isinya dari topic:

```bash
docker exec selaras-kafka /opt/kafka/bin/kafka-console-consumer.sh \
  --bootstrap-server localhost:9092 \
  --topic llm.dlq --from-beginning --max-messages 20 \
  --property print.key=true
```

Kuncinya adalah **id penilaian**; isinya `Envelope` protobuf berisi
`LlmJobFailed` dengan `job_id` dan `reason`.

### Memutarnya kembali

Hanya setelah **sebabnya diperbaiki** — kuota dinaikkan, model diganti, prompt
diperbaiki. Memutar ulang tanpa itu hanya mengulangi kegagalan yang sama tiga
kali lagi.

```sql
BEGIN;

-- 1. Lepas klaim idempotensinya. Tanpa ini, pengiriman berikutnya dilewati
--    sebagai duplikat dan tidak ada yang terjadi.
DELETE FROM llm.processed_messages
WHERE key = 'llm-worker' || chr(31) || '<idempotency_key dari llm_jobs>';

-- 2. Hapus baris pekerjaannya, supaya penghitung percobaannya mulai dari nol.
--    Tanpa ini ia langsung dianggap menyerah lagi pada percobaan pertama.
DELETE FROM llm.llm_jobs WHERE id = '<job id>';

-- 3. Kembalikan penilaiannya ke pending.
UPDATE assessment.risk_assessments
SET personalization_status = 'pending', personalization_error = NULL
WHERE id = '<assessment id>';

COMMIT;
```

Lalu minta ulang lewat API — `PATCH /api/v1/risk-assessments/{slug}/personalize`.
Permintaan itu yang menulis baris outbox baru.

> Pemisah `chr(31)` bukan salah ketik. Kunci idempotensi disimpan sebagai
> `<scope>\x1f<key>`; pemisah biasa seperti `:` akan membuat scope `a` + key
> `b:c` bertabrakan dengan scope `a:b` + key `c`.

## Gejala 3 — antrean menumpuk

```bash
docker exec selaras-kafka /opt/kafka/bin/kafka-consumer-groups.sh \
  --bootstrap-server localhost:9092 --describe --group llm-worker
```

`LAG` yang terus naik berarti pekerjaan datang lebih cepat daripada
diselesaikan.

- **Naik perlahan** → tambah replika worker. Batas atasnya **jumlah partisi
  `llm.jobs`, yaitu 12** ([`docs/topics.md`](../topics.md)). Replika ke-13 akan
  menganggur, bukan membantu.
- **Naik cepat dan `CURRENT-OFFSET` tidak bergerak sama sekali** → worker macet,
  bukan lambat. Lihat lognya. Satu pesan yang gagal berulang akan **menahan
  partisinya** sampai menyerah — itu disengaja, karena melewatinya akan
  mematahkan urutan per agregat.

## Gejala 4 — pekerjaan menggantung setelah worker mati mendadak

Proses yang dimatikan rapi (SIGTERM) melepas klaim pekerjaan yang sedang
berjalan. Proses yang dimatikan mendadak (SIGKILL, OOM killer) **tidak**.

Akibatnya: baris `llm_jobs` tertinggal di `pending`/`running`, klaimnya masih
ada, dan pengiriman ulang dilewati sebagai duplikat.

Cari yang menggantung:

```sql
SELECT id, aggregate_id, status, attempts, created_at, idempotency_key
FROM llm.llm_jobs
WHERE status IN ('pending', 'running')
  AND created_at < now() - interval '30 minutes'
ORDER BY created_at;
```

> Ambang 30 menit adalah tebakan yang harus diganti angka nyata begitu ada
> data durasi pekerjaan (F3-15). Jangan memperlakukannya sebagai batas yang
> berarti.

Pemulihannya sama dengan "memutarnya kembali" di Gejala 2.

## Yang DILARANG

- **Jangan menaikkan jumlah partisi topic yang sudah berisi.** Kafka memetakan
  kunci lewat hash modulo jumlah partisi; menambahnya mengalihkan kunci yang
  sudah berjalan dan mematahkan urutan per agregat **tanpa satu pun galat**.
- **Jangan mengosongkan `processed_messages` untuk "membersihkan".** Setiap
  pekerjaan yang pernah selesai akan dikerjakan lagi, dan yang berbayar
  dibayar dua kali. Pangkas berdasarkan umur (`Sweep`), bukan seluruhnya.
- **Jangan menghapus baris `outbox` yang `published_at` NULL.** Itu event yang
  belum terkirim, bukan sampah.
- **Jangan mengubah `LLM_PROVIDER` menjadi `fake` di lingkungan nyata.**
  Jawabannya dibuat lokal dan bukan analisis apa pun; pengguna tidak akan bisa
  membedakannya dari laporan sungguhan.
- **Jangan mengubah consumer group** (`llm-worker`, `assessment-results`).
  Group baru mulai dari awal topic dan mengerjakan ulang seluruh riwayatnya.

## Konfigurasi yang menentukan perilakunya

| Variabel | Wajib | Akibat kalau salah |
| :--- | :--- | :--- |
| `LLM_POSTGRES_DSN` | ya | Worker menolak start |
| `KAFKA_BROKERS` | ya | Worker menolak start; di assessment-svc, personalisasi ditolak |
| `LLM_PROVIDER` | ya | Tidak punya bawaan, dengan sengaja: `fake` yang tidak disadari menjawab pengguna dengan teks buatan, `gemini` yang tidak disadari menghabiskan kuota berbayar |
| `GEMINI_API_KEY` | ya bila `LLM_PROVIDER=gemini` | Worker menolak start |
| `GEMINI_MODEL` | tidak | Bawaan `gemini-2.5-flash-lite` |
| `LLM_TIMEOUT` | tidak | Detik, per percobaan. Bawaan 120 |
