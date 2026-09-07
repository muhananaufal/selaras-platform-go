// Membangun koleksi Postman v2.1 yang bisa di-"Run" tanpa setup:
// baseUrl di variabel koleksi, token ditangkap dari Register/Login, slug dan
// id ditangkap dari jawaban sebelumnya, tiap request punya asersi status.
const fs = require("fs");
const out = process.argv[2];

const BASE = "http://127.0.0.1:18080/api/v1";
const PASSWORD = "correct-horse-battery";

const test = (lines) => ({ listen: "test", script: { type: "text/javascript", exec: lines } });
const pre = (lines) => ({ listen: "prerequest", script: { type: "text/javascript", exec: lines } });
const expectStatus = (code, extra = []) => test([
  `pm.test("status ${code}", () => pm.response.to.have.status(${code}));`,
  ...extra,
]);
const capture = (name, path) =>
  `pm.collectionVariables.set("${name}", pm.response.json()${path});`;

const url = (path) => ({
  raw: `{{baseUrl}}${path}`,
  host: ["{{baseUrl}}"],
  path: path.replace(/^\//, "").split("/"),
});

function req(name, method, path, { body, auth = true, events = [], description = "" } = {}) {
  const r = { method, header: [{ key: "Accept", value: "application/json" }], url: url(path) };
  if (body !== undefined) {
    r.header.push({ key: "Content-Type", value: "application/json" });
    r.body = { mode: "raw", raw: JSON.stringify(body, null, 2), options: { raw: { language: "json" } } };
  }
  if (!auth) r.auth = { type: "noauth" };
  if (description) r.description = description;
  return { name, request: r, event: events };
}

// Jeda di pre-request: proses asinkron (event, relay outbox, worker LLM
// palsu) butuh sekitar satu-dua detik sebelum hasilnya bisa dibaca.
const waitMs = (ms) => pre([`setTimeout(() => {}, ${ms});`]);

const assessmentBody = {
  has_diabetes: false, smoking_status: "Perokok aktif", q_exercise: "Jarang",
  sbp_input_type: "manual", sbp_value: 150,
  tchol_input_type: "manual", tchol_value: 6.2,
  hdl_input_type: "manual", hdl_value: 1.0,
};

const folders = [
  {
    name: "1 · Auth", description: "Daftar, masuk, lihat diri sendiri. Token dari Login disimpan ke {{bearerToken}} dan dipakai seluruh request berikutnya.",
    item: [
      req("Register", "POST", "/register", {
        auth: false,
        body: { name: "Postman", email: "{{email}}", password: PASSWORD, password_confirmation: PASSWORD },
        events: [
          pre([`pm.collectionVariables.set("email", "postman-" + Date.now() + "@user.co");`]),
          expectStatus(201, [capture("bearerToken", ".access_token"),
            `pm.test("access_token ada", () => pm.expect(pm.response.json().access_token).to.be.a("string"));`]),
        ],
        description: "Surel dibuat unik per larian oleh skrip pre-request.",
      }),
      req("Login", "POST", "/login", {
        auth: false,
        body: { email: "{{email}}", password: PASSWORD },
        events: [expectStatus(200, [capture("bearerToken", ".access_token")])],
        description: "Login mencabut seluruh sesi sebelumnya (D1); token dari Register tidak berlaku lagi setelah ini.",
      }),
      req("Me", "GET", "/me", { events: [expectStatus(200)] }),
    ],
  },
  {
    name: "2 · Profile",
    item: [
      req("Get profile", "GET", "/profile", { events: [expectStatus(200)] }),
      req("Update profile", "PATCH", "/profile", {
        body: { first_name: "Uji", last_name: "Postman", date_of_birth: "1970-05-10", sex: "male", country_of_residence: "Indonesia" },
        events: [expectStatus(200)],
        description: "Wajib sebelum analisis risiko: umur, jenis kelamin, dan negara menentukan tabel SCORE2.",
      }),
    ],
  },
  {
    name: "3 · Risk assessment",
    item: [
      req("Create assessment", "POST", "/risk-assessments", {
        body: assessmentBody,
        events: [expectStatus(201, [capture("assessmentSlug", ".data.slug")])],
        description: "Skor SCORE2 dihitung sinkron; personalisasi LLM diminta terpisah.",
      }),
      req("Get assessment", "GET", "/risk-assessments/{{assessmentSlug}}", { events: [expectStatus(200)] }),
      req("Request personalization", "PATCH", "/risk-assessments/{{assessmentSlug}}/personalize", {
        events: [expectStatus(202)],
        description: "202: pekerjaan LLM diantrekan; laporannya muncul di GET beberapa detik kemudian (penyedia palsu di lokal).",
      }),
      req("Get assessment (after personalization)", "GET", "/risk-assessments/{{assessmentSlug}}", {
        events: [waitMs(3000), expectStatus(200)],
      }),
    ],
  },
  {
    name: "4 · Dashboard",
    item: [
      req("Dashboard", "GET", "/dashboard", {
        events: [waitMs(1500), expectStatus(200)],
        description: "Read-model yang dimaterialisasi dari event; jeda singkat memberi waktu proyeksi menyusul.",
      }),
    ],
  },
  {
    name: "5 · Coaching",
    item: [
      req("Start program", "POST", "/coaching/programs", {
        body: { risk_assessment_slug: "{{assessmentSlug}}", difficulty: "Standar & Konsisten" },
        events: [waitMs(2500), expectStatus(202, [capture("programSlug", ".data.slug")])],
        description: "Program bersumber dari analisis di atas (D3: satu program per analisis). Coaching mengenal analisis lewat event, karena itu ada jeda sebelum request ini.",
      }),
      req("Get program (curriculum ready)", "GET", "/coaching/programs/{{programSlug}}", {
        events: [waitMs(4000), expectStatus(200, [
          `const weeks = pm.response.json().data.weeks || [];`,
          `pm.test("kurikulum sudah ada", () => pm.expect(weeks.length).to.be.above(0));`,
          `const task = weeks.flatMap(w => w.tasks || []).find(t => t.id);`,
          `if (task) pm.collectionVariables.set("taskId", task.id);`,
        ])],
      }),
      req("Complete a task", "PATCH", "/coaching/tasks/{{taskId}}/toggle-task-status", {
        events: [expectStatus(200, [`pm.test("tugas selesai", () => pm.expect(pm.response.json().data.is_completed).to.eql(true));`])],
      }),
      req("Pause program", "PATCH", "/coaching/programs/{{programSlug}}/toggle-program-status", { events: [expectStatus(200)] }),
      req("Resume program", "PATCH", "/coaching/programs/{{programSlug}}/toggle-program-status", { events: [expectStatus(200)] }),
      req("Open a thread", "POST", "/coaching/programs/{{programSlug}}/threads", {
        body: { title: "Soal sarapan", message: "Sarapan apa yang cocok untuk minggu pertama?" },
        events: [expectStatus(202, [capture("threadSlug", ".data.slug")])],
      }),
      req("Get thread", "GET", "/coaching/threads/{{threadSlug}}", { events: [waitMs(3000), expectStatus(200)] }),
      req("Rename thread", "PATCH", "/coaching/threads/{{threadSlug}}", { body: { title: "Sarapan minggu pertama" }, events: [expectStatus(200)] }),
      req("Reply in thread", "POST", "/coaching/threads/{{threadSlug}}/messages", { body: { message: "Kalau tidak sempat masak?" }, events: [expectStatus(202)] }),
      req("Delete thread", "DELETE", "/coaching/threads/{{threadSlug}}", { events: [waitMs(2000), expectStatus(204)] }),
      req("Graduation report", "GET", "/coaching/programs/{{programSlug}}/graduation-report", {
        events: [test([`pm.test("status 200 atau 202", () => pm.expect([200, 202]).to.include(pm.response.code));`])],
        description: "202 saat laporan baru diminta, 200 saat sudah ada. Meminta laporan mengunci program (D5), jadi thread dihapus sebelum ini.",
      }),
      req("Delete program", "DELETE", "/coaching/programs/{{programSlug}}", { events: [expectStatus(204)] }),
    ],
  },
  {
    name: "6 · Chat",
    item: [
      req("Create conversation", "POST", "/chat/conversations", {
        body: { message: "Apakah kopi berpengaruh pada tekanan darah saya?" },
        events: [expectStatus(202, [capture("conversationSlug", ".data.slug")])],
      }),
      req("List conversations", "GET", "/chat/conversations", { events: [expectStatus(200)] }),
      req("Get conversation", "GET", "/chat/conversations/{{conversationSlug}}", { events: [waitMs(3000), expectStatus(200)] }),
      req("Rename conversation", "PATCH", "/chat/conversations/{{conversationSlug}}", { body: { title: "Soal kopi" }, events: [expectStatus(200)] }),
      req("Send message", "POST", "/chat/conversations/{{conversationSlug}}/messages", { body: { message: "Berapa cangkir yang aman?" }, events: [expectStatus(202)] }),
      req("Delete conversation", "DELETE", "/chat/conversations/{{conversationSlug}}", { events: [waitMs(2000), expectStatus(204)] }),
    ],
  },
  {
    name: "7 · Culinary",
    item: [
      req("Hub data", "GET", "/culinary/hub-data", { events: [expectStatus(200)] }),
      req("Update preferences", "PATCH", "/culinary/preferences", {
        body: { allergies: "udang dan kepiting", budget_level: "thrifty", cooking_style: "quick_every_time", taste_profiles: ["pedas", "gurih"], kitchen_equipment: ["wajan", "rice cooker"] },
        events: [expectStatus(200)],
      }),
      req("Ask for a daily guide", "POST", "/culinary/daily-guides", {
        body: { plan_type: "cook_at_home", time_availability: "quick", energy_level: "tired", cuisine_preference: "Masakan Sunda", craving_type: "soupy_and_warm", social_context: "alone" },
        events: [expectStatus(202)],
      }),
      req("Hub data (guide arrived)", "GET", "/culinary/hub-data", { events: [waitMs(3000), expectStatus(200)] }),
    ],
  },
  {
    name: "8 · Account",
    item: [
      req("Request password reset", "POST", "/password-reset/request", {
        auth: false, body: { email: "{{email}}" }, events: [expectStatus(202)],
        description: "Tautannya dikirim ke Mailpit: http://127.0.0.1:18025. Konfirmasi butuh token dari surel itu, jadi tidak ikut dalam larian otomatis.",
      }),
      req("Logout", "POST", "/logout", { events: [expectStatus(200)] }),
      req("Me after logout (401)", "GET", "/me", { events: [expectStatus(401)], description: "Pencabutan lewat penghitung generasi (ADR-020) berlaku seketika." }),
      req("Login again", "POST", "/login", { auth: false, body: { email: "{{email}}", password: PASSWORD }, events: [expectStatus(200, [capture("bearerToken", ".access_token")])] }),
      req("Delete account (wrong password → 403)", "DELETE", "/delete-account", { body: { password: "jelas-bukan-kata-sandinya" }, events: [expectStatus(403)] }),
      req("Delete account", "DELETE", "/delete-account", {
        body: { password: PASSWORD }, events: [expectStatus(202)],
        description: "Saga penghapusan lintas unit (ADR-011); 202 karena unit lain menghapus datanya lewat event.",
      }),
    ],
  },
];

const collection = {
  info: {
    name: "Selaras Platform (Go)",
    description: `Alur lengkap 32 endpoint edge-gateway, siap "Run" tanpa setup: stack lokal harus menyala (task up:full), lalu jalankan koleksi ini berurutan. Token, slug, dan id ditangkap otomatis antar-request; surel dibuat unik per larian. Sumber kebenarannya api/openapi/edge-v1.yaml.`,
    schema: "https://schema.getpostman.com/json/collection/v2.1.0/collection.json",
  },
  auth: { type: "bearer", bearer: [{ key: "token", value: "{{bearerToken}}", type: "string" }] },
  variable: [
    { key: "baseUrl", value: BASE, type: "string" },
    { key: "bearerToken", value: "", type: "string" },
    { key: "email", value: "", type: "string" },
    { key: "assessmentSlug", value: "", type: "string" },
    { key: "programSlug", value: "", type: "string" },
    { key: "taskId", value: "", type: "string" },
    { key: "threadSlug", value: "", type: "string" },
    { key: "conversationSlug", value: "", type: "string" },
  ],
  item: folders,
};

fs.writeFileSync(out, JSON.stringify(collection, null, 2));
const count = (items) => items.reduce((n, i) => n + (i.item ? count(i.item) : 1), 0);
console.log(`written ${out}: ${folders.length} folders, ${count(folders)} requests`);
