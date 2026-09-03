# Topic Kafka dan desain partisinya

Sumber kebenarannya adalah `internal/platform/kafka/topics.go`. Dokumen ini
menjelaskan **mengapa**; kode itu yang menentukan **apa**, dan `cmd/topics` yang
membuat serta membacanya kembali dari broker.

```
go run ./cmd/topics -brokers 127.0.0.1:19092
```

Perintah itu boleh dijalankan berkali-kali. Topic yang sudah ada dibiarkan.

## Mengapa jumlah partisi tidak boleh diubah sembarangan

Jumlah partisi adalah **batas atas paralelisme konsumen** (ADR-014 aturan 1).
Satu partisi hanya boleh dipegang satu konsumen dalam satu group, jadi group
dengan dua belas konsumen di topic tiga partisi menyisakan sembilan yang
menganggur — dan `maxReplicaCount` KEDA di F9-22 tidak boleh melebihi angka ini.

Menaikkannya kemudian **bukan operasi yang netral**. Kafka memetakan kunci ke
partisi lewat hash modulo jumlah partisi, jadi menambah partisi mengubah tujuan
kunci yang sudah berjalan: pesan untuk agregat yang sama tiba-tiba mendarat di
tempat lain, dan urutan yang selama ini terjaga patah di titik perubahan —
tanpa satu pun galat.

Karena itu `EnsureTopics` sengaja tidak pernah mengubah topic yang sudah ada.

## Daftarnya

| Topic | Partisi | Alasan |
| :--- | ---: | :--- |
| `profile.updated` | 3 | Konsumennya penulis cache (F2-16) — pekerjaan pendek, terikat basis data. Tiga cukup untuk memisahkan pengguna yang sibuk tanpa menyebar beban yang belum ada. |
| `assessment.completed` | 3 | Pemicu personalisasi. Lajunya terikat pada berapa banyak penilaian yang diselesaikan orang, bukan pada kecepatan mesin — dan itu angka yang kecil. |
| `llm.jobs` | 12 | Satu-satunya yang butuh paralelisme sungguhan: pekerjaannya menunggu jaringan selama puluhan detik, jadi jumlah partisi menentukan berapa banyak yang boleh menunggu bersamaan. Dua belas adalah plafon `maxReplicaCount` di F9-22. |
| `llm.results` | 12 | Dipasangkan dengan `llm.jobs` supaya satu worker bisa memegang partisi yang bersesuaian di keduanya. Jumlah yang berbeda membuat hasil sebuah job mendarat di partisi yang dipegang worker lain. |
| `llm.dlq` | 1 | Antrean surat mati (F3-13), dibaca manusia saat menyelidiki — bukan oleh armada konsumen. Satu partisi menjaga urutannya utuh, dan kalau ia sampai butuh lebih, yang salah bukan jumlah partisinya. |
| `user.deletion` | 1 | Penghapusan akun harus berurutan terhadap dirinya sendiri dan jarang terjadi. Paralelisme di sini hanya menambah cara untuk salah. |

## Kunci partisi

Kuncinya adalah `aggregate_id` dari baris outbox — id profil, id penilaian, atau
id pengguna. Itu yang menjamin **urutan per agregat**: Kafka tidak menjamin
urutan global, hanya urutan di dalam satu partisi. Tanpa kunci, "profil
diperbarui" bisa tiba setelah "profil dihapus".

## Dua listener, dan mengapa

Broker mengumumkan dua alamat:

| Listener | Diumumkan sebagai | Untuk |
| :--- | :--- | :--- |
| `PLAINTEXT` | `kafka:9092` | Service di dalam jaringan compose |
| `HOST` | `127.0.0.1:19092` | Test dan alat yang berjalan di host |

Kafka menjawab setiap sambungan dengan alamat yang harus dipakai klien
**selanjutnya**, bukan alamat yang barusan dipakai untuk menyambung. Dengan satu
listener yang mengumumkan `kafka:9092`, klien di host berhasil menyambung ke
port yang dipetakan lalu diarahkan ke nama yang tidak bisa ia resolusi — gagal
setelah terlihat berhasil.

## Metadata broker tidak langsung menyusul

`CreateTopic` yang berhasil tidak berarti topic itu sudah muncul di metadata.
Pembacaan yang dilakukan seketika setelahnya akan melaporkan topic yang baru
saja berhasil dibuat sebagai **tidak ada** — ini benar-benar terjadi saat topic
di atas pertama kali dibuat: empat dari enam "hilang" pada pembacaan pertama.

`WaitForTopics` menunggu sampai broker mengumumkannya, alih-alih memperlonggar
pemeriksaannya. Yang ingin dibuktikan tetap sama: topic itu ada, dengan jumlah
partisi yang diminta.

## Replikasi

`-replicas 1` adalah bawaan dan **hanya untuk pengembangan lokal** — satu broker
tidak bisa mereplikasi ke mana pun. Di lingkungan dengan lebih dari satu broker,
angka ini dinaikkan; `acks=all` di producer baru bermakna kalau ada replika yang
menyusul.
