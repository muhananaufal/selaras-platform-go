# ADR-027 — Kontrak publik lewat Connect, bukan REST

**Status.** Diterima (2026-09-25). Mengoreksi ADR-005 untuk lapisan tepi;
bagian internalnya (gRPC antar service, ID publik berupa string) tetap.

---

**Konteks.** ADR-005 mempertahankan REST `/api/v1` karena frontend Laravel
sudah memanggilnya dan tidak boleh pecah. Premis itu gugur: frontend ditulis
ulang dari nol dan belum ada satu baris pun. Yang tersisa dari REST hanyalah
biayanya: dua bahasa kontrak (proto di dalam, OpenAPI tulis tangan di tepi)
yang dijaga tangan, hasil LLM yang harus di-poll klien, dan pemetaan string
Indonesia ke enum yang ditulis ulang di setiap handler.

**Opsi yang ditimbang.**

| Opsi | Kelebihan | Kekurangan |
| :--- | :--- | :--- |
| A. REST tetap | Paling akrab; cache HTTP penuh; curl | Kontrak ganda; SSE tulis tangan untuk streaming; klien bertipe harus di-generate dari OpenAPI yang bisa menyimpang dari kode |
| B. gRPC / gRPC-Web | Satu kontrak proto | Browser butuh proxy; semua POST, nol cache HTTP; tidak curl-able; pihak ketiga tetap butuh JSON terpisah |
| C. GraphQL | Klien memilih field | Bahasa kontrak ketiga; batas kedalaman/biaya query jadi permukaan serangan baru; berlebihan untuk 39 operasi sederhana |
| **D. Connect (connect-go)** | Satu handler melayani protokol Connect (JSON/biner lewat HTTP/1.1 atau HTTP/2), gRPC, dan gRPC-Web; browser memanggil dengan `fetch` tanpa proxy; GET untuk operasi tanpa efek samping; server-stream untuk hasil LLM | Ekosistem jauh lebih kecil (pkg.go.dev: 5.924 pengimpor, grpc-go 270.134, gin 183.243); URL berupa nama fungsi, bukan sumber daya; bidirectional butuh HTTP/2 |

Pola "proto sebagai kontrak, JSON/HTTP sebagai permukaan" adalah pola Google
APIs (transcoding, docs.cloud.google.com/apis/design); Connect menjalankannya
tanpa lapisan transcoding terpisah.

**Keputusan.** Opsi D.

1. **Paket `edge.v1` milik tepi (pola BFF).** Tujuh service - Auth, Profile,
   Assessment, Coaching, Chat, Nutrition, Dashboard - dengan pesan yang
   dibentuk untuk klien: tanpa id internal, tanpa `user_id` (pemanggil selalu
   pengguna di token, ADR-023). Kosakata enum dan kuesioner risiko memakai
   ulang paket internal, karena itu kata domain dan sudah dijaga `buf breaking`.
2. **JSON.** Opsi bawaan protojson untuk tulis: lowerCamelCase, enum dengan
   namanya, field kosong dihilangkan (connect-go `codec.go`). Untuk baca,
   codec KETAT (`internal/edge/wire`): connect-go memakai `DiscardUnknown`,
   dan di protojson opsi itu juga mengubah **nama enum yang tidak dikenal**
   menjadi nol (`decode.go` `unmarshalEnum`). Untuk `craving_type`, nol
   berarti "tidak ingin apa-apa" - salah ketik akan dijawab sebagai permintaan
   lain. Field dan nama enum tak dikenal ditolak; angka enum yang tak
   terdefinisi ditolak `EnumGuard`.
3. **Galat.** Kosakata kode Connect (sama dengan gRPC). Pelanggaran per field
   lewat detail `google.rpc.BadRequest`; batas laju `resource_exhausted`
   (HTTP 429) dengan `google.rpc.RetryInfo` dan `Retry-After`. Pesan bergaya
   gRPC: huruf kecil, tanpa titik.
4. **GET dan cache.** Semua RPC baca `NO_SIDE_EFFECTS`, jadi boleh GET. Semua
   jawaban `Cache-Control: no-store` - isinya data satu pengguna.
5. **Streaming.** `Watch*` per sumber daya asinkron: server membaca service
   pemilik tiap 2 detik dan mengirim perubahan sampai status akhir, maksimal
   5 menit per stream (`service.DefaultWatch`). Tanpa status di antara replika,
   jadi skala horizontal tetap bebas. Fan-out dari `llm.results` adalah
   Gelombang 2, saat jumlah stream membuat pembacaan itu mahal.
6. **Tepi yang tetap HTTP polos.** `/healthz`, `/readyz`, serta redirect dan
   callback OAuth di `/auth/{provider}/...` - yang membukanya browser, bukan
   klien RPC. Pertukaran sesinya `Auth/ExchangeSocialSession`.
7. **Autentikasi default-tolak.** Interceptor memegang daftar prosedur
   PUBLIK; yang tidak terdaftar terlindungi. Pencabutan tetap gagal-tertutup
   (ADR-020).
8. **Observabilitas.** otelhttp, bukan otelconnect: alert SLO membaca
   `http_server_request_duration_seconds` dengan `http_route`, dan setiap
   prosedur didaftarkan sebagai pola ServeMux sendiri sehingga `http_route`
   berisi path prosedur.
9. **OpenAPI di-generate** dari proto (`buf.gen.openapi.yaml`) untuk pihak
   ketiga dan pemindai; CI menggagalkan kode hasil generate yang basi.

**Yang ditemukan saat menerapkannya.**

- gin memercayai `X-Forwarded-For` dari alamat mana pun secara bawaan, dan
  gateway REST tidak pernah memanggil `SetTrustedProxies`: batas lima login
  per menit per IP bisa diakali dengan header palsu (dibuktikan merah).
  Sekarang header dibaca hanya dari CIDR `TRUSTED_PROXY_CIDRS`.
- `GetGraduationReport` memicu laporan LLM baru setiap kali laporan
  sebelumnya gagal, dan rute REST-nya tanpa batas laju. Kini dibatasi.
- chat-svc memberi halaman pesan dari yang TERLAMA; stream percakapan
  mengikuti halaman terakhir, bukan halaman pertama.

**Yang sengaja tidak dilakukan.** protovalidate: validasi tetap Go eksplisit
di `internal/edge/service`, karena protovalidate menambah dependensi BSR jarak
jauh dan runtime CEL untuk aturan yang sedikit. Dievaluasi ulang bila aturan
validasi tumbuh atau klien non-Go perlu membaca aturannya dari kontrak.

**Konsekuensi.** Positif: satu kontrak dari browser sampai service; klien
bertipe (e2e memakai klien hasil generate, jadi perubahan kontrak yang
merusak konsumen gagal saat kompilasi); streaming tanpa protokol tambahan.
Negatif: masalah aneh diselesaikan dengan membaca source, bukan forum;
dokumen dan skrip yang menyebut `/api/v1` harus diperbarui (reel showcase
masih tertunda); klien gRPC murni tidak terhitung di rasio 5xx karena gRPC
menjawab HTTP 200 untuk galat - frontend memakai protokol Connect, jadi alert
tetap bermakna untuk lalu lintas yang ada.

**Pembatal.**
- Frontend butuh komposisi lintas banyak service di satu layar secara masif →
  GraphQL dievaluasi sebagai lapisan DI ATAS Connect, bukan pengganti.
- API untuk pihak ketiga menjadi produk utama dan mereka menuntut URL sumber
  daya → vanguard-go (transcoding REST↔Connect) atau REST khusus publik.
- connect-go berhenti dirawat → kontrak tetap proto dan server yang sama sudah
  melayani gRPC standar; yang diganti hanya lapisan transport di tepi.
- Proto `edge.v1` identik satu-satu dengan proto internal selama dua rilis →
  lapisan BFF dihapus.
