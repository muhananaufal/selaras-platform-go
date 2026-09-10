// Validator reel: jalankan dari folder docs/reel:  node tools/validate-reel.js reel-mekanisme-01-outbox.html [path repo]
// Memeriksa: sintaks blok data, rujukan edge/payload/blok/fx/kode/cabang, baris kode yang disorot benar-benar ada,
// nama test (proof) ada di repo, refs menunjuk berkas/ADR yang ada, kesinambungan titik, dan simulasi mut + ZOOM tiap urutan.
const fs = require('fs'), path = require('path'), vm = require('vm');
const DOCS = path.resolve(__dirname, '..');
const file = process.argv[2]; const REPO = process.argv[3] || path.resolve(__dirname, '../../..');
const html = fs.readFileSync(path.join(DOCS, file), 'utf8');
const m = /<script>([\s\S]*?)<\/script>/.exec(html); if (!m) { console.error('blok data tidak ditemukan'); process.exit(1); }
const ctx = { console };
vm.createContext(ctx);
try { vm.runInContext(m[1] + '\nthis.__ = { REEL, CODE, NODES, RAILS, EDGES, PANEL, STEPS, BRANCHES, CHAPTERS, PAYLOAD, ZOOM, initial, BOX };', ctx); }
catch (e) { console.error('sintaks/eval gagal:', e.message); process.exit(1); }
const { REEL, CODE, NODES, RAILS, EDGES, PANEL, STEPS, BRANCHES, PAYLOAD, ZOOM, initial, BOX } = ctx.__;
const errs = [], warns = [];
const E = (s) => errs.push(s);
const strip = s => s.replace(/<[^>]+>/g, '');
// test names di repo
let testNames = null;
try { testNames = new Set(); const walk = d => { for (const f of fs.readdirSync(d, { withFileTypes: true })) { const p = path.join(d, f.name); if (f.isDirectory()) { if (!/node_modules|\.git|gen/.test(f.name)) walk(p); } else if (f.name.endsWith('_test.go')) { for (const mm of fs.readFileSync(p, 'utf8').matchAll(/^func (Test[A-Za-z0-9_]+)/gm)) testNames.add(mm[1]); } } }; walk(path.join(REPO, 'internal')); walk(path.join(REPO, 'cmd')); } catch (_) { warns.push('repo tidak terbaca; proof tidak diperiksa'); testNames = null; }
const refOk = r => /^ADR-\d{3}$/.test(r) ? fs.existsSync(path.join(REPO, 'docs/adr')) && fs.readdirSync(path.join(REPO, 'docs/adr')).some(f => f.startsWith(r)) : fs.existsSync(path.join(REPO, r));
const seqOf = name => { if (!name) return STEPS.slice(); const b = BRANCHES[name]; let k = b.after != null ? b.after : STEPS.findIndex(s => s.branch === true || (Array.isArray(s.branch) && s.branch.includes(name))); return b.resume ? [...STEPS.slice(0, k + 1), ...b.steps, ...STEPS.slice(k + 1)] : [...STEPS.slice(0, k + 1), ...b.steps]; };
const checkStep = (s, where) => {
  const at = s.at; if (!at) E(`${where}: tanpa at`);
  else if (at.startsWith('rail-') ? !RAILS[at.slice(5)] : !NODES[at]) E(`${where}: at '${at}' tidak ada`);
  const trips = [...(s.travel || []), ...(s.after || []), ...(s.proc || []).flatMap(p => typeof p === 'string' ? [] : (p.go || []))];
  trips.forEach(t => { if (!EDGES[t.e]) E(`${where}: edge ${t.e} tidak ada`); if (!PAYLOAD[t.p]) E(`${where}: payload ${t.p} tidak ada`); });
  (s.proc || []).forEach((p, i) => { if (typeof p === 'string') return; if (p.b != null) { if (!PANEL[at]) E(`${where} proc ${i}: b=${p.b} tetapi ${at} tanpa PANEL`); else if (p.b >= PANEL[at].blocks.length) E(`${where} proc ${i}: b=${p.b} di luar blok`); }
    (p.fx || []).forEach(f => { const [k, a] = f.split(':'); if (['slot', 'busy'].includes(k) && !RAILS[a]) E(`${where}: fx ${f} rel tidak ada`); if (['row', 'unrow', 'tick', 'flash', 'off', 'on', 'shake'].includes(k) && !NODES[a]) E(`${where}: fx ${f} node tidak ada`); if (!['slot', 'busy', 'row', 'unrow', 'tick', 'flash', 'off', 'on', 'shake'].includes(k)) E(`${where}: fx ${f} tidak dikenal`); }); });
  if (s.code && !CODE[s.code]) E(`${where}: code '${s.code}' tidak ada`);
  if (s.lines && s.code && CODE[s.code]) { const rows = CODE[s.code].src.split('\n'); const hit = Array.isArray(s.lines) ? rows.length >= (s.lines[1] || s.lines[0]) : rows.some(r => strip(r).includes(s.lines)); if (!hit) E(`${where}: lines '${s.lines}' tidak cocok di CODE.${s.code}`); }
  if (s.zoom && !ZOOM[s.zoom]) E(`${where}: zoom '${s.zoom}' tidak ada`);
  if (s.branch && Array.isArray(s.branch)) s.branch.forEach(n => { if (!BRANCHES[n]) E(`${where}: cabang '${n}' tidak ada`); });
  if (s.proof && testNames) [].concat(s.proof).forEach(p => { if (!testNames.has(p)) E(`${where}: proof ${p} tidak ada di repo`); });
  (s.refs || []).forEach(r => { if (!refOk(r)) warns.push(`${where}: ref '${r}' tidak ditemukan di repo`); });
  if (!s.title || !s.text) E(`${where}: title/text kosong`);
};
STEPS.forEach((s, i) => checkStep(s, `STEPS[${i}]`));
Object.entries(BRANCHES).forEach(([n, b]) => { if (!b.label) E(`BRANCHES.${n}: tanpa label`); const k = b.after != null ? b.after : STEPS.findIndex(s => s.branch === true || (Array.isArray(s.branch) && s.branch.includes(n))); if (k < 0) E(`BRANCHES.${n}: tidak dipasang di langkah mana pun`); b.steps.forEach((s, i) => checkStep(s, `BRANCHES.${n}[${i}]`)); });
// kesinambungan titik + simulasi
const endOf = t => t.rev ? EDGES[t.e].a : EDGES[t.e].b, startOf = t => t.rev ? EDGES[t.e].b : EDGES[t.e].a;
[null, ...Object.keys(BRANCHES)].forEach(name => {
  const seq = seqOf(name); let pos = null; const S = initial(); let order = [];
  seq.forEach((s, i) => {
    const where = `${name || 'utama'}[${i}] "${s.title}"`;
    if (s.jump) pos = null;
    const trips = [...(s.travel || []), ...(s.proc || []).flatMap(p => typeof p === 'string' ? [] : (p.go || [])), ...(s.after || [])];
    const before = pos; if (s.bg) pos = null;
    trips.forEach(t => { if (!EDGES[t.e]) return; if (t.jump) pos = null; const from = startOf(t); if (pos && from !== pos) E(`${where}: titik berangkat dari ${from} padahal terakhir di ${pos}`); pos = endOf(t); });
    if (s.bg) pos = before; // pelaku latar (relay kedua, dsb.) punya titiknya sendiri; titik utama tidak berpindah
    if (!trips.length && pos && s.at !== pos && !s.jump) warns.push(`${where}: at ${s.at} sedangkan titik terakhir di ${pos} (tanpa perjalanan)`);
    try { s.mut(S); } catch (e) { E(`${where}: mut gagal: ${e.message}`); }
    S.shown.forEach(id => { if (!order.includes(id)) order.push(id); if (id.startsWith('rail-') ? !RAILS[id.slice(5)] : !NODES[id]) E(`${where}: show '${id}' tidak ada`); });
    S.edges.forEach(id => { if (!EDGES[id]) E(`${where}: edge state '${id}' tidak ada`); });
    if (S.focus && !(S.focus.startsWith('rail-') ? RAILS[S.focus.slice(5)] : NODES[S.focus])) E(`${where}: focus '${S.focus}' tidak ada`);
    if (S.focus) { try { BOX(S.focus); } catch (e) { E(`${where}: BOX(${S.focus}) gagal: ${e.message}`); } }
    if (s.zoom && ZOOM[s.zoom]) { try { const [t, h] = ZOOM[s.zoom](S); if (typeof t !== 'string' || typeof h !== 'string') E(`${where}: ZOOM.${s.zoom} tidak mengembalikan [judul, html]`); } catch (e) { E(`${where}: ZOOM.${s.zoom} gagal: ${e.message}`); } }
    // node yang menjadi at harus sudah tampil setelah mut
    if (s.at && !S.shown.includes(s.at)) E(`${where}: at '${s.at}' belum ditampilkan (show) sampai langkah ini`);
  });
  if (!S.outcome) warns.push(`${name || 'utama'}: S.outcome kosong di akhir urutan`);
  console.log(`${name || 'utama'}: ${seq.length} langkah, ${order.length} komponen tampil`);
});
// kanvas: node dalam viewBox
Object.entries(NODES).forEach(([id, n]) => { if (n.x < 80 || n.x > 1520 || n.y < 80 || n.y > 840) warns.push(`node ${id} dekat tepi kanvas (${n.x},${n.y})`); });
warns.forEach(w => console.log('  peringatan: ' + w));
errs.forEach(e => console.log('  GALAT: ' + e));
console.log(`${REEL.id}: ${errs.length} galat, ${warns.length} peringatan`);
process.exit(errs.length ? 1 : 0);
