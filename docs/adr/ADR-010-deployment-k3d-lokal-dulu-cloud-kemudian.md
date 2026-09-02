# ADR-010 — Deployment: k3d lokal dulu, cloud kemudian


**Konteks.** Anda memilih lokal dulu dengan niat masuk cloud nyata belakangan.

**Keputusan.** Seluruh manifest ditulis **cloud-ready sejak awal**: konfigurasi 12-factor lewat
environment, nol path absolut lokal, image didorong ke registry, rahasia lewat Secret dan bukan
ConfigMap, resource request dan limit dinyatakan sejak awal.

**Konsekuensi.** Positif: pindah ke cloud jadi mengganti nilai, bukan menulis ulang; biaya
selama pengembangan nol. Negatif: sebagian kerja terasa berlebihan untuk lingkungan lokal;
sejumlah masalah nyata cloud (jaringan, DNS, TLS, egress) tidak akan muncul sampai benar-benar
di-deploy.

**Pembatal.** Kalau ternyata cloud tidak jadi ditempuh sama sekali, sebagian kerja ini memang
mubazir — tapi harganya kecil dan tidak merusak apa pun.

---

