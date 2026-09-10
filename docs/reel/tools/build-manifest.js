// Membangun manifest.js dari daftar reel di bawah + refs yang dibaca langsung dari berkas reel yang sudah jadi.
// Jalankan dari folder docs/reel:  node tools/build-manifest.js
// Tambah reel baru: tambahkan entri di REELS (status 'belum' → 'jadi' saat berkasnya ada), lalu jalankan lagi.
const fs = require('fs'), path = require('path');
const DOCS = path.resolve(__dirname, '..');
const t = (id, en) => ({ id, en });
const REELS = [
  { id: 'C', tier: 1, num: 'C', file: 'reel-cerita.html', title: t('Sehari bersama Selaras', 'A day with Selaras'), sub: t('Sembilan adegan dari mendaftar sampai menghapus akun, dari sisi pengguna', "Nine scenes from sign-up to account deletion, from the user's side"), mech: [] },
  { id: '01', tier: 2, num: '01', file: 'reel-alur-01-daftar-masuk.html', title: t('Daftar, masuk, dan sesi', 'Sign-up, sign-in, and sessions'), sub: t('JWT EdDSA, generasi token, profil kosong lewat token bootstrap', 'EdDSA JWT, token generation, empty profile via bootstrap token'), mech: ['M3', 'M8'] },
  { id: '02', tier: 2, num: '02', file: 'reel-alur-02-profil.html', title: t('Ubah profil → cache di unit lain', 'Profile update → caches in other units'), sub: t('Outbox, relay at-least-once, salinan yang boleh basi', 'Outbox, at-least-once relay, caches that may go stale'), mech: ['M1', 'M3', 'M4', 'M8'] },
  { id: '03', tier: 2, num: '03', file: 'reel-alur-03-penilaian.html', title: t('Penilaian risiko SCORE2', 'SCORE2 risk assessment'), sub: t('Mesin risiko dengan paritas golden vector; hasil disiarkan ke coaching dan dasbor', 'Risk engine with golden-vector parity; results broadcast to coaching and dashboard'), mech: ['M1', 'M2', 'M3', 'M4', 'M7', 'M8'] },
  { id: '04', tier: 2, num: '04', file: 'reel-alur-04-personalisasi.html', title: t('Personalisasi laporan lewat LLM', 'Report personalisation through the LLM'), sub: t('Jalur permintaan tidak memanggil LLM; worker mengerjakannya, hasil pulang lewat topic bersama', 'The request path never calls the LLM; a worker does, results return over a shared topic'), mech: ['M1', 'M2', 'M3', 'M4', 'M5', 'M8'] },
  { id: '05', tier: 2, num: '05', file: 'reel-alur-05-coaching.html', title: t('Program coaching sampai kelulusan', 'Coaching programme to graduation'), sub: t('Kurikulum LLM, tugas harian, utas pelatih, laporan kelulusan', 'LLM curriculum, daily tasks, coach threads, graduation report'), mech: ['M1', 'M2', 'M3', 'M4', 'M5', 'M7', 'M8'] },
  { id: '06', tier: 2, num: '06', file: 'reel-alur-06-chat.html', title: t('Percakapan dengan asisten', 'Conversation with the assistant'), sub: t('Pesan disimpan, HTTP menjawab segera, balasan tiba lewat llm.results', 'Message stored, HTTP answers at once, reply arrives via llm.results'), mech: ['M1', 'M2', 'M3', 'M4', 'M5', 'M8'] },
  { id: '07', tier: 2, num: '07', file: 'reel-alur-07-nutrisi.html', title: t('Panduan menu harian', 'Daily meal guide'), sub: t('Konteks disimpan di samping hasilnya; bahasa dari cache', 'Context stored next to the result; language from cache'), mech: ['M1', 'M2', 'M3', 'M4', 'M5', 'M8'] },
  { id: '08', tier: 2, num: '08', file: 'reel-alur-08-dasbor.html', title: t('Dasbor sebagai read-model', 'Dashboard as a read model'), sub: t('Proyeksi dari tiga topic, replika baca, pembangunan ulang', 'Projection from three topics, read replica, rebuild'), mech: ['M2', 'M3', 'M4', 'M7', 'M8'] },
  { id: '09', tier: 2, num: '09', file: 'reel-alur-09-hapus-akun.html', title: t('Hapus akun: saga enam unit', 'Account deletion: a six-unit saga'), sub: t('Konfirmasi enam peserta, kompensasi, saga menggantung', 'Six participants confirm, compensation, hanging sagas'), mech: ['M1', 'M2', 'M3', 'M4', 'M6', 'M7', 'M8'] },
  { id: '10', tier: 2, num: '10', file: 'reel-alur-10-beban-http.html', title: t('Autoscaling HTTP: HPA naik lalu turun', 'HTTP autoscaling: HPA up then down'), sub: t('60 VU, CPU 60 %, maks 4/3 replika, batas jujur satu node', '60 VUs, 60 % CPU, max 4/3 replicas, the honest single-node limit'), mech: ['M3', 'M8', 'M10'] },
  { id: '11', tier: 2, num: '11', file: 'reel-alur-11-beban-llm.html', title: t('Autoscaling worker: KEDA dari nol', 'Worker autoscaling: KEDA from zero'), sub: t('Lag dari broker, lagThreshold 5, maks 12 = partisi, cold-start 8,3 s', 'Lag from the broker, lagThreshold 5, max 12 = partitions, 8.3 s cold start'), mech: ['M1', 'M2', 'M4', 'M5', 'M8', 'M10'] },
  { id: 'M1', tier: 3, num: 'M1', file: 'reel-mekanisme-01-outbox.html', title: t('Transactional outbox dan relay', 'Transactional outbox and relay'), sub: t('Duplikat daripada kehilangan: SKIP LOCKED, publish dulu baru tandai', 'Duplicates over loss: SKIP LOCKED, publish first then mark') },
  { id: 'M2', tier: 3, num: 'M2', file: 'reel-mekanisme-02-idempotensi.html', title: t('Idempotensi penerima', 'Consumer idempotency'), sub: t('Satu INSERT yang memutuskan, scope per konsumen, kunci diikat ke pengguna', 'One deciding INSERT, per-consumer scope, key bound to the user') },
  { id: 'M3', tier: 3, num: 'M3', file: 'reel-mekanisme-03-token.html', title: t('Token: EdDSA, generasi, kepercayaan antar-service', 'Tokens: EdDSA, generations, inter-service trust'), sub: t('Verifikasi lokal, pencabutan lewat penghitung, interceptor di tiap service', 'Local verification, revocation by counter, an interceptor in every service') },
  { id: 'M4', tier: 3, num: 'M4', file: 'reel-mekanisme-04-kafka.html', title: t('Kafka: kunci, urutan, komit, topic dibuat ulang', 'Kafka: keys, ordering, commits, recreated topics'), sub: t('Urutan per partisi, komit manual, Rewinder, EnsureTopics', 'Per-partition ordering, manual commits, Rewinder, EnsureTopics') },
  { id: 'M5', tier: 3, num: 'M5', file: 'reel-mekanisme-05-worker.html', title: t('Siklus hidup llm-worker', 'The llm-worker lifecycle'), sub: t('Klaim, panggilan di luar transaksi, percobaan, parkir kuota, surat mati', 'Claim, out-of-transaction call, retries, quota parking, dead letters') },
  { id: 'M6', tier: 3, num: 'M6', file: 'reel-mekanisme-06-saga.html', title: t('Saga penghapusan dan kompensasinya', 'The deletion saga and its compensation'), sub: t('Daftar peserta sebagai kontrak; menggantung daripada diam-diam', 'The participant list as a contract; hanging beats silent') },
  { id: 'M7', tier: 3, num: 'M7', file: 'reel-mekanisme-07-read-model.html', title: t('Read-model dasbor', 'The dashboard read model'), sub: t('Proyeksi dan posisinya satu transaksi; rebuild kapan saja', 'Projection and its position in one transaction; rebuild anytime') },
  { id: 'M8', tier: 3, num: 'M8', file: 'reel-mekanisme-08-observabilitas.html', title: t('Observabilitas: trace lewat event', 'Observability: traces through events'), sub: t('Span konsumen anak dari permintaan; log ber-trace_id; alert dari SLO', 'Consumer spans as children of the request; trace_id in logs; alerts from SLOs') },
  { id: 'M9', tier: 3, num: 'M9', file: 'reel-mekanisme-09-data.html', title: t('Siklus hidup data', 'The data lifecycle'), sub: t('Migrasi, partisi bulanan, backup dan drill restore, verifikasi penghapusan, data pribadi', 'Migrations, monthly partitions, backups and restore drills, deletion verification, personal data') },
  { id: 'M10', tier: 3, num: 'M10', file: 'reel-mekanisme-10-deploy.html', title: t('Deploy, jaringan, dan CI/CD', 'Deploy, networking, and CI/CD'), sub: t('Sembilan job CI, Helm, k3d, NetworkPolicy, PDB, HPA/KEDA, chaos', 'Nine CI jobs, Helm, k3d, NetworkPolicy, PDB, HPA/KEDA, chaos') },
  { id: 'M11', tier: 3, num: 'M11', file: 'reel-mekanisme-11-bukti.html', title: t('Bukti dan pengujian', 'Evidence and testing'), sub: t('Golden vector, e2e, acceptance D/S, k6 vs SLO, chaos, drill, kontrak', 'Golden vectors, e2e, D/S acceptance, k6 vs SLO, chaos, drills, contracts') },
];
// status & refs: dari keberadaan berkas dan isi `refs: [...]` di dalamnya
REELS.forEach((r, i) => {
  r.order = i;
  const p = path.join(DOCS, r.file);
  r.status = fs.existsSync(p) ? 'jadi' : 'belum';
  if (r.status === 'jadi') { const html = fs.readFileSync(p, 'utf8'); r.refs = [...new Set([...html.matchAll(/refs: \[([^\]]*)\]/g)].flatMap(m => m[1].split(',').map(s => s.trim().replace(/^'|'$/g, '')).filter(Boolean)))]; }
});
const ADR_TITLES = {
  'ADR-001': 'bahasa dan runtime Go', 'ADR-002': 'topologi 9 unit, profile-svc berdiri sendiri', 'ADR-003': 'message broker Apache Kafka mode KRaft', 'ADR-004': 'konsistensi: transactional outbox, referensi lunak', 'ADR-005': 'transport gRPC internal, REST di edge, id publik', 'ADR-006': 'database PostgreSQL, schema per service, satu instans', 'ADR-007': 'user profile id diangkat jadi identitas global', 'ADR-008': 'bukti paritas: golden vector dari oracle', 'ADR-009': 'dashboard sebagai read-model, bukan agregator', 'ADR-010': 'deployment k3d lokal dulu, cloud kemudian', 'ADR-011': 'penghapusan akun sebagai saga dengan kompensasi', 'ADR-012': 'bentuk token: JWT berumur pendek, daftar cabut', 'ADR-013': 'kebijakan port: apa yang direplikasi, diperbaiki, dibuang', 'ADR-014': 'autoscaling: HPA untuk HTTP, KEDA lag-based untuk worker', 'ADR-015': 'pemilihan library: kriteria, bukan popularitas', 'ADR-016': 'bangun seolah produksi, bukan seolah latihan', 'ADR-017': 'meninjau tiga pilihan dengan data terukur', 'ADR-018': 'lingkungan kerja: Windows untuk kode, WSL untuk Docker', 'ADR-019': 'temuan baru saat porting diperbaiki di tempat', 'ADR-020': 'token EdDSA, pencabutan lewat penghitung generasi', 'ADR-021': 'kontrak identity dikoreksi agar tidak melawan ADR-007', 'ADR-022': 'RPC profil berkunci user id', 'ADR-023': 'service tidak mempercayai identitas yang dikirimkan', 'ADR-024': 'pemilik adalah pengguna, bukan profilnya', 'ADR-025': 'kuota penyedia LLM memarkir pekerjaan, bukan mematikannya', 'ADR-026': 'setiap service memverifikasi token pengguna sendiri',
};
const ADR_REELS = { 'ADR-001': ['M11'], 'ADR-002': [], 'ADR-003': ['M4'], 'ADR-004': ['M1', '02'], 'ADR-005': ['01'], 'ADR-006': ['M9'], 'ADR-007': ['01', '02'], 'ADR-008': ['03', 'M11'], 'ADR-009': ['08', 'M7'], 'ADR-010': ['M10'], 'ADR-011': ['09', 'M6'], 'ADR-012': ['01', 'M3'], 'ADR-013': ['M9'], 'ADR-014': ['10', '11', 'M10'], 'ADR-015': [], 'ADR-016': ['M10'], 'ADR-017': ['M11'], 'ADR-018': ['M10'], 'ADR-019': ['M11'], 'ADR-020': ['01', 'M3'], 'ADR-021': ['M3'], 'ADR-022': ['02'], 'ADR-023': ['03'], 'ADR-024': ['M3'], 'ADR-025': ['04', '11', 'M5'], 'ADR-026': ['01', 'M3'] };
const ADRS = Object.keys(ADR_TITLES).map(id => ({ id, title: ADR_TITLES[id], file: 'docs/adr/' + id, reels: ADR_REELS[id] || [] }));
const RUNBOOK_REELS = { 'account-deletion': ['09', 'M6'], 'assessment-svc': ['03', '04'], 'backfill-nutrition': ['07', 'M9'], 'backup': ['M9'], 'chat-svc': ['06'], 'coaching-svc': ['05'], 'dashboard-svc': ['08', 'M7'], 'deploy': ['M10'], 'edge-gateway': ['01', 'M3'], 'identity-svc': ['01'], 'llm-worker': ['M5'], 'nutrition-svc': ['07'], 'profile-svc': ['02'], 'rate-limits': ['01', '04'], 'restore-drill': ['M9', 'M11'] };
const RUNBOOKS = Object.keys(RUNBOOK_REELS).map(id => ({ id, file: 'docs/runbook/' + id + '.md', reels: RUNBOOK_REELS[id] }));
const DOCS_LIST = [
  { id: 'RFC-000', title: 'Dekomposisi platform', file: 'docs/rfc/RFC-000-platform-decomposition.md', reels: [] },
  { id: 'RFC-999', title: 'Retrospektif', file: 'docs/rfc/RFC-999-retrospective.md', reels: ['M11'] },
  { id: 'FOUNDATION-GATE', title: 'Gerbang fondasi', file: 'docs/FOUNDATION-GATE.md', reels: ['M11'] },
  { id: 'parity-report', title: 'Laporan paritas SCORE2', file: 'docs/parity-report.md', reels: ['03', 'M11'] },
  { id: 'consistency-report', title: 'Laporan konsistensi', file: 'docs/consistency-report.md', reels: ['M11'] },
  { id: 'performance-report', title: 'Laporan kinerja', file: 'docs/performance-report.md', reels: ['04', '10', '11'] },
  { id: 'local-capacity', title: 'Kapasitas lokal', file: 'docs/local-capacity.md', reels: ['M11'] },
  { id: 'observability', title: 'Observabilitas', file: 'docs/observability.md', reels: ['M8'] },
  { id: 'topics', title: 'Topic Kafka dan partisinya', file: 'docs/topics.md', reels: ['M4'] },
  { id: 'db-connections', title: 'Koneksi Postgres dan PgBouncer', file: 'docs/db-connections.md', reels: ['10', 'M9'] },
  { id: 'data-handling', title: 'Penanganan data pribadi', file: 'docs/data-handling.md', reels: ['M9'] },
  { id: 'finops', title: 'FinOps', file: 'docs/finops.md', reels: ['11', 'M5'] },
];
const out = `// Manifes reel Selaras — dipakai hub (index.html) dan tiap reel (reel.js). Dibangun oleh tools/build-manifest.js.
// status 'jadi' = berkasnya ada dan bisa diputar; 'belum' = direncanakan (lihat plan.html). Teks: { id, en }.
const REELS = ${JSON.stringify(REELS, null, 1)};
const ADRS = ${JSON.stringify(ADRS, null, 1)};
const RUNBOOKS = ${JSON.stringify(RUNBOOKS, null, 1)};
const DOCS = ${JSON.stringify(DOCS_LIST, null, 1)};
`;
fs.writeFileSync(path.join(DOCS, 'manifest.js'), out);
require('vm').runInNewContext(out, {});
console.log(`manifest.js: ${REELS.length} reel (${REELS.filter(r => r.status === 'jadi').length} jadi), ${ADRS.length} ADR, ${RUNBOOKS.length} runbook, ${DOCS_LIST.length} dokumen`);
