# Chaos F9-14 — penyedia LLM lambat, sesekali gagal, selalu gagal

Skrip: `test/chaos/llm.sh`. Dijalankan 2026-09-07 terhadap stack compose
lokal dengan penyedia `fake`; gangguan dipasang lewat `LLM_FAKE_FAULT`
(`slow=<durasi>`, `flaky=<n>`, `error`).

## Hasil

```
04:07:25  llm-worker dinyalakan ulang dengan LLM_FAKE_FAULT=flaky=2
04:07:36  flaky=2: personalisasi selesai setelah 4 detik; job: completed attempts=2 error=fake provider fault: call 2 of the first 2 fails
04:07:36  llm-worker dinyalakan ulang dengan LLM_FAKE_FAULT=error
04:07:47  error: penilaian ditandai failed setelah 4 detik; job: dead attempts=3 error=fake provider fault: every call fails
04:07:47    alasan yang tersimpan (kolom internal; kontrak publik hanya memuat personalization_status): fake provider fault: every call fails
04:07:47  llm-worker dinyalakan ulang dengan LLM_FAKE_FAULT=slow=15s
04:07:54  slow=15s: seluruh alur HTTP (daftar, profil, penilaian, personalisasi) dijawab dalam 295 ms
04:08:11  slow=15s: personalisasi selesai setelah 17 detik; job: completed attempts=0 error=-
04:08:18  llm-worker kembali tanpa gangguan
```

## Yang dibuktikan

| Gangguan | Perilaku yang diharapkan | Terjadi |
| :--- | :--- | :--- |
| `flaky=2` — dua panggilan pertama gagal | worker mencoba ulang (jeda 0,5 s lalu 1 s), berhasil pada percobaan ketiga; pengguna tidak melihat apa pun | ✅ `completed attempts=2`, selesai 4 detik setelah diminta |
| `error` — semua panggilan gagal | setelah `MaxAttempts` (3) job berakhir `dead`, event `LlmJobFailed` terbit, penilaian ditandai `failed` — **bukan pending selamanya** | ✅ `dead attempts=3`, `personalization_status = failed` dalam 4 detik |
| `slow=15s` — tiap jawaban ditahan 15 detik | permintaan HTTP tetap dijawab 202 seketika; hasil datang belakangan | ✅ seluruh alur HTTP 295 ms; hasil tiba 17 detik kemudian (15 s + dua putaran relay) |

Ketiganya berjalan **tanpa Gemini**: itulah gunanya mode gangguan pada
penyedia palsu. Yang diuji adalah jalur percobaan ulang, jalur mati, dan
pemisahan HTTP dari pekerjaan — bukan Gemini-nya.

## Yang terlihat, dan patut dicatat

- **`last_error` bertahan setelah berhasil.** Job `flaky=2` berakhir
  `completed` tetapi kolom `last_error` masih memuat galat percobaan kedua.
  Itu riwayat, bukan keadaan, dan kolomnya memang bernama *last* — tetapi
  siapa pun yang menyaring `last_error IS NOT NULL` untuk mencari job
  bermasalah akan mendapat job yang sehat. Dicatat, tidak diubah.
- **`job_id` yang dijawab API tidak ada di `llm_jobs`.** Ia id event
  permintaannya; worker memberi id sendiri pada barisnya. Keduanya bertemu di
  `idempotency_key` (`personalization:<assessment_id>`) dan `aggregate_id`.
  Klien tidak pernah membutuhkannya — kontrak publik menyuruh memantau
  `personalization_status` penilaiannya — tetapi operator yang memegang
  `job_id` dari log gateway harus tahu ia mencari lewat `aggregate_id`.
- **Alasan kegagalan tidak bocor ke pengguna.** `personalization_error`
  menyimpan galat mentah penyedia, dan kontrak publik (`edge-v1.yaml`) hanya
  memuat `personalization_status`. D9 (kegagalan AI dijawab ramah) terjaga di
  batas API, bukan di kolomnya.
- **`retryDelay` pendek dengan sengaja** (0,5 s, 1 s): penyedia Gemini sudah
  punya backoff-nya sendiri di dalam (`MaxAttempts: 3` di klien). Jeda di
  worker menangani yang lolos dari lapisan itu, dan menunggu lama hanya
  menahan partisi untuk semua orang.

## Cara mengulang

```
bash test/chaos/llm.sh
```

Skrip menyalakan ulang `llm-worker` dengan tiap gangguan, lalu
mengembalikannya tanpa gangguan di akhir. Ia menuntut `LLM_PROVIDER=fake`.
