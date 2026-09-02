# ADR-005 — Transport: gRPC internal, REST di edge, ID publik berupa string


**Konteks.** Frontend yang ada memanggil REST `/api/v1` dan tidak boleh pecah. Antar service,
kontrak yang diperiksa compiler jauh lebih aman. E16 dan tipe `uuid` pada `coaching_tasks.id`
memunculkan ID yang tidak seragam.

**Opsi.** (a) gRPC internal + REST edge · (b) REST di semua lapis · (c) GraphQL di edge.

**Keputusan.** (a). Seluruh ID yang keluar lewat API publik bertipe **string**, tanpa
mengekspos apakah di baliknya bigint, uuid, atau slug.

**Konsekuensi.** Positif: kontrak internal dikunci protobuf dan dicek `buf breaking`; frontend
tidak perlu berubah; heterogenitas ID tersembunyi di balik kontrak. Negatif: dua bentuk kontrak
yang harus dijaga konsisten; debugging gRPC lebih repot daripada `curl`; ada biaya belajar
toolchain `buf`.

**Pembatal.** Kalau ternyata hanya ada satu atau dua panggilan antar service, gRPC adalah
upacara tanpa manfaat dan REST internal lebih jujur.

---

