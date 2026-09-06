// Skenario BACA: jalur yang dilalui pengguna saat membuka aplikasi.
//
// Lima GET yang menyusun halaman utama dan halaman profil. Tidak ada satu pun
// yang mengantre pekerjaan LLM, sehingga yang diukur murni gateway, gRPC,
// dan Postgres - jalur yang sama dengan baseline B2-07.
//
// Menjalankan:
//   task k6 -- read
// atau langsung:
//   k6 run -e BASE_URL=http://127.0.0.1:18080 test/k6/scenarios/read.js

import { sleep } from "k6";
import { completeProfile, get, sessionFor, startAssessment } from "../lib/api.js";
import { slo } from "../lib/slo.js";

export const options = {
  scenarios: {
    read: {
      executor: "ramping-vus",
      startVUs: 0,
      stages: [
        { duration: "30s", target: 20 },
        { duration: "60s", target: 20 },
        { duration: "15s", target: 0 },
      ],
      gracefulRampDown: "10s",
    },
  },
  thresholds: slo.read,
};

export default function () {
  // Akun per VU dengan profil lengkap dan satu penilaian, supaya dashboard
  // dan daftar penilaian mengembalikan data - bukan jalur "kosong" yang
  // lebih murah dari yang dialami pengguna sungguhan.
  const { token } = sessionFor("read", (t) => {
    completeProfile(t);
    startAssessment(t);
  });

  get(token, "/me", "GET /me");
  get(token, "/profile", "GET /profile");
  get(token, "/risk-assessments", "GET /risk-assessments");
  get(token, "/dashboard", "GET /dashboard");
  get(token, "/culinary/hub-data", "GET /culinary/hub-data");

  sleep(1);
}
