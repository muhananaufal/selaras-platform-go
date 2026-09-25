// SLO thresholds that make k6 FAIL (F9-11).
//
// The numbers are DERIVED from the first measurement of the Go system
// (docs/performance-report.md, 2026-09-07): five times the measured p95,
// rounded up. Five, because what should be caught is a regression of an order
// of magnitude - a query that lost its index, a gRPC call that became two -
// not noise from a machine building an image next door.
//
// B2-08 planned the SLOs from a Laravel baseline that was never measured, so
// these thresholds are provisional. Inventing a p95 that "sounds reasonable"
// is still forbidden; these have a source, and it is named.
//
// The measurements were taken on the REST gateway. The Connect gateway keeps
// the same hops (HTTP in, gRPC behind), so the thresholds carry over; a
// re-measurement is recorded in docs/performance-report.md.

// HTTP failure rate. One percent is policy, not measurement: under test load
// without disruption, a failed request is a defect.
const failureRate = ["rate<0.01"];

// Registration has its own threshold: argon2id (64 MiB, three iterations)
// put its p95 at 375 ms when measured, and the concurrent-derivation cap adds
// queueing under load. Without this, one route that IS slow would drag every
// other route's threshold up.
const register = { "http_req_duration{name:/edge.v1.Auth/Register}": ["p(95)<1500"] };

export const slo = {
  read: Object.assign(
    {
      http_req_failed: failureRate,
      checks: ["rate>0.99"],
      // measured: p95 4.4 ms, p99 11 ms
      "http_req_duration{expected_response:true}": ["p(95)<25", "p(99)<60"],
    },
    register,
  ),
  write: Object.assign(
    {
      http_req_failed: failureRate,
      checks: ["rate>0.99"],
      // measured (without registration): p95 14.5 ms, p99 26 ms
      "http_req_duration{name:/edge.v1.Profile/UpdateProfile}": ["p(95)<75", "p(99)<150"],
      "http_req_duration{name:/edge.v1.Assessment/StartAssessment}": ["p(95)<75", "p(99)<150"],
      "http_req_duration{name:/edge.v1.Nutrition/UpdatePreferences}": ["p(95)<75", "p(99)<150"],
    },
    register,
  ),
  mixed: Object.assign(
    {
      http_req_failed: failureRate,
      checks: ["rate>0.99"],
      // measured: p95 9.7 ms, p99 14 ms
      "http_req_duration{expected_response:true}": ["p(95)<50", "p(99)<100"],
    },
    register,
  ),
};
