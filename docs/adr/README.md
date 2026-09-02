# Architecture Decision Records

Setiap berkas memuat satu keputusan: konteks, opsi yang ditimbang, keputusan, konsekuensi dua
arah, dan **pembatal** — kondisi yang, bila benar, membuat keputusan itu gugur. Keputusan tanpa
pembatal adalah preferensi yang menyamar jadi analisis.

Bukti yang menopangnya ada di Evidence Ledger pada dokumen rencana di repo Laravel,
`docs/migration-plan/01-decisions-and-evidence.md`.

| ADR | Keputusan |
| :--- | :--- |
| 001 | Bahasa dan runtime: Go |
| 002 | Topologi: 9 unit, profile-svc berdiri sendiri |
| 003 | Message broker: Apache Kafka mode KRaft |
| 004 | Konsistensi: transactional outbox dan referensi lunak |
| 005 | Transport: gRPC internal, REST di edge, ID publik string |
| 006 | Database: PostgreSQL, schema per service |
| 007 | `user_profile_id` sebagai identitas global |
| 008 | Bukti paritas: golden vector oracle |
| 009 | Dashboard sebagai read-model |
| 010 | Deployment: k3d lokal dulu, cloud kemudian |
| 011 | Penghapusan akun sebagai saga |
| 012 | Bentuk token: JWT pendek dan daftar cabut |
| 013 | Kebijakan port: replikasi, perbaiki, atau buang |
| 014 | Autoscaling: HPA untuk HTTP, KEDA untuk worker |
| 015 | Pemilihan library: kriteria, bukan popularitas |
| 016 | Bangun seolah produksi, bukan seolah latihan |
| 017 | Meninjau tiga pilihan dengan data terukur |
| 018 | Lingkungan kerja: Windows untuk kode, WSL untuk Docker |
| 019 | Temuan baru saat porting: diperbaiki di tempat, dengan pagar |
