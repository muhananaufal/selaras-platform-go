// WRITE scenario: the paths that change data without touching the LLM.
//
// Three writes per iteration: updating the profile, starting a risk
// assessment (computing SCORE2 and writing the outbox in one transaction),
// and changing the culinary preferences. Personalisation, coaching, chat, and
// meal guides are DELIBERATELY absent - they all queue LLM work, and that has
// a different load shape.
//
// Run:
//   task k6 -- write

import { sleep } from "k6";
import { call, completeProfile, sessionFor, startAssessment } from "../lib/api.js";
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

const budgets = ["BUDGET_LEVEL_THRIFTY", "BUDGET_LEVEL_STANDARD", "BUDGET_LEVEL_FLEXIBLE"];

export default function () {
  const { token } = sessionFor("write", (t) => completeProfile(t));

  call(token, "/edge.v1.Profile/UpdateProfile", { firstName: "Beban", lastName: `Iterasi${__ITER}` });
  startAssessment(token);
  call(token, "/edge.v1.Nutrition/UpdatePreferences", { budgetLevel: budgets[__ITER % budgets.length] });

  sleep(1);
}
