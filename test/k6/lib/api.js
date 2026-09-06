// Pustaka bersama skenario k6.
//
// Satu tempat untuk bentuk permintaan dan pembacaan jawabannya, supaya tiga
// skenario tidak menyalin tiga versi yang perlahan menyimpang. Bentuknya
// mengikuti kontrak publik (api/openapi/edge-v1.yaml) dan suite e2e - kalau
// salah satunya berubah, yang gagal di sini adalah pemeriksaan, bukan angka
// latensi yang diam-diam mengukur jalur galat.

import http from "k6/http";
import { check, fail } from "k6";

export const BASE_URL = __ENV.BASE_URL || "http://127.0.0.1:18080";

// Kata sandi memenuhi aturan minimum pendaftaran. Bukan rahasia: akun yang
// dibuat di sini adalah akun uji yang dibuang setelah pengukuran.
const PASSWORD = "correct-horse-battery";

const JSON_HEADERS = { "Content-Type": "application/json", Accept: "application/json" };

function url(path) {
  return `${BASE_URL}/api/v1${path}`;
}

function authHeaders(token) {
  return { headers: Object.assign({ Authorization: `Bearer ${token}` }, JSON_HEADERS) };
}

// register mendaftarkan satu akun uji dan mengembalikan tokennya.
//
// Domain surelnya @user.co, sama dengan suite e2e: domain itu yang diizinkan
// linter data uji (docs/data-handling.md), dan yang menjaga akun uji tidak
// menyerupai alamat orang sungguhan.
export function register(tag) {
  const email = `k6-${tag}-${Date.now()}-${Math.floor(Math.random() * 1e6)}@user.co`;
  const res = http.post(
    url("/register"),
    JSON.stringify({
      name: "K6 Load",
      email,
      password: PASSWORD,
      password_confirmation: PASSWORD,
    }),
    { headers: JSON_HEADERS, tags: { name: "POST /register" } },
  );
  if (res.status !== 201 && res.status !== 200) {
    fail(`register answered ${res.status}: ${res.body}`);
  }
  // Token ada di AKAR jawaban, bukan di bawah "data" - bentuk yang
  // dipertahankan dari sistem lama (ADR-005).
  const token = res.json("access_token");
  if (!token) {
    fail(`register returned no access token: ${res.body}`);
  }
  return { email, token };
}

export function login(email) {
  const res = http.post(
    url("/login"),
    JSON.stringify({ email, password: PASSWORD }),
    { headers: JSON_HEADERS, tags: { name: "POST /login" } },
  );
  check(res, { "login 200": (r) => r.status === 200 });
  return res.json("access_token");
}

// completeProfile mengisi profil sampai penilaian risiko bisa dimulai.
export function completeProfile(token) {
  const res = http.patch(
    url("/profile"),
    JSON.stringify({
      first_name: "Beban",
      last_name: "Uji",
      date_of_birth: "1970-05-10",
      sex: "male",
      country_of_residence: "Indonesia",
    }),
    Object.assign(authHeaders(token), { tags: { name: "PATCH /profile" } }),
  );
  check(res, { "profile 200": (r) => r.status === 200 });
  unexpected("PATCH /profile", res, 200);
  return res;
}

// assessmentInput adalah kuesioner yang sah, seluruhnya manual - sama dengan
// yang dipakai suite e2e, sehingga jalur yang diukur adalah jalur yang
// terbukti benar.
export function assessmentInput() {
  return {
    has_diabetes: false,
    smoking_status: "Perokok aktif",
    q_exercise: "Jarang",
    sbp_input_type: "manual",
    sbp_value: 150,
    tchol_input_type: "manual",
    tchol_value: 6.2,
    hdl_input_type: "manual",
    hdl_value: 1.0,
  };
}

export function startAssessment(token) {
  const res = http.post(
    url("/risk-assessments"),
    JSON.stringify(assessmentInput()),
    Object.assign(authHeaders(token), { tags: { name: "POST /risk-assessments" } }),
  );
  check(res, { "assessment 201": (r) => r.status === 201 });
  unexpected("POST /risk-assessments", res, 201);
  return res;
}

// unexpected mencatat permintaan yang gagal beserta alasannya. Angka
// kegagalan tanpa alasan tidak bisa diselidiki setelah larian selesai - dan
// lima kegagalan dari enam ribu permintaan pernah lolos begitu saja.
export function unexpected(name, res, want) {
  if (res.status !== want) {
    console.warn(
      `${name}: status ${res.status} want ${want}; error=${res.error || "-"}; body=${String(res.body || "").slice(0, 200)}`,
    );
  }
}

export function get(token, path, name) {
  const res = http.get(url(path), Object.assign(authHeaders(token), { tags: { name } }));
  check(res, { [`${name} 200`]: (r) => r.status === 200 });
  unexpected(name, res, 200);
  return res;
}

export function patch(token, path, body, name) {
  const res = http.patch(
    url(path),
    JSON.stringify(body),
    Object.assign(authHeaders(token), { tags: { name } }),
  );
  check(res, { [`${name} 200`]: (r) => r.status === 200 });
  unexpected(name, res, 200);
  return res;
}

// Akun per VU, dibuat sekali pada iterasi pertama VU itu.
//
// setup() k6 berjalan di satu VU dan hasilnya disalin ke semua VU; membuat
// satu akun per VU di sana berarti N pendaftaran berurutan sebelum
// pengukuran mulai. Membuatnya malas di VU masing-masing menyebar
// pendaftarannya dan tetap menjaga satu akun per VU.
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
