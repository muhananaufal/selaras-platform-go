# Koleksi Postman

`selaras.postman_collection.json` adalah alur lengkap 32 endpoint gateway
yang bisa di-**Run** tanpa setup: `baseUrl` sudah menunjuk ke stack lokal,
surel dibuat unik per larian, token dan setiap slug/id ditangkap otomatis
dari jawaban sebelumnya, dan tiap request punya asersi status.

```bash
task up:full        # stack harus menyala
task postman:run    # Newman menjalankan koleksi yang sama dari CLI
```

Di Postman: Import → pilih berkas ini → Run collection. Urutan folder adalah
urutan alurnya (Auth → Profile → Assessment → Dashboard → Coaching → Chat →
Culinary → Account); jangan diacak - Coaching butuh slug analisis, Account
menghapus akunnya di akhir.

Koleksinya DIBANGUN, bukan ditulis tangan: `node build.js <output>`.
Body request meniru yang dipakai suite e2e (terbukti diterima gateway);
sumber kebenaran kontraknya tetap `api/openapi/edge-v1.yaml`.
