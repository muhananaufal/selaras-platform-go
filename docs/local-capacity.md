# B0-06 — Uji kapasitas lokal

Dijalankan 2026-09-02 pada mesin pengembangan: Windows, 15,7 GB RAM fisik,
4 processor logis, Docker daemon di dalam WSL.

## Plafon

| | |
| :--- | :--- |
| RAM fisik | 15,7 GB |
| Plafon WSL (`.wslconfig`) | **8 GB** + swap 4 GB — sudah aktif, terlihat sebagai 7,761 GiB |
| Processor logis | 4, seluruhnya terlihat oleh WSL |
| Sudah terpakai container proyek lain | ~690 MB (caddy, dbgate, mysql, postgres, docker-tcp-proxy) |

## Hasil profil `core`

Diukur saat idle, setelah seluruh service melaporkan `healthy`.

| Service | Terpakai | Batas | Rasio |
| :--- | ---: | ---: | ---: |
| postgres | 40 MB | 512 MB | 8% |
| kafka | 315 MB | 1024 MB | 31% |
| redis | 10 MB | 192 MB | 5% |
| **Total aktual** | **~365 MB** | 1728 MB | 21% |

**Waktu dari `task up` sampai tiga service `healthy`: 13 detik.**

## Verifikasi fungsional

| Uji | Hasil |
| :--- | :--- |
| Isolasi schema — `svc_identity` menulis di schema sendiri | ✅ `CREATE TABLE` / `DROP TABLE` berhasil |
| Isolasi schema — `svc_identity` menyentuh `coaching` | ✅ **ditolak** |
| Isolasi schema — `svc_identity` menulis di `public` | ✅ **ditolak** |
| Delapan schema + delapan role terbentuk dari `initdb` | ✅ |
| Kafka KRaft — create, describe, delete topic | ✅ tanpa ZooKeeper |
| Redis | ✅ `PONG` |

## Keputusan yang dihasilkan

**Pembatal ADR-003 TIDAK aktif. Broker tetap Apache Kafka.**

Kafka memakai 315 MB idle dengan heap ditahan di 512 MB — jauh di bawah kekhawatiran awal
saat plafon masih 3 GB. Setelah dinaikkan ke 8 GB, ruang tersisa sekitar 6,7 GB untuk sembilan
unit Go dan lapisan observability. Tidak ada alasan turun ke Redpanda atau NATS.

**Batas berikutnya kemungkinan CPU, bukan RAM.** Mesin hanya punya 4 processor logis dan Kafka
JVM akan berbagi dengan sembilan unit Go. Diukur ulang di F9 saat seluruh sistem menyala di
bawah beban k6.

## Dua cacat yang ditemukan justru karena menjalankannya

| # | Temuan | Perbaikan |
| :--- | :--- | :--- |
| 1 | **PostgreSQL 18 mengubah konvensi mount.** Data harus berada di `/var/lib/postgresql`, bukan `/var/lib/postgresql/data`; container keluar dengan exit 1 dan pesan yang menyebut "unused mount/volume" | Volume dipindah ke `/var/lib/postgresql` |
| 2 | **Kredensial per-service tidak diteruskan ke container.** `initdb` membacanya dari environment, sementara compose hanya meneruskan `POSTGRES_*` | Delapan variabel `SVC_*_PASSWORD` ditambahkan, seluruhnya wajib tanpa nilai bawaan |

Keduanya tidak akan terlihat dari membaca dokumentasi. Inilah alasan B0-06 ada.
