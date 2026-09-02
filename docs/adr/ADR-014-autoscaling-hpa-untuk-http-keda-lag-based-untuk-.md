# ADR-014 — Autoscaling: HPA untuk HTTP, KEDA lag-based untuk worker


**Konteks.** Rencana semula tidak memuat autoscaling sama sekali. Tiga prasyaratnya sudah ada
tanpa disengaja: F9-03 menetapkan resource request (tanpanya HPA tidak bisa menghitung apa pun),
F3-15 mengekspos lag konsumen, dan F9-11 menjalankan k6 terhadap SLO. Beban sistem ini juga
punya dua bentuk yang berbeda: request HTTP yang pendek, dan job LLM yang panjang serta menumpuk
saat burst (E4).

**Opsi.** (a) tanpa autoscaling · (b) HPA berbasis CPU/memori pada service HTTP ·
(c) (b) ditambah KEDA berbasis consumer lag pada `llm-worker`, dengan scale-to-zero saat idle ·
(d) (c) ditambah cluster autoscaler.

**Keputusan.** (c). Opsi (d) ditunda sampai ada klaster cloud sungguhan — di k3d ia tidak punya
arti karena nodenya satu.

**Alasan (b) sendirian ditolak.** CPU adalah proksi yang buruk untuk pekerjaan yang didominasi
menunggu jawaban Gemini. Worker yang tertinggal 500 job bisa punya CPU rendah dan tetap tidak
di-scale, sementara antreannya terus tumbuh. Sinyal yang benar untuk konsumen antrean adalah
**lag**, bukan CPU.

**Aturan yang mengikat.**

1. **`maxReplicaCount` untuk `llm-worker` tidak boleh melebihi jumlah partisi topic-nya.**
   Konsumen melebihi partisi akan menganggur — scaling yang terlihat bekerja padahal tidak
   menambah paralelisme apa pun. Karena itu jumlah partisi wajib dinyatakan di task F3-01,
   sebelum autoscaling dipasang.
2. **Ambang HPA diturunkan dari SLO** yang ditetapkan di B2-08, bukan dari angka bulat yang
   terdengar wajar.
3. **Autoscaling hanya sah bila dibuktikan di bawah beban.** `kubectl get hpa` bukan bukti;
   yang jadi bukti adalah k6 menekan sistem sampai ambang terlewati, dan jumlah replica terekam
   naik lalu turun kembali (F9-24).
4. **Batas k3d dinyatakan terbuka.** Pada satu node, replica tambahan berbagi CPU host yang sama,
   sehingga yang terbukti adalah **mekanismenya bekerja**, bukan kapasitasnya bertambah.
   Menyajikan grafik replica naik tanpa menyebut ini adalah klaim yang menyesatkan (F9-25).
5. **Scale-to-zero wajib melaporkan cold-start terukur.** Worker yang tidur menghemat sumber daya
   tetapi menambah latensi job pertama; angkanya diukur, bukan diabaikan.

**Konsekuensi.** Positif: sinyal scaling cocok dengan bentuk bebannya; scale-to-zero menurunkan
beban idle di mesin lokal dan menyambung langsung ke perhitungan FinOps (F9-16); lag-based
scaling adalah bagian yang paling sulit dipalsukan karena menuntut metrik, beban, dan bukti.
Negatif: KEDA menambah komponen di klaster sehingga memperberat R4; ada operator baru yang harus
dipahami dan dipelihara; scale-to-zero memperkenalkan cold-start yang sebelumnya tidak ada.

**Pembatal.** Bila B0-06 menunjukkan mesin Anda tidak sanggup menjalankan metrics-server dan KEDA
bersama sembilan unit lain, keputusan ini turun ke opsi (b) dan KEDA pindah ke fase cloud.

---

