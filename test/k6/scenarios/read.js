// READ scenario: the path a user walks when opening the app.
//
// Five reads that make up the home page and the profile page. None of them
// queues LLM work, so what is measured is purely the gateway, gRPC, and
// Postgres - the same path as the B2-07 baseline.
//
// Run:
//   task k6 -- read
// or directly:
//   k6 run -e BASE_URL=http://127.0.0.1:18080 test/k6/scenarios/read.js

import { sleep } from "k6";
import { call, completeProfile, sessionFor, startAssessment } from "../lib/api.js";
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
  // One account per VU with a complete profile and one assessment, so the
  // dashboard and the history return data - not the cheaper "empty" path a
  // real user never sees.
  const { token } = sessionFor("read", (t) => {
    completeProfile(t);
    startAssessment(t);
  });

  call(token, "/edge.v1.Auth/GetMe");
  call(token, "/edge.v1.Profile/GetProfile");
  call(token, "/edge.v1.Assessment/ListAssessments");
  call(token, "/edge.v1.Dashboard/GetDashboard");
  call(token, "/edge.v1.Nutrition/GetHubData");

  sleep(1);
}
