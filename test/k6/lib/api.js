// Shared library of the k6 scenarios.
//
// One place for the shape of every request and the reading of its answer, so
// three scenarios do not copy three versions that slowly drift apart. The shape
// follows the public edge.v1 contract (api/proto/edge/v1) over the Connect
// protocol with JSON - the same wire the browser uses. If the contract changes,
// what fails here is a check, not a latency number silently measuring the
// error path.

import http from "k6/http";
import { check, fail } from "k6";

export const BASE_URL = __ENV.BASE_URL || "http://127.0.0.1:18080";

// The password meets the minimum registration rules. Not a secret: the
// accounts made here are throwaway test accounts.
const PASSWORD = "correct-horse-battery";

// Every Connect unary call is a POST of a JSON message to
// /<package>.<Service>/<Method>, with Connect-Protocol-Version: 1.
const CONNECT_HEADERS = {
  "Content-Type": "application/json",
  "Connect-Protocol-Version": "1",
};

function params(token, name) {
  const headers = Object.assign({}, CONNECT_HEADERS);
  if (token) {
    headers.Authorization = `Bearer ${token}`;
  }
  return { headers, tags: { name } };
}

// call runs one procedure and checks it succeeded. name is the metric tag;
// it is the procedure itself so thresholds and dashboards read the same label.
export function call(token, procedure, body) {
  const res = http.post(`${BASE_URL}${procedure}`, JSON.stringify(body || {}), params(token, procedure));
  check(res, { [`${procedure} 200`]: (r) => r.status === 200 });
  unexpected(procedure, res, 200);
  return res;
}

// register creates one test account and returns its token.
//
// The email domain is @user.co, the same as the e2e suite: it is the domain the
// test-data linter allows (docs/data-handling.md), and it keeps test accounts
// from resembling real addresses.
export function register(tag) {
  const email = `k6-${tag}-${Date.now()}-${Math.floor(Math.random() * 1e6)}@user.co`;
  const res = http.post(
    `${BASE_URL}/edge.v1.Auth/Register`,
    JSON.stringify({ email, password: PASSWORD, passwordConfirmation: PASSWORD }),
    params("", "/edge.v1.Auth/Register"),
  );
  if (res.status !== 200) {
    fail(`register answered ${res.status}: ${res.body}`);
  }
  const token = res.json("session.accessToken");
  if (!token) {
    fail(`register returned no access token: ${res.body}`);
  }
  return { email, token };
}

export function login(email) {
  const res = http.post(
    `${BASE_URL}/edge.v1.Auth/Login`,
    JSON.stringify({ email, password: PASSWORD }),
    params("", "/edge.v1.Auth/Login"),
  );
  check(res, { "login 200": (r) => r.status === 200 });
  return res.json("session.accessToken");
}

// completeProfile fills the profile in until a risk assessment can start.
export function completeProfile(token) {
  return call(token, "/edge.v1.Profile/UpdateProfile", {
    firstName: "Beban",
    lastName: "Uji",
    dateOfBirth: "1970-05-10",
    sex: "SEX_MALE",
    countryOfResidence: "Indonesia",
  });
}

// assessmentInput is a valid questionnaire, measured manually throughout -
// the same one the e2e suite uses, so the path measured is a path proven right.
export function assessmentInput() {
  const manual = (value) => ({ mode: "INPUT_MODE_MANUAL", measuredValue: value });
  return {
    hasDiabetes: false,
    smokingStatus: "SMOKING_STATUS_CURRENT",
    exercise: "EXERCISE_HABIT_RARELY",
    systolicBloodPressure: manual(150),
    totalCholesterol: manual(6.2),
    hdlCholesterol: manual(1.0),
  };
}

export function startAssessment(token) {
  return call(token, "/edge.v1.Assessment/StartAssessment", { input: assessmentInput() });
}

// unexpected logs a failed request with its reason. A failure rate without
// reasons cannot be investigated after the run - and five failures out of six
// thousand once slipped through exactly that way.
export function unexpected(name, res, want) {
  if (res.status !== want) {
    console.warn(
      `${name}: status ${res.status} want ${want}; error=${res.error || "-"}; body=${String(res.body || "").slice(0, 200)}`,
    );
  }
}

// One account per VU, created once on that VU's first iteration.
//
// k6's setup() runs on one VU and its result is copied to all of them; making
// one account per VU there would mean N registrations in a row before
// measurement starts. Making it lazily in each VU spreads the registrations
// and still keeps one account per VU.
const sessions = {};

export function sessionFor(tag, prepare) {
  if (!sessions[__VU]) {
    const account = register(`${tag}-vu${__VU}`);
    if (prepare) {
      prepare(account.token);
    }
    sessions[__VU] = account;
  }
  return sessions[__VU];
}
