// Skenario CAMPURAN: bentuk trafik yang paling mendekati pemakaian nyata.
//
// Dua skenario berjalan bersamaan: pembaca yang membuka halaman (mayoritas)
// dan penulis yang mengubah data (minoritas). Perbandingannya 4:1, ditetapkan
// dari bentuk aplikasinya - satu penilaian dibaca berkali-kali lewat
// dashboard dan riwayat - bukan dari pengukuran trafik produksi, yang memang
// tidak pernah ada.
//
// Menjalankan:
//   task k6 -- mixed

import { sleep } from "k6";
import { completeProfile, get, patch, sessionFor, startAssessment } from "../lib/api.js";
import { slo } from "../lib/slo.js";

export const options = {
  scenarios: {
    readers: {
      executor: "ramping-vus",
      exec: "reader",
      startVUs: 0,
      stages: [
        { duration: "30s", target: 16 },
        { duration: "90s", target: 16 },
        { duration: "15s", target: 0 },
      ],
      gracefulRampDown: "10s",
    },
    writers: {
      executor: "ramping-vus",
      exec: "writer",
      startVUs: 0,
      stages: [
        { duration: "30s", target: 4 },
        { duration: "90s", target: 4 },
        { duration: "15s", target: 0 },
      ],
      gracefulRampDown: "10s",
    },
  },
  thresholds: slo.mixed,
};

export function reader() {
  const { token } = sessionFor("mixed-r", (t) => {
    completeProfile(t);
    startAssessment(t);
  });

  get(token, "/dashboard", "GET /dashboard");
  get(token, "/risk-assessments", "GET /risk-assessments");
  get(token, "/profile", "GET /profile");

  sleep(1);
}

export function writer() {
  const { token } = sessionFor("mixed-w", (t) => completeProfile(t));

  startAssessment(token);
  patch(token, "/profile", { last_name: `Iterasi${__ITER}` }, "PATCH /profile");

  sleep(2);
}
