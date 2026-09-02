# Foundation Gate — ringkasan untuk ditinjau

**Status:** menunggu persetujuan (task B1-10)
**Tanggal:** 2026-09-02

Kontrak di bawah ini dikunci setelah Anda menyetujuinya. Sesudah itu, perubahan yang merusak
konsumen harus datang sebagai versi paket baru — dan `buf breaking` di CI yang menegakkannya,
bukan niat baik.

## Yang paling mahal diubah belakangan

Dua hal ini merambat ke sembilan unit, jadi keduanya yang paling layak Anda pertanyakan sekarang:

1. **Tabel kepemilikan** — [RFC-000 bagian 2](rfc/RFC-000-platform-decomposition.md). Satu tabel,
   satu pemilik. Memindahkannya setelah migrasi ditulis jauh lebih mahal.
2. **Peta kopling** — [RFC-000 bagian 3](rfc/RFC-000-platform-decomposition.md). Lima kopling yang
   sudah diketahui dan cara masing-masing ditangani.

## Kontrak yang dikunci

| Berkas | Isi |
| :--- | :--- |
| `api/proto/common/v1` | Identitas, paginasi kursor, kode galat, kunci idempotensi |
| `api/proto/identity/v1` | Autentikasi; reset kata sandi dua langkah |
| `api/proto/profile/v1` | Demografi; `risk_region` sengaja tidak di sini |
| `api/proto/assessment/v1` | SCORE2 beserta seluruh jawaban proksi sebagai enum semantik |
| `api/proto/coaching/v1` | Program, minggu, tugas, diskusi |
| `api/proto/chat/v1` | Percakapan asisten umum |
| `api/proto/nutrition/v1` | Preferensi dan panduan menu |
| `api/proto/dashboard/v1` | Read-model |
| `api/proto/events/v1` | Amplop event bersama |
| `api/openapi/edge-v1.yaml` | 27 path, 34 operasi |

## Lima hal yang sengaja BERBEDA dari sistem lama

Semuanya perbaikan, dan semuanya bisa Anda batalkan sekarang — sesudah gerbang ini, biayanya naik.

| # | Perubahan | Alasan |
| :--- | :--- | :--- |
| 1 | Reset kata sandi jadi dua langkah | Endpoint lama publik dan langsung mengganti kata sandi milik email mana pun yang diketahui pemanggil (S1) |
| 2 | Operasi LLM menjawab `202` dengan `job_id` | Yang lama menahan koneksi, satu di antaranya dengan batas 300 detik di dalam request (E4) |
| 3 | Sumber daya orang lain menjawab `404`, bukan `403` | Membedakan keduanya membocorkan keberadaannya (S9) |
| 4 | Hapus akun memverifikasi kata sandi, menjawab `202` | Yang lama memintanya lalu tidak pernah memeriksanya (S2); penghapusan kini saga lintas service |
| 5 | `GET /risk-assessments/{slug}` baru; `GET /user` dibuang | Yang pertama dibutuhkan karena personalisasi kini asinkron; yang kedua punya anotasi OpenAPI tetapi tidak pernah terdaftar sebagai rute (B10) |

## Yang sudah dibuktikan, bukan diasumsikan

| Klaim | Bukti |
| :--- | :--- |
| Profil `core` muat di mesin ini | Idle 365 MB terhadap plafon 1728 MB; healthy dalam 13 detik |
| Isolasi schema ditegakkan database | `svc_identity` menulis di schema sendiri berhasil; menyentuh `coaching` dan `public` ditolak |
| Kafka berjalan tanpa ZooKeeper | Topic dibuat, dijelaskan, dan dihapus lewat KRaft |
| Deteksi breaking change bekerja | Field yang sengaja dihapus tertangkap `buf breaking` |
| Image berjalan | 15,8 MB distroless, probe menjawab 200, log JSON, nonroot, graceful shutdown |
| Gerbang kualitas hijau | `task ci`: fmt, vet, golangci-lint, buf lint, vacuum, test — seluruhnya lolos |

## Yang BELUM dikerjakan, dan disengaja

- **B2 belum dimulai.** Golden vector dan baseline k6 menunggu gerbang ini.
- **B2-10 wajib sebelum vektor dibuat.** Cacat `estimateSbp` sudah terbukti; vektor dilarang
  dibekukan di atasnya (ADR-013).
- **Belum ada logika domain.** Yang ada baru probe kesehatan, dan itu memang sengaja: kontrak
  lebih dulu.
