// Ambang SLO yang membuat k6 GAGAL (F9-11).
//
// Angkanya DITURUNKAN dari pengukuran pertama terhadap sistem Go
// (docs/performance-report.md, 2026-09-07): lima kali p95 terukur, dibulatkan
// ke atas. Lima, karena yang ingin ditangkap adalah regresi satu orde - query
// yang kehilangan indeks, panggilan gRPC yang menjadi dua - bukan gangguan
// mesin yang sedang membangun image di sebelahnya.
//
// B2-08 merencanakan SLO dari baseline Laravel. Baseline itu belum pernah
// diukur, jadi ambang ini provisional dan ditinjau bersama laporan saat B2-07
// dijalankan. Mengarang angka p95 yang "terdengar wajar" tetap dilarang; yang
// ada di sini punya sumber, dan sumbernya disebut.

// Tingkat kegagalan HTTP. Satu persen adalah kebijakan, bukan pengukuran:
// pada beban uji tanpa gangguan, permintaan yang gagal adalah cacat.
const failureRate = ["rate<0.01"];

// Pendaftaran diberi ambang sendiri: argon2id (64 MiB, tiga iterasi) membuat
// p95-nya 375 ms saat diukur, dan batas derivasi serentak yang baru menambah
// antrean di bawah beban. Tanpa ini, satu rute yang memang lambat akan
// menyeret ambang rute lain ke atas.
const register = { "http_req_duration{name:POST /register}": ["p(95)<1500"] };

export const slo = {
  read: Object.assign(
    {
      http_req_failed: failureRate,
      checks: ["rate>0.99"],
      // terukur: p95 4,4 ms, p99 11 ms
      "http_req_duration{expected_response:true}": ["p(95)<25", "p(99)<60"],
    },
    register,
  ),
  write: Object.assign(
    {
      http_req_failed: failureRate,
      checks: ["rate>0.99"],
      // terukur (tanpa pendaftaran): p95 14,5 ms, p99 26 ms
      "http_req_duration{name:PATCH /profile}": ["p(95)<75", "p(99)<150"],
      "http_req_duration{name:POST /risk-assessments}": ["p(95)<75", "p(99)<150"],
      "http_req_duration{name:PATCH /culinary/preferences}": ["p(95)<75", "p(99)<150"],
    },
    register,
  ),
  mixed: Object.assign(
    {
      http_req_failed: failureRate,
      checks: ["rate>0.99"],
      // terukur: p95 9,7 ms, p99 14 ms
      "http_req_duration{expected_response:true}": ["p(95)<50", "p(99)<100"],
    },
    register,
  ),
};
