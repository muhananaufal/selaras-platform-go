// Ambang SLO yang membuat k6 GAGAL (F9-11).
//
// Yang ada di sini SEKARANG hanya tingkat kegagalan. Ambang latensi belum
// ditulis, dengan sengaja: B2-08 menetapkan SLO dari baseline Laravel yang
// belum pernah diukur, dan mengarang angka p95 yang "terdengar wajar" adalah
// hal yang dilarang rencana ini. Angkanya diisi setelah pengukuran pertama
// terhadap sistem Go (docs/performance-report.md), dan dinyatakan di sana
// sebagai turunan dari pengukuran itu - bukan dari Laravel.

// Tingkat kegagalan HTTP. Satu persen adalah kebijakan, bukan pengukuran:
// pada beban uji tanpa gangguan, permintaan yang gagal adalah cacat, dan
// satu persen memberi ruang hanya untuk kegagalan akun uji yang bertabrakan.
const failureRate = ["rate<0.01"];

export const slo = {
  read: {
    http_req_failed: failureRate,
    checks: ["rate>0.99"],
  },
  write: {
    http_req_failed: failureRate,
    checks: ["rate>0.99"],
  },
  mixed: {
    http_req_failed: failureRate,
    checks: ["rate>0.99"],
  },
};
