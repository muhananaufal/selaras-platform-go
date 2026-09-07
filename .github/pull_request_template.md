## Apa yang berubah dan mengapa

<!-- Masalahnya dulu, baru solusinya. Tautkan temuan (B-nomor) atau ADR bila ada. -->

## Bukti

- [ ] Test yang membuktikan perubahan ini pernah **merah** sebelum diperbaiki (bugfix), atau mutasi yang membuktikan test-nya menggigit (fitur)
- [ ] `task lint` bersih; `go vet ./...` bersih
- [ ] Suite integrasi dijalankan dengan unit aplikasi mati (`task down:apps` lalu `task test:integration`)
- [ ] Bila menyentuh `api/proto` atau `api/openapi`: `buf breaking` / `vacuum` lulus, dan kliennya disebut
- [ ] Bila menyentuh skema: migrasi `up` dan `down` keduanya dicoba

## Keputusan

<!-- Perubahan yang menyentuh arsitektur membawa ADR baru atau mengubah pembatal ADR yang ada. Sebutkan nomornya, atau tulis "tidak ada keputusan arsitektural". -->

## Yang sengaja tidak dilakukan

<!-- Scope yang dilihat tetapi ditinggalkan, dan mengapa. Kosong berarti tidak ada. -->
