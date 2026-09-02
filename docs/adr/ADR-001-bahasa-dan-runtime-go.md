# ADR-001 — Bahasa dan runtime: Go


**Konteks.** Tujuan proyek adalah bukti kompetensi. Sistem saat ini PHP/Laravel (E3).

**Opsi.** (a) Go · (b) tetap PHP dengan stack industri di sekelilingnya · (c) Rust · (d) Java/Kotlin.

**Keputusan.** Go.

**Konsekuensi.** Positif: satu binary statis yang ramah container; concurrency native untuk
worker; toolchain lengkap tanpa ketergantungan pihak ketiga. Negatif: seluruh 8.465 LOC ditulis
ulang; ekosistem AI/LLM di Go lebih tipis dibanding Python/Node `[memori-model]`; sebagian
kenyamanan Laravel (Eloquent, Socialite, resource) harus dirakit sendiri.

**Pembatal.** Kalau tujuannya bergeser jadi mengejar produk cepat jadi, opsi (b) menang telak —
biayanya jauh lebih kecil dan langsung memperbaiki E1, E2, dan E4.

---

