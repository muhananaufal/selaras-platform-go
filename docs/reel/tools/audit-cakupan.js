// Audit cakupan: inventarisasi repo Go, lalu cocokkan tiap butir dengan teks rencana dan manifes.
// Jalankan dari folder docs/reel:  node tools/audit-cakupan.js [path repo]
// Keluaran: daftar butir repo yang belum disebut di plan.html maupun manifest.js.
const fs = require('fs'), path = require('path'), cp = require('child_process');
const DOCS = path.resolve(__dirname, '..');
const REPO = process.argv[2] || path.resolve(__dirname, '../../..');
const text = (fs.readFileSync(path.join(DOCS, 'plan.html'), 'utf8') + fs.readFileSync(path.join(DOCS, 'manifest.js'), 'utf8')).toLowerCase();
const ls = d => { try { return fs.readdirSync(path.join(REPO, d)); } catch (_) { return []; } };
const grep = (file, re) => { try { return [...fs.readFileSync(path.join(REPO, file), 'utf8').matchAll(re)].map(m => m[1]); } catch (_) { return []; } };
const inv = {
  cmd: ls('cmd'), internal: ls('internal'), platform: ls('internal/platform'),
  rpc: ls('api/proto').flatMap(s => grep(`api/proto/${s}/v1/${s}.proto`, /rpc ([A-Za-z]+)/g)),
  route: grep('internal/edge/router.go', /\.(?:GET|POST|PUT|PATCH|DELETE)\("([^"]+)"/g),
  topic: grep('internal/platform/kafka/topics.go', /Name:\s*"([a-z.]+)"/g),
  adr: ls('docs/adr').filter(f => f.startsWith('ADR-')).map(f => f.slice(0, 7)),
  runbook: ls('docs/runbook'), docs: [...ls('docs'), ...ls('docs/rfc')].filter(f => f.endsWith('.md')),
  deploy: [...ls('deploy/compose'), ...ls('deploy/helm/selaras/templates'), ...ls('deploy/k3d'), ...ls('deploy/compose/observability')].filter(f => /\.|sh$/.test(f)),
  ci: grep('.github/workflows/ci.yml', /^  ([a-z][a-z -]*):$/gm).filter(j => j !== 'push'), workflows: ls('.github/workflows'), test: ls('test'),
};
const alias = { 'edge-gateway': 'gateway', 'llm-worker': 'worker' };
let total = 0, missing = 0;
for (const [k, items] of Object.entries(inv)) {
  const miss = items.filter(it => { const n = it.toLowerCase().replace(/\/$/, ''); const c = [n, alias[n] || '', n.replace(/\.md$/, ''), n.replace(/\.ya?ml$/, ''), n.replace(/\.sh$/, ''), n.replace(/-svc$/, ''), n.replace(/-/g, ' ')].filter(x => x.length >= 2); return !c.some(x => text.includes(x)); });
  total += items.length; missing += miss.length;
  console.log(`${k}: ${items.length} butir, belum disebut ${miss.length}${miss.length ? ' → ' + miss.join(' | ') : ''}`);
}
console.log(`\nTotal ${total} butir, ${missing} belum disebut. (Nama RPC/rute dipetakan per perilaku di bagian "Peta cakupan" rencana; cek tabel itu untuk butir yang tersisa.)`);
