# ADR-018 — Lingkungan kerja: Windows untuk kode, WSL untuk Docker


**Konteks.** Toolchain mesin ini terbelah dan tidak ada sisi yang lengkap `[fakta:E26]`: Go dan
PHP hidup di Windows, Docker hidup di WSL, dan daemon WSL semula tidak terjangkau dari Windows.
Tanpa jembatan, `testcontainers-go` mustahil — dan itu menggugurkan **setiap** task bertanda 🔴
yang menuntut "Postgres sungguhan, bukan mock".

**Opsi.** (a) pindahkan seluruh pengembangan ke WSL · (b) pasang Docker Desktop di Windows ·
(c) **Windows tetap jadi tempat menulis kode, daemon WSL dijembatani ke Windows**.

**Keputusan.** (c) — pilihan Anda, dengan konsekuensinya dinyatakan terbuka.

**Cara menjembatani, dan kenapa bukan cara yang lebih umum.** Jalan yang biasa ditempuh adalah
menambah `-H tcp://…` pada `ExecStart` dockerd lewat systemd drop-in. Itu menuntut `sudo` dan
**me-restart daemon**, yang berarti menghentikan `caddy-proxy`, `dbgate-ui`, dan `mysql-db` milik
proyek lain. Yang dipakai justru satu container proxy:

```
docker run -d --name docker-tcp-proxy --restart=unless-stopped \
  -p 127.0.0.1:2375:2375 -v /var/run/docker.sock:/var/run/docker.sock \
  alpine/socat tcp-listen:2375,fork,reuseaddr unix-connect:/var/run/docker.sock
```

Nol `sudo`, nol restart daemon, nol berkas di `/etc`, dan **nol gangguan pada container proyek
lain** — terbukti: ketiganya tetap menunjukkan uptime yang sama setelah proxy dipasang.
Membatalkannya cukup `docker rm -f docker-tcp-proxy`.

**Aturan keamanan yang mengikat.** Port **wajib** terikat ke `127.0.0.1`, tidak pernah `0.0.0.0`.
Endpoint Docker tanpa TLS setara akses root: siapa pun yang menjangkaunya bisa menjalankan
container yang me-mount seluruh filesystem. Mengikatnya ke loopback adalah satu-satunya hal yang
menahan itu, dan ia tidak boleh dilonggarkan "sementara" untuk alasan apa pun.

**Konsekuensi.** Positif: menulis kode tetap di Windows sesuai preferensi; `DOCKER_HOST` membuat
`testcontainers-go` menemukan daemon tanpa konfigurasi tambahan `[fakta:E28]`; proyek lain tidak
tersentuh. Negatif: endpoint tanpa TLS adalah permukaan serang yang sebelumnya tidak ada;
`DOCKER_HOST` menjadi state tersembunyi yang membingungkan bila lupa; **bind mount lintas
Windows-Linux menuntut path gaya Linux**, sehingga compose dan testcontainers perlu perhatian
khusus soal path dan performa `/mnt/c`.

**Pembatal.** Bila bind mount lintas-batas atau performa `/mnt/c` menjadi hambatan nyata, opsi
(a) — pindah ke WSL sepenuhnya — kembali jadi pilihan yang lebih murah daripada terus menambal.


---

