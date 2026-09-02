# ADR-004 — Konsistensi: transactional outbox + referensi lunak


**Konteks.** Begitu data dipecah, menulis ke database dan menerbitkan event menjadi dua sumber
kebenaran yang bisa menyimpang. E16 menunjukkan sudah ada FK lintas-domain yang tidak akan bisa
ditegakkan lagi setelah pemecahan.

**Opsi.** (a) transactional outbox · (b) two-phase commit · (c) publikasi langsung setelah commit
dan berharap tidak gagal · (d) Change Data Capture dengan Debezium.

**Keputusan.** Transactional outbox (a) `[fakta:E10]`. FK lintas-service diturunkan menjadi
**referensi lunak** + snapshot dari event.

**Konsekuensi.** Positif: state dan event selalu sinkron karena keduanya satu transaksi lokal;
tidak butuh koordinasi global; relay bisa gagal dan diulang tanpa kehilangan data. Negatif:
setiap service butuh tabel outbox dan proses relay; pengiriman bersifat at-least-once sehingga
konsumen **wajib** idempoten; ada jeda antara commit dan publikasi.

**Pembatal.** Kalau ternyata tidak ada satu pun alur yang benar-benar butuh jaminan itu, outbox
jadi kompleksitas tanpa lawan. Alur di master plan bagian 6 adalah pembenarannya — kalau alur
itu dibatalkan, ADR ini ditinjau ulang.

---

