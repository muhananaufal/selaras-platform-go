// MIXED scenario: the traffic shape closest to real use.
//
// Two scenarios run together: readers opening pages (the majority) and
// writers changing data (the minority). The 4:1 ratio comes from the shape
// of the app - one assessment is read many times through the dashboard and
// the history - not from production traffic, which has never existed.
//
// Run:
//   task k6 -- mixed

import { sleep } from "k6";
import { call, completeProfile, sessionFor, startAssessment } from "../lib/api.js";
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

  call(token, "/edge.v1.Dashboard/GetDashboard");
  call(token, "/edge.v1.Assessment/ListAssessments");
  call(token, "/edge.v1.Profile/GetProfile");

  sleep(1);
}

export function writer() {
  const { token } = sessionFor("mixed-w", (t) => completeProfile(t));

  startAssessment(token);
  call(token, "/edge.v1.Profile/UpdateProfile", { lastName: `Iterasi${__ITER}` });

  sleep(2);
}
