# Chaos F9-12 — broker dimatikan paksa saat pekerjaan berjalan

Skrip: `test/chaos/broker.sh`. Dijalankan 2026-09-07 terhadap stack compose
lokal. Kafka dimatikan dengan `docker kill` (SIGKILL, bukan shutdown rapi):
yang diuji adalah broker yang hilang mendadak, bukan yang pamit.

## Hasil

```
04:08:26  menyiapkan 8 akun dengan profil dan penilaian
04:08:28  outbox belum terkirim sebelum gangguan: 10
04:08:28  MEMATIKAN broker (docker kill selaras-kafka)
04:08:28  mengantre 8 personalisasi SAAT broker mati
04:08:28  diterima 202: 8 dari 8 (gateway dan service tidak bergantung pada broker untuk menerima)
04:08:34  outbox menahan 18 event selama broker mati
04:08:34  MENYALAKAN broker kembali
04:08:52  outbox kosong kembali 18 detik setelah broker dinyalakan
04:08:54  personalisasi selesai: 8 dari 8
04:08:55  job LLM gagal/mati sejak gangguan: 0
04:08:55  LULUS: nol event hilang, seluruh 8 pekerjaan selesai setelah broker pulih
```

## Yang dibuktikan

1. **Menerima permintaan tidak bergantung pada broker.** Delapan permintaan
   personalisasi dijawab 202 saat Kafka sudah mati. Service menulis penilaian
   dan event-nya dalam satu transaksi Postgres (ADR-004); broker tidak ada di
   jalur permintaan.
2. **Outbox menahan, bukan membuang.** Selama broker mati, 18 baris
   `published_at IS NULL` menumpuk (8 permintaan personalisasi + event
   `assessment.completed` dan `profile.updated` dari persiapan yang belum
   sempat terkirim). Relay mencoba, gagal, mencatat `attempts` dan
   `last_error`, dan mencoba lagi pada interval berikutnya.
3. **Pemulihan otomatis dan lengkap.** 18 detik setelah `docker start` —
   sebagian besar adalah waktu KRaft menyala dan `healthcheck` — outbox kosong
   kembali dan 8/8 personalisasi selesai. Tidak ada satu pun job LLM yang
   berakhir `failed` atau `dead`.
4. **Kriteria selesai #7** ("broker dimatikan paksa saat job berjalan tanpa
   ada event hilang, dibuktikan lewat outbox") terpenuhi dengan bukti
   dua angka: 18 tertahan, 0 tersisa.

## Yang terlihat, dan patut dicatat

- **Konsumen tidak perlu dinyalakan ulang.** franz-go menyambung ulang
  sendiri; keenam konsumen (dan relay) melanjutkan dari offset yang sudah
  dikomit. Tidak ada satu pun proses yang restart selama skenario ini.
- **Ada 10 baris belum terkirim SEBELUM gangguan.** Itu bukan sisa; relay
  berjalan setiap detik dan persiapan 8 akun menghasilkan puluhan event dalam
  dua detik. Angka "sebelum" dicatat supaya "18 saat mati" bisa dibaca
  relatif terhadapnya, bukan sebagai angka absolut.
- **Pesan yang sudah diterima broker sebelum `kill` tidak hilang** karena
  broker menulis ke disk sebelum mengakui (`acks=all` pada satu node berarti
  satu penulisan). Skenario ini tidak menguji kehilangan disk; itu domain
  backup (F9-30/31), bukan chaos broker.
- **Yang TIDAK tertangkap skenario ini, dan ditemukan di k3d (F9-04):** image
  `apache/kafka` menulis log ke `/tmp` di dalam container kecuali
  `KAFKA_LOG_DIRS` disetel. `docker kill` + `docker start` memakai container
  yang sama, jadi datanya bertahan - tetapi `docker compose up
  --force-recreate`, atau pod yang dibuat ulang, membuang SELURUH topic
  beserta isinya, dan volume `kafka-data` yang dipasang ternyata kosong
  sejak awal. Diperbaiki di compose dan k8s dengan `KAFKA_LOG_DIRS`. Outbox
  tetap sumber kebenarannya, tetapi hasil yang sudah terbit dan belum
  dikonsumsi akan hilang dalam jendela itu.

## Cara mengulang

```
JOBS=8 bash test/chaos/broker.sh
```

`JOBS` mengatur berapa personalisasi diantre saat broker mati. Skrip menyerah
setelah 180 detik bila outbox tidak kosong kembali, dan menyalakan broker
kembali pada setiap jalur keluar.
