// Skenario TULIS: jalur yang mengubah data tanpa menyentuh LLM.
//
// Tiga penulisan per iterasi: memperbarui profil, memulai penilaian risiko
// (menghitung SCORE2 dan menulis outbox dalam satu transaksi), dan mengubah
// preferensi kuliner. Personalisasi, coaching, chat, dan panduan menu
// SENGAJA tidak ada di sini - semuanya mengantre pekerjaan LLM, dan itu
// diukur terpisah (scenarios/llm.js) karena bentuk bebannya berbeda.
//
// Menjalankan:
//   task k6 -- write

import { sleep } from "k6";
import { completeProfile, patch, sessionFor, startAssessment } from "../lib/api.js";
import { slo } from "../lib/slo.js";

export const options = {
  scenarios: {
    write: {
      executor: "ramping-vus",
      startVUs: 0,
      stages: [
        { duration: "30s", target: 10 },
        { duration: "60s", target: 10 },
        { duration: "15s", target: 0 },
      ],
      gracefulRampDown: "10s",
    },
  },
  thresholds: slo.write,
};

const budgets = ["thrifty", "standard", "flexible"];

export default function () {
  const { token } = sessionFor("write", (t) => completeProfile(t));

  patch(
    token,
    "/profile",
    { first_name: "Beban", last_name: `Iterasi${__ITER}` },
    "PATCH /profile",
  );

  startAssessment(token);

  patch(
    token,
    "/culinary/preferences",
    { budget_level: budgets[__ITER % budgets.length] },
    "PATCH /culinary/preferences",
  );

  sleep(1);
}
