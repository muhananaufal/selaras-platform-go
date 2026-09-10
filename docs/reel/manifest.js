// Manifes reel Selaras — dipakai hub (index.html) dan tiap reel (reel.js). Dibangun oleh tools/build-manifest.js.
// status 'jadi' = berkasnya ada dan bisa diputar; 'belum' = direncanakan (lihat plan.html). Teks: { id, en }.
const REELS = [
 {
  "id": "C",
  "tier": 1,
  "num": "C",
  "file": "reel-cerita.html",
  "title": {
   "id": "Sehari bersama Selaras",
   "en": "A day with Selaras"
  },
  "sub": {
   "id": "Sembilan adegan dari mendaftar sampai menghapus akun, dari sisi pengguna",
   "en": "Nine scenes from sign-up to account deletion, from the user's side"
  },
  "mech": [],
  "order": 0,
  "status": "belum"
 },
 {
  "id": "01",
  "tier": 2,
  "num": "01",
  "file": "reel-alur-01-daftar-masuk.html",
  "title": {
   "id": "Daftar, masuk, dan sesi",
   "en": "Sign-up, sign-in, and sessions"
  },
  "sub": {
   "id": "JWT EdDSA, generasi token, profil kosong lewat token bootstrap",
   "en": "EdDSA JWT, token generation, empty profile via bootstrap token"
  },
  "mech": [
   "M3",
   "M8"
  ],
  "order": 1,
  "status": "belum"
 },
 {
  "id": "02",
  "tier": 2,
  "num": "02",
  "file": "reel-alur-02-profil.html",
  "title": {
   "id": "Ubah profil → cache di unit lain",
   "en": "Profile update → caches in other units"
  },
  "sub": {
   "id": "Outbox, relay at-least-once, salinan yang boleh basi",
   "en": "Outbox, at-least-once relay, caches that may go stale"
  },
  "mech": [
   "M1",
   "M3",
   "M4",
   "M8"
  ],
  "order": 2,
  "status": "belum"
 },
 {
  "id": "03",
  "tier": 2,
  "num": "03",
  "file": "reel-alur-03-penilaian.html",
  "title": {
   "id": "Penilaian risiko SCORE2",
   "en": "SCORE2 risk assessment"
  },
  "sub": {
   "id": "Mesin risiko dengan paritas golden vector; hasil disiarkan ke coaching dan dasbor",
   "en": "Risk engine with golden-vector parity; results broadcast to coaching and dashboard"
  },
  "mech": [
   "M1",
   "M2",
   "M3",
   "M4",
   "M7",
   "M8"
  ],
  "order": 3,
  "status": "belum"
 },
 {
  "id": "04",
  "tier": 2,
  "num": "04",
  "file": "reel-alur-04-personalisasi.html",
  "title": {
   "id": "Personalisasi laporan lewat LLM",
   "en": "Report personalisation through the LLM"
  },
  "sub": {
   "id": "Jalur permintaan tidak memanggil LLM; worker mengerjakannya, hasil pulang lewat topic bersama",
   "en": "The request path never calls the LLM; a worker does, results return over a shared topic"
  },
  "mech": [
   "M1",
   "M2",
   "M3",
   "M4",
   "M5",
   "M8"
  ],
  "order": 4,
  "status": "jadi",
  "refs": [
   "test/postman/selaras.postman_collection.json",
   "internal/edge/middleware/bodylimit.go",
   "internal/edge/middleware/tracing.go",
   "internal/edge/middleware/auth.go",
   "ADR-020",
   "internal/edge/middleware/ratelimit.go",
   "internal/edge/handler/idempotency.go",
   "internal/platform/authn/interceptor.go",
   "ADR-026",
   "internal/assessment/app/request_personalization.go",
   "ADR-023",
   "ADR-004",
   "internal/platform/outbox/schema.sql",
   "internal/edge/handler/assessment.go",
   "docs/performance-report.md",
   "internal/platform/outbox/relay.go",
   "internal/platform/kafka/topics.go",
   "docs/topics.md",
   "ADR-014",
   "internal/llmworker/worker.go",
   "internal/platform/idempotency/guard.go",
   "internal/llm/gemini/gemini.go",
   "ADR-025",
   "internal/assessment/adapter/consumer/results.go",
   "docs/finops.md",
   "deploy/compose/observability/alerts.yml"
  ]
 },
 {
  "id": "05",
  "tier": 2,
  "num": "05",
  "file": "reel-alur-05-coaching.html",
  "title": {
   "id": "Program coaching sampai kelulusan",
   "en": "Coaching programme to graduation"
  },
  "sub": {
   "id": "Kurikulum LLM, tugas harian, utas pelatih, laporan kelulusan",
   "en": "LLM curriculum, daily tasks, coach threads, graduation report"
  },
  "mech": [
   "M1",
   "M2",
   "M3",
   "M4",
   "M5",
   "M7",
   "M8"
  ],
  "order": 5,
  "status": "belum"
 },
 {
  "id": "06",
  "tier": 2,
  "num": "06",
  "file": "reel-alur-06-chat.html",
  "title": {
   "id": "Percakapan dengan asisten",
   "en": "Conversation with the assistant"
  },
  "sub": {
   "id": "Pesan disimpan, HTTP menjawab segera, balasan tiba lewat llm.results",
   "en": "Message stored, HTTP answers at once, reply arrives via llm.results"
  },
  "mech": [
   "M1",
   "M2",
   "M3",
   "M4",
   "M5",
   "M8"
  ],
  "order": 6,
  "status": "belum"
 },
 {
  "id": "07",
  "tier": 2,
  "num": "07",
  "file": "reel-alur-07-nutrisi.html",
  "title": {
   "id": "Panduan menu harian",
   "en": "Daily meal guide"
  },
  "sub": {
   "id": "Konteks disimpan di samping hasilnya; bahasa dari cache",
   "en": "Context stored next to the result; language from cache"
  },
  "mech": [
   "M1",
   "M2",
   "M3",
   "M4",
   "M5",
   "M8"
  ],
  "order": 7,
  "status": "belum"
 },
 {
  "id": "08",
  "tier": 2,
  "num": "08",
  "file": "reel-alur-08-dasbor.html",
  "title": {
   "id": "Dasbor sebagai read-model",
   "en": "Dashboard as a read model"
  },
  "sub": {
   "id": "Proyeksi dari tiga topic, replika baca, pembangunan ulang",
   "en": "Projection from three topics, read replica, rebuild"
  },
  "mech": [
   "M2",
   "M3",
   "M4",
   "M7",
   "M8"
  ],
  "order": 8,
  "status": "belum"
 },
 {
  "id": "09",
  "tier": 2,
  "num": "09",
  "file": "reel-alur-09-hapus-akun.html",
  "title": {
   "id": "Hapus akun: saga enam unit",
   "en": "Account deletion: a six-unit saga"
  },
  "sub": {
   "id": "Konfirmasi enam peserta, kompensasi, saga menggantung",
   "en": "Six participants confirm, compensation, hanging sagas"
  },
  "mech": [
   "M1",
   "M2",
   "M3",
   "M4",
   "M6",
   "M7",
   "M8"
  ],
  "order": 9,
  "status": "belum"
 },
 {
  "id": "10",
  "tier": 2,
  "num": "10",
  "file": "reel-alur-10-beban-http.html",
  "title": {
   "id": "Autoscaling HTTP: HPA naik lalu turun",
   "en": "HTTP autoscaling: HPA up then down"
  },
  "sub": {
   "id": "60 VU, CPU 60 %, maks 4/3 replika, batas jujur satu node",
   "en": "60 VUs, 60 % CPU, max 4/3 replicas, the honest single-node limit"
  },
  "mech": [
   "M3",
   "M8",
   "M10"
  ],
  "order": 10,
  "status": "belum"
 },
 {
  "id": "11",
  "tier": 2,
  "num": "11",
  "file": "reel-alur-11-beban-llm.html",
  "title": {
   "id": "Autoscaling worker: KEDA dari nol",
   "en": "Worker autoscaling: KEDA from zero"
  },
  "sub": {
   "id": "Lag dari broker, lagThreshold 5, maks 12 = partisi, cold-start 8,3 s",
   "en": "Lag from the broker, lagThreshold 5, max 12 = partitions, 8.3 s cold start"
  },
  "mech": [
   "M1",
   "M2",
   "M4",
   "M5",
   "M8",
   "M10"
  ],
  "order": 11,
  "status": "belum"
 },
 {
  "id": "M1",
  "tier": 3,
  "num": "M1",
  "file": "reel-mekanisme-01-outbox.html",
  "title": {
   "id": "Transactional outbox dan relay",
   "en": "Transactional outbox and relay"
  },
  "sub": {
   "id": "Duplikat daripada kehilangan: SKIP LOCKED, publish dulu baru tandai",
   "en": "Duplicates over loss: SKIP LOCKED, publish first then mark"
  },
  "order": 12,
  "status": "jadi",
  "refs": [
   "internal/profile/app/publish.go",
   "ADR-004",
   "internal/profile/adapter/postgres",
   "internal/platform/outbox/outbox.go",
   "internal/platform/outbox/schema.sql",
   "internal/platform/outbox/outbox_integration_test.go",
   "internal/platform/outbox/relay.go",
   "cmd/profile-svc/relay.go",
   "internal/platform/outbox/routing.go",
   "internal/platform/kafka/publisher.go",
   "internal/platform/kafka/topics.go",
   "ADR-014",
   "internal/assessment/adapter/consumer/results.go",
   "internal/edge/handler/idempotency.go",
   "docs/runbook/profile-svc.md",
   "internal/platform/outbox/relay_test.go",
   "internal/platform/idempotency/guard.go"
  ]
 },
 {
  "id": "M2",
  "tier": 3,
  "num": "M2",
  "file": "reel-mekanisme-02-idempotensi.html",
  "title": {
   "id": "Idempotensi penerima",
   "en": "Consumer idempotency"
  },
  "sub": {
   "id": "Satu INSERT yang memutuskan, scope per konsumen, kunci diikat ke pengguna",
   "en": "One deciding INSERT, per-consumer scope, key bound to the user"
  },
  "order": 13,
  "status": "jadi",
  "refs": [
   "internal/edge/handler/idempotency.go",
   "internal/assessment/app/request_personalization.go",
   "internal/platform/outbox/relay.go",
   "ADR-004",
   "internal/llmworker/worker.go",
   "internal/platform/idempotency/guard.go",
   "internal/platform/idempotency/schema.sql",
   "internal/platform/idempotency/guard_integration_test.go",
   "internal/assessment/adapter/consumer/results.go",
   "docs/runbook/llm-worker.md"
  ]
 },
 {
  "id": "M3",
  "tier": 3,
  "num": "M3",
  "file": "reel-mekanisme-03-token.html",
  "title": {
   "id": "Token: EdDSA, generasi, kepercayaan antar-service",
   "en": "Tokens: EdDSA, generations, inter-service trust"
  },
  "sub": {
   "id": "Verifikasi lokal, pencabutan lewat penghitung, interceptor di tiap service",
   "en": "Local verification, revocation by counter, an interceptor in every service"
  },
  "order": 14,
  "status": "belum"
 },
 {
  "id": "M4",
  "tier": 3,
  "num": "M4",
  "file": "reel-mekanisme-04-kafka.html",
  "title": {
   "id": "Kafka: kunci, urutan, komit, topic dibuat ulang",
   "en": "Kafka: keys, ordering, commits, recreated topics"
  },
  "sub": {
   "id": "Urutan per partisi, komit manual, Rewinder, EnsureTopics",
   "en": "Per-partition ordering, manual commits, Rewinder, EnsureTopics"
  },
  "order": 15,
  "status": "belum"
 },
 {
  "id": "M5",
  "tier": 3,
  "num": "M5",
  "file": "reel-mekanisme-05-worker.html",
  "title": {
   "id": "Siklus hidup llm-worker",
   "en": "The llm-worker lifecycle"
  },
  "sub": {
   "id": "Klaim, panggilan di luar transaksi, percobaan, parkir kuota, surat mati",
   "en": "Claim, out-of-transaction call, retries, quota parking, dead letters"
  },
  "order": 16,
  "status": "belum"
 },
 {
  "id": "M6",
  "tier": 3,
  "num": "M6",
  "file": "reel-mekanisme-06-saga.html",
  "title": {
   "id": "Saga penghapusan dan kompensasinya",
   "en": "The deletion saga and its compensation"
  },
  "sub": {
   "id": "Daftar peserta sebagai kontrak; menggantung daripada diam-diam",
   "en": "The participant list as a contract; hanging beats silent"
  },
  "order": 17,
  "status": "belum"
 },
 {
  "id": "M7",
  "tier": 3,
  "num": "M7",
  "file": "reel-mekanisme-07-read-model.html",
  "title": {
   "id": "Read-model dasbor",
   "en": "The dashboard read model"
  },
  "sub": {
   "id": "Proyeksi dan posisinya satu transaksi; rebuild kapan saja",
   "en": "Projection and its position in one transaction; rebuild anytime"
  },
  "order": 18,
  "status": "belum"
 },
 {
  "id": "M8",
  "tier": 3,
  "num": "M8",
  "file": "reel-mekanisme-08-observabilitas.html",
  "title": {
   "id": "Observabilitas: trace lewat event",
   "en": "Observability: traces through events"
  },
  "sub": {
   "id": "Span konsumen anak dari permintaan; log ber-trace_id; alert dari SLO",
   "en": "Consumer spans as children of the request; trace_id in logs; alerts from SLOs"
  },
  "order": 19,
  "status": "belum"
 },
 {
  "id": "M9",
  "tier": 3,
  "num": "M9",
  "file": "reel-mekanisme-09-data.html",
  "title": {
   "id": "Siklus hidup data",
   "en": "The data lifecycle"
  },
  "sub": {
   "id": "Migrasi, partisi bulanan, backup dan drill restore, verifikasi penghapusan, data pribadi",
   "en": "Migrations, monthly partitions, backups and restore drills, deletion verification, personal data"
  },
  "order": 20,
  "status": "belum"
 },
 {
  "id": "M10",
  "tier": 3,
  "num": "M10",
  "file": "reel-mekanisme-10-deploy.html",
  "title": {
   "id": "Deploy, jaringan, dan CI/CD",
   "en": "Deploy, networking, and CI/CD"
  },
  "sub": {
   "id": "Sembilan job CI, Helm, k3d, NetworkPolicy, PDB, HPA/KEDA, chaos",
   "en": "Nine CI jobs, Helm, k3d, NetworkPolicy, PDB, HPA/KEDA, chaos"
  },
  "order": 21,
  "status": "belum"
 },
 {
  "id": "M11",
  "tier": 3,
  "num": "M11",
  "file": "reel-mekanisme-11-bukti.html",
  "title": {
   "id": "Bukti dan pengujian",
   "en": "Evidence and testing"
  },
  "sub": {
   "id": "Golden vector, e2e, acceptance D/S, k6 vs SLO, chaos, drill, kontrak",
   "en": "Golden vectors, e2e, D/S acceptance, k6 vs SLO, chaos, drills, contracts"
  },
  "order": 22,
  "status": "belum"
 }
];
const ADRS = [
 {
  "id": "ADR-001",
  "title": "bahasa dan runtime Go",
  "file": "docs/adr/ADR-001",
  "reels": [
   "M11"
  ]
 },
 {
  "id": "ADR-002",
  "title": "topologi 9 unit, profile-svc berdiri sendiri",
  "file": "docs/adr/ADR-002",
  "reels": []
 },
 {
  "id": "ADR-003",
  "title": "message broker Apache Kafka mode KRaft",
  "file": "docs/adr/ADR-003",
  "reels": [
   "M4"
  ]
 },
 {
  "id": "ADR-004",
  "title": "konsistensi: transactional outbox, referensi lunak",
  "file": "docs/adr/ADR-004",
  "reels": [
   "M1",
   "02"
  ]
 },
 {
  "id": "ADR-005",
  "title": "transport gRPC internal, REST di edge, id publik",
  "file": "docs/adr/ADR-005",
  "reels": [
   "01"
  ]
 },
 {
  "id": "ADR-006",
  "title": "database PostgreSQL, schema per service, satu instans",
  "file": "docs/adr/ADR-006",
  "reels": [
   "M9"
  ]
 },
 {
  "id": "ADR-007",
  "title": "user profile id diangkat jadi identitas global",
  "file": "docs/adr/ADR-007",
  "reels": [
   "01",
   "02"
  ]
 },
 {
  "id": "ADR-008",
  "title": "bukti paritas: golden vector dari oracle",
  "file": "docs/adr/ADR-008",
  "reels": [
   "03",
   "M11"
  ]
 },
 {
  "id": "ADR-009",
  "title": "dashboard sebagai read-model, bukan agregator",
  "file": "docs/adr/ADR-009",
  "reels": [
   "08",
   "M7"
  ]
 },
 {
  "id": "ADR-010",
  "title": "deployment k3d lokal dulu, cloud kemudian",
  "file": "docs/adr/ADR-010",
  "reels": [
   "M10"
  ]
 },
 {
  "id": "ADR-011",
  "title": "penghapusan akun sebagai saga dengan kompensasi",
  "file": "docs/adr/ADR-011",
  "reels": [
   "09",
   "M6"
  ]
 },
 {
  "id": "ADR-012",
  "title": "bentuk token: JWT berumur pendek, daftar cabut",
  "file": "docs/adr/ADR-012",
  "reels": [
   "01",
   "M3"
  ]
 },
 {
  "id": "ADR-013",
  "title": "kebijakan port: apa yang direplikasi, diperbaiki, dibuang",
  "file": "docs/adr/ADR-013",
  "reels": [
   "M9"
  ]
 },
 {
  "id": "ADR-014",
  "title": "autoscaling: HPA untuk HTTP, KEDA lag-based untuk worker",
  "file": "docs/adr/ADR-014",
  "reels": [
   "10",
   "11",
   "M10"
  ]
 },
 {
  "id": "ADR-015",
  "title": "pemilihan library: kriteria, bukan popularitas",
  "file": "docs/adr/ADR-015",
  "reels": []
 },
 {
  "id": "ADR-016",
  "title": "bangun seolah produksi, bukan seolah latihan",
  "file": "docs/adr/ADR-016",
  "reels": [
   "M10"
  ]
 },
 {
  "id": "ADR-017",
  "title": "meninjau tiga pilihan dengan data terukur",
  "file": "docs/adr/ADR-017",
  "reels": [
   "M11"
  ]
 },
 {
  "id": "ADR-018",
  "title": "lingkungan kerja: Windows untuk kode, WSL untuk Docker",
  "file": "docs/adr/ADR-018",
  "reels": [
   "M10"
  ]
 },
 {
  "id": "ADR-019",
  "title": "temuan baru saat porting diperbaiki di tempat",
  "file": "docs/adr/ADR-019",
  "reels": [
   "M11"
  ]
 },
 {
  "id": "ADR-020",
  "title": "token EdDSA, pencabutan lewat penghitung generasi",
  "file": "docs/adr/ADR-020",
  "reels": [
   "01",
   "M3"
  ]
 },
 {
  "id": "ADR-021",
  "title": "kontrak identity dikoreksi agar tidak melawan ADR-007",
  "file": "docs/adr/ADR-021",
  "reels": [
   "M3"
  ]
 },
 {
  "id": "ADR-022",
  "title": "RPC profil berkunci user id",
  "file": "docs/adr/ADR-022",
  "reels": [
   "02"
  ]
 },
 {
  "id": "ADR-023",
  "title": "service tidak mempercayai identitas yang dikirimkan",
  "file": "docs/adr/ADR-023",
  "reels": [
   "03"
  ]
 },
 {
  "id": "ADR-024",
  "title": "pemilik adalah pengguna, bukan profilnya",
  "file": "docs/adr/ADR-024",
  "reels": [
   "M3"
  ]
 },
 {
  "id": "ADR-025",
  "title": "kuota penyedia LLM memarkir pekerjaan, bukan mematikannya",
  "file": "docs/adr/ADR-025",
  "reels": [
   "04",
   "11",
   "M5"
  ]
 },
 {
  "id": "ADR-026",
  "title": "setiap service memverifikasi token pengguna sendiri",
  "file": "docs/adr/ADR-026",
  "reels": [
   "01",
   "M3"
  ]
 }
];
const RUNBOOKS = [
 {
  "id": "account-deletion",
  "file": "docs/runbook/account-deletion.md",
  "reels": [
   "09",
   "M6"
  ]
 },
 {
  "id": "assessment-svc",
  "file": "docs/runbook/assessment-svc.md",
  "reels": [
   "03",
   "04"
  ]
 },
 {
  "id": "backfill-nutrition",
  "file": "docs/runbook/backfill-nutrition.md",
  "reels": [
   "07",
   "M9"
  ]
 },
 {
  "id": "backup",
  "file": "docs/runbook/backup.md",
  "reels": [
   "M9"
  ]
 },
 {
  "id": "chat-svc",
  "file": "docs/runbook/chat-svc.md",
  "reels": [
   "06"
  ]
 },
 {
  "id": "coaching-svc",
  "file": "docs/runbook/coaching-svc.md",
  "reels": [
   "05"
  ]
 },
 {
  "id": "dashboard-svc",
  "file": "docs/runbook/dashboard-svc.md",
  "reels": [
   "08",
   "M7"
  ]
 },
 {
  "id": "deploy",
  "file": "docs/runbook/deploy.md",
  "reels": [
   "M10"
  ]
 },
 {
  "id": "edge-gateway",
  "file": "docs/runbook/edge-gateway.md",
  "reels": [
   "01",
   "M3"
  ]
 },
 {
  "id": "identity-svc",
  "file": "docs/runbook/identity-svc.md",
  "reels": [
   "01"
  ]
 },
 {
  "id": "llm-worker",
  "file": "docs/runbook/llm-worker.md",
  "reels": [
   "M5"
  ]
 },
 {
  "id": "nutrition-svc",
  "file": "docs/runbook/nutrition-svc.md",
  "reels": [
   "07"
  ]
 },
 {
  "id": "profile-svc",
  "file": "docs/runbook/profile-svc.md",
  "reels": [
   "02"
  ]
 },
 {
  "id": "rate-limits",
  "file": "docs/runbook/rate-limits.md",
  "reels": [
   "01",
   "04"
  ]
 },
 {
  "id": "restore-drill",
  "file": "docs/runbook/restore-drill.md",
  "reels": [
   "M9",
   "M11"
  ]
 }
];
const DOCS = [
 {
  "id": "RFC-000",
  "title": "Dekomposisi platform",
  "file": "docs/rfc/RFC-000-platform-decomposition.md",
  "reels": []
 },
 {
  "id": "RFC-999",
  "title": "Retrospektif",
  "file": "docs/rfc/RFC-999-retrospective.md",
  "reels": [
   "M11"
  ]
 },
 {
  "id": "FOUNDATION-GATE",
  "title": "Gerbang fondasi",
  "file": "docs/FOUNDATION-GATE.md",
  "reels": [
   "M11"
  ]
 },
 {
  "id": "parity-report",
  "title": "Laporan paritas SCORE2",
  "file": "docs/parity-report.md",
  "reels": [
   "03",
   "M11"
  ]
 },
 {
  "id": "consistency-report",
  "title": "Laporan konsistensi",
  "file": "docs/consistency-report.md",
  "reels": [
   "M11"
  ]
 },
 {
  "id": "performance-report",
  "title": "Laporan kinerja",
  "file": "docs/performance-report.md",
  "reels": [
   "04",
   "10",
   "11"
  ]
 },
 {
  "id": "local-capacity",
  "title": "Kapasitas lokal",
  "file": "docs/local-capacity.md",
  "reels": [
   "M11"
  ]
 },
 {
  "id": "observability",
  "title": "Observabilitas",
  "file": "docs/observability.md",
  "reels": [
   "M8"
  ]
 },
 {
  "id": "topics",
  "title": "Topic Kafka dan partisinya",
  "file": "docs/topics.md",
  "reels": [
   "M4"
  ]
 },
 {
  "id": "db-connections",
  "title": "Koneksi Postgres dan PgBouncer",
  "file": "docs/db-connections.md",
  "reels": [
   "10",
   "M9"
  ]
 },
 {
  "id": "data-handling",
  "title": "Penanganan data pribadi",
  "file": "docs/data-handling.md",
  "reels": [
   "M9"
  ]
 },
 {
  "id": "finops",
  "title": "FinOps",
  "file": "docs/finops.md",
  "reels": [
   "11",
   "M5"
  ]
 }
];
