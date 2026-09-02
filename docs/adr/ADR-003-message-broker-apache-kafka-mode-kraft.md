# ADR-003 — Message broker: Apache Kafka mode KRaft


**Konteks.** E4 membuktikan panggilan Gemini berjalan sinkron di dalam request HTTP. Itu
justifikasi teknis nyata untuk pemrosesan asinkron — bukan penambahan demi terlihat modern.

**Catatan koreksi.** Versi pertama ADR ini memilih **Redpanda**. Itu keliru sebagai catatan
keputusan: opsi yang disodorkan ke Anda tertulis "Kafka (Redpanda)" sebagai satu pilihan, dan
implementasinya saya putuskan sendiri tanpa menanyakannya. Yang Anda pilih adalah **Kafka**.
Riwayat ini disimpan supaya jelas bahwa perubahannya adalah koreksi terjemahan, bukan pembalikan
keputusan.

**Opsi.** (a) **Apache Kafka mode KRaft** · (b) Redpanda · (c) NATS JetStream ·
(d) Asynq di atas Redis.

**Keputusan.** (a), Apache Kafka 4.x berjalan dalam mode KRaft.

**Konsekuensi.** Positif: ini implementasi rujukan, jadi tidak ada celah kompatibilitas yang
perlu diwaspadai — empat batasan terdokumentasi yang melekat pada Redpanda (satu mekanisme
SASL/SCRAM per user, HTTP Proxy tanpa CRUD topic/ACL, quota `request_percentage` tidak didukung,
KIP-890 belum diimplementasi) `[fakta:E9]` **tidak berlaku di sini**; sejak 4.0 ia berjalan
sepenuhnya tanpa ZooKeeper dengan KRaft sebagai mode bawaan, sehingga tidak ada ensemble
terpisah yang harus dipelihara di mesin lokal `[fakta:E20]`; KEDA punya scaler Apache Kafka
resmi untuk autoscaling berbasis lag `[fakta:E19]`. Negatif: berjalan di atas JVM, sehingga
jejak memori dan waktu start lebih besar daripada opsi (b) `[memori-model — diukur di B0-06]`;
tuning JVM menjadi tanggung jawab baru yang tidak ada pada opsi lain; untuk beban proyek ini
kapasitasnya tetap jauh melampaui kebutuhan.

**Tangga penurunan bila B0-06 menunjukkan mesin tidak sanggup.** Dua tingkat, dan tingkat
pertama nyaris gratis:

| Tingkat | Turun ke | Ongkos perubahan kode |
| :---: | :--- | :--- |
| 1 | **Redpanda** | Nihil — protokol Kafka yang sama, klien Go yang sama `[fakta:E9]` |
| 2 | **NATS JetStream** | Lapisan pesan ditulis ulang. Autoscaling tetap aman: KEDA punya scaler NATS JetStream tersendiri `[fakta:E19]` |

**Pembatal.** B0-06. Bila Kafka tidak bisa dijalankan nyaman bersama sembilan unit lain di mesin
Anda, turun satu tingkat — bukan langsung ke dasar. Keputusan itu diambil dari angka RAM yang
diukur, bukan dari firasat.

---

