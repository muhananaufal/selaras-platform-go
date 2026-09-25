// Builds a Postman v2.1 collection that can be "Run" with no setup: baseUrl
// in a collection variable, the token captured from Register/Login, slugs and
// ids captured from earlier answers, and a status assertion on every request.
//
// Every request is a Connect unary call: POST <baseUrl>/<package>.<Service>/<Method>
// with a JSON body and Connect-Protocol-Version: 1 - the same wire the browser
// uses (ADR-027). The Watch* server streams are not included: Postman has no
// reader for the Connect streaming envelope, and the e2e suite covers them.
//
// The collection holds NO saved responses on purpose: an example written by
// hand is a claim about the code that goes stale unnoticed.
const fs = require("fs");
const out = process.argv[2];

const BASE = "http://127.0.0.1:18080";
const PASSWORD = "correct-horse-battery";

const test = (lines) => ({ listen: "test", script: { type: "text/javascript", exec: lines } });
const pre = (lines) => ({ listen: "prerequest", script: { type: "text/javascript", exec: lines } });
const expectStatus = (code, extra = []) => test([
  `pm.test("status ${code}", () => pm.response.to.have.status(${code}));`,
  ...extra,
]);
const expectCode = (httpStatus, code) => expectStatus(httpStatus, [
  `pm.test("code ${code}", () => pm.expect(pm.response.json().code).to.eql("${code}"));`,
]);
const capture = (name, path) =>
  `pm.collectionVariables.set("${name}", pm.response.json()${path});`;

const url = (procedure) => ({
  raw: `{{baseUrl}}${procedure}`,
  host: ["{{baseUrl}}"],
  path: procedure.replace(/^\//, "").split("/"),
});

function rpc(name, procedure, { body = {}, auth = true, events = [], description = "" } = {}) {
  const r = {
    method: "POST",
    header: [
      { key: "Content-Type", value: "application/json" },
      { key: "Connect-Protocol-Version", value: "1" },
    ],
    url: url(procedure),
    body: { mode: "raw", raw: JSON.stringify(body, null, 2), options: { raw: { language: "json" } } },
  };
  if (!auth) r.auth = { type: "noauth" };
  if (description) r.description = description;
  return { name, request: r, event: events };
}

// A pause in the pre-request script: asynchronous work (events, the outbox
// relay, the fake LLM worker) needs a second or two before its result is
// readable.
const waitMs = (ms) => pre([`setTimeout(() => {}, ${ms});`]);

const manual = (value) => ({ mode: "INPUT_MODE_MANUAL", measuredValue: value });
const assessmentInput = {
  hasDiabetes: false,
  smokingStatus: "SMOKING_STATUS_CURRENT",
  exercise: "EXERCISE_HABIT_RARELY",
  systolicBloodPressure: manual(150),
  totalCholesterol: manual(6.2),
  hdlCholesterol: manual(1.0),
};

const folders = [
  {
    name: "1 · Auth",
    description: "Sign up, sign in, see yourself. The token from Login is kept in {{bearerToken}} and used by every later request.",
    item: [
      rpc("Register", "/edge.v1.Auth/Register", {
        auth: false,
        body: { email: "{{email}}", password: PASSWORD, passwordConfirmation: PASSWORD },
        events: [
          pre([`pm.collectionVariables.set("email", "postman-" + Date.now() + "@user.co");`]),
          expectStatus(200, [capture("bearerToken", ".session.accessToken"),
            `pm.test("accessToken present", () => pm.expect(pm.response.json().session.accessToken).to.be.a("string"));`]),
        ],
        description: "The email is made unique per run by the pre-request script.",
      }),
      rpc("Login", "/edge.v1.Auth/Login", {
        auth: false,
        body: { email: "{{email}}", password: PASSWORD },
        events: [expectStatus(200, [capture("bearerToken", ".session.accessToken")])],
        description: "Signing in revokes every earlier session (D1); the token from Register stops working after this.",
      }),
      rpc("Get me", "/edge.v1.Auth/GetMe", { events: [expectStatus(200)] }),
    ],
  },
  {
    name: "2 · Profile",
    item: [
      rpc("Get profile", "/edge.v1.Profile/GetProfile", { events: [expectStatus(200)] }),
      rpc("Update profile", "/edge.v1.Profile/UpdateProfile", {
        body: { firstName: "Uji", lastName: "Postman", dateOfBirth: "1970-05-10", sex: "SEX_MALE", countryOfResidence: "Indonesia" },
        events: [expectStatus(200)],
        description: "Required before a risk assessment: age, sex, and country select the SCORE2 table.",
      }),
    ],
  },
  {
    name: "3 · Risk assessment",
    item: [
      rpc("Start assessment", "/edge.v1.Assessment/StartAssessment", {
        body: { input: assessmentInput },
        events: [expectStatus(200, [capture("assessmentSlug", ".assessment.slug")])],
        description: "SCORE2 is computed synchronously; the LLM report is requested separately.",
      }),
      rpc("Get assessment", "/edge.v1.Assessment/GetAssessment", {
        body: { slug: "{{assessmentSlug}}" }, events: [expectStatus(200)],
      }),
      rpc("Request personalization", "/edge.v1.Assessment/RequestPersonalization", {
        body: { slug: "{{assessmentSlug}}" },
        events: [expectStatus(200)],
        description: "Queues the LLM job and returns at once; the report appears on GetAssessment (or WatchAssessment) a few seconds later with the fake provider.",
      }),
      rpc("Get assessment (after personalization)", "/edge.v1.Assessment/GetAssessment", {
        body: { slug: "{{assessmentSlug}}" }, events: [waitMs(3000), expectStatus(200)],
      }),
      rpc("List assessments", "/edge.v1.Assessment/ListAssessments", { events: [expectStatus(200)] }),
    ],
  },
  {
    name: "4 · Dashboard",
    item: [
      rpc("Get dashboard", "/edge.v1.Dashboard/GetDashboard", {
        events: [waitMs(1500), expectStatus(200)],
        description: "A read-model materialised from events; the short pause lets the projection catch up.",
      }),
    ],
  },
  {
    name: "5 · Coaching",
    item: [
      rpc("Start program", "/edge.v1.Coaching/StartProgram", {
        body: { riskAssessmentSlug: "{{assessmentSlug}}", difficulty: "DIFFICULTY_STANDARD" },
        events: [waitMs(2500), expectStatus(200, [capture("programSlug", ".program.slug")])],
        description: "Sourced from the assessment above (D3: one program per assessment). Coaching learns of the assessment through an event, hence the pause.",
      }),
      rpc("Get program (curriculum ready)", "/edge.v1.Coaching/GetProgram", {
        body: { slug: "{{programSlug}}" },
        events: [waitMs(4000), expectStatus(200, [
          `const weeks = pm.response.json().program.weeks || [];`,
          `pm.test("curriculum arrived", () => pm.expect(weeks.length).to.be.above(0));`,
          `const task = weeks.flatMap(w => w.tasks || []).find(t => t.id);`,
          `if (task) pm.collectionVariables.set("taskId", task.id);`,
        ])],
      }),
      rpc("Complete a task", "/edge.v1.Coaching/ToggleTaskStatus", {
        body: { taskId: "{{taskId}}" },
        events: [expectStatus(200, [`pm.test("task completed", () => pm.expect(pm.response.json().task.completed).to.eql(true));`])],
      }),
      rpc("Pause program", "/edge.v1.Coaching/ToggleProgramStatus", { body: { slug: "{{programSlug}}" }, events: [expectStatus(200)] }),
      rpc("Resume program", "/edge.v1.Coaching/ToggleProgramStatus", { body: { slug: "{{programSlug}}" }, events: [expectStatus(200)] }),
      rpc("Open a thread", "/edge.v1.Coaching/StartThread", {
        body: { programSlug: "{{programSlug}}", title: "Soal sarapan", message: "Sarapan apa yang cocok untuk minggu pertama?" },
        events: [expectStatus(200, [capture("threadSlug", ".thread.slug")])],
      }),
      rpc("Get thread", "/edge.v1.Coaching/GetThread", { body: { slug: "{{threadSlug}}" }, events: [waitMs(3000), expectStatus(200)] }),
      rpc("Rename thread", "/edge.v1.Coaching/UpdateThreadTitle", { body: { slug: "{{threadSlug}}", title: "Sarapan minggu pertama" }, events: [expectStatus(200)] }),
      rpc("Reply in thread", "/edge.v1.Coaching/SendThreadMessage", { body: { threadSlug: "{{threadSlug}}", message: "Kalau tidak sempat masak?" }, events: [expectStatus(200)] }),
      rpc("Delete thread", "/edge.v1.Coaching/DeleteThread", { body: { slug: "{{threadSlug}}" }, events: [waitMs(2000), expectStatus(200)] }),
      rpc("Graduation report", "/edge.v1.Coaching/GetGraduationReport", {
        body: { programSlug: "{{programSlug}}" },
        events: [expectStatus(200)],
        description: "The status says whether the report is PENDING or READY. Requesting it locks the program (D5), so the thread is deleted first.",
      }),
      rpc("Delete program", "/edge.v1.Coaching/DeleteProgram", { body: { slug: "{{programSlug}}" }, events: [expectStatus(200)] }),
    ],
  },
  {
    name: "6 · Chat",
    item: [
      rpc("Create conversation", "/edge.v1.Chat/CreateConversation", {
        body: { message: "Apakah kopi berpengaruh pada tekanan darah saya?" },
        events: [expectStatus(200, [capture("conversationSlug", ".conversation.slug")])],
      }),
      rpc("List conversations", "/edge.v1.Chat/ListConversations", { events: [expectStatus(200)] }),
      rpc("Get conversation", "/edge.v1.Chat/GetConversation", { body: { slug: "{{conversationSlug}}" }, events: [waitMs(3000), expectStatus(200)] }),
      rpc("Rename conversation", "/edge.v1.Chat/UpdateConversationTitle", { body: { slug: "{{conversationSlug}}", title: "Soal kopi" }, events: [expectStatus(200)] }),
      rpc("Send message", "/edge.v1.Chat/SendMessage", { body: { conversationSlug: "{{conversationSlug}}", message: "Berapa cangkir yang aman?" }, events: [expectStatus(200)] }),
      rpc("Delete conversation", "/edge.v1.Chat/DeleteConversation", { body: { slug: "{{conversationSlug}}" }, events: [waitMs(2000), expectStatus(200)] }),
    ],
  },
  {
    name: "7 · Culinary",
    item: [
      rpc("Get hub data", "/edge.v1.Nutrition/GetHubData", { events: [expectStatus(200)] }),
      rpc("Update preferences", "/edge.v1.Nutrition/UpdatePreferences", {
        body: {
          allergies: "udang dan kepiting", budgetLevel: "BUDGET_LEVEL_THRIFTY", cookingStyle: "COOKING_STYLE_QUICK_EVERY_TIME",
          tasteProfiles: { values: ["pedas", "gurih"] }, kitchenEquipment: { values: ["wajan", "rice cooker"] },
        },
        events: [expectStatus(200)],
      }),
      rpc("Ask for a daily guide", "/edge.v1.Nutrition/GenerateDailyGuide", {
        body: { input: {
          planType: "PLAN_TYPE_COOK_AT_HOME", timeAvailability: "TIME_AVAILABILITY_QUICK", energyLevel: "ENERGY_LEVEL_TIRED",
          cuisinePreference: "Masakan Sunda", cravingType: "CRAVING_TYPE_SOUPY_AND_WARM", socialContext: "SOCIAL_CONTEXT_ALONE",
        } },
        events: [expectStatus(200)],
      }),
      rpc("Get hub data (guide arrived)", "/edge.v1.Nutrition/GetHubData", { events: [waitMs(3000), expectStatus(200)] }),
      rpc("Mistyped enum is refused", "/edge.v1.Nutrition/GenerateDailyGuide", {
        body: { input: {
          planType: "PLAN_TYPE_COOK_AT_HOME", timeAvailability: "TIME_AVAILABILITY_QUICK", energyLevel: "ENERGY_LEVEL_TIRED",
          cuisinePreference: "Masakan Sunda", cravingType: "CRAVING_TYPE_GRILLD",
        } },
        events: [expectCode(400, "invalid_argument")],
        description: "An unknown enum name is refused, not read as \"no craving\" (the strict codec, ADR-027).",
      }),
    ],
  },
  {
    name: "8 · Account",
    item: [
      rpc("Request password reset", "/edge.v1.Auth/RequestPasswordReset", {
        auth: false, body: { email: "{{email}}" }, events: [expectStatus(200)],
        description: "The link goes to Mailpit: http://127.0.0.1:18025. Confirming needs the token from that email, so it is not part of the automatic run.",
      }),
      rpc("Logout", "/edge.v1.Auth/Logout", { events: [expectStatus(200)] }),
      rpc("Get me after logout (401)", "/edge.v1.Auth/GetMe", {
        events: [expectCode(401, "unauthenticated")],
        description: "Revocation through the generation counter (ADR-020) takes effect at once.",
      }),
      rpc("Login again", "/edge.v1.Auth/Login", { auth: false, body: { email: "{{email}}", password: PASSWORD }, events: [expectStatus(200, [capture("bearerToken", ".session.accessToken")])] }),
      rpc("Delete account (wrong password → 403)", "/edge.v1.Auth/DeleteAccount", { body: { password: "jelas-bukan-kata-sandinya" }, events: [expectCode(403, "permission_denied")] }),
      rpc("Delete account", "/edge.v1.Auth/DeleteAccount", {
        body: { password: PASSWORD }, events: [expectStatus(200)],
        description: "The deletion saga across units (ADR-011); the status says IN_PROGRESS because the other units delete their data through events.",
      }),
    ],
  },
];

const count = (items) => items.reduce((n, i) => n + (i.item ? count(i.item) : 1), 0);

const collection = {
  info: {
    name: "Selaras Platform (Go)",
    description: `The whole edge.v1 flow over the Connect protocol, ready to "Run" with no setup: the local stack must be running (task up:full), then run this collection in order. Tokens, slugs, and ids are captured between requests; the email is unique per run. The source of truth is api/proto/edge/v1.`,
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
console.log(`written ${out}: ${folders.length} folders, ${count(folders)} requests`);
