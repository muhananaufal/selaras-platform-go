// Penonton otomatis: Chrome headless dikendalikan lewat protokol DevTools (tanpa dependensi), memutar satu reel dari kartu pembuka
// sampai penutup untuk jalur utama dan setiap cabang, memeriksa invarian tampilan tiap langkah, menyimpan tangkapan layar, dan
// menulis laporan. Jalankan dari folder docs/reel:
//   node tools/tonton.js reel-mekanisme-01-outbox.html [folder-keluaran]
// Invarian yang diperiksa tiap 400 ms: keterangan (gelembung) di dalam kanvas dan di kiri kartu Rincian, teks keterangan tidak lebih
// lebar dari gelembungnya, label blok panel muat, subjek fokus di dalam kanvas, kartu Rincian dan caption tanpa luapan horizontal,
// tidak ada galat konsol/eksepsi. Keluar 0 bila semua urutan mencapai penutup tanpa temuan.
const { spawn } = require('child_process');
const fs = require('fs'), path = require('path'), os = require('os');
const DOCS = path.resolve(__dirname, '..');
const reel = process.argv[2]; if (!reel) { console.error('sebutkan berkas reel'); process.exit(2); }
const OUT = process.argv[3] || path.join(os.tmpdir(), 'selaras-tonton', reel.replace(/\.html$/, ''));
fs.mkdirSync(OUT, { recursive: true });
const CHROME = process.env.CHROME || 'C:/Program Files/Google/Chrome/Application/chrome.exe';
const URL_BASE = 'file:///' + DOCS.replace(/\\/g, '/') + '/' + reel;
const sleep = ms => new Promise(r => setTimeout(r, ms));

const launch = () => new Promise((res, rej) => {
  const prof = fs.mkdtempSync(path.join(os.tmpdir(), 'tonton-prof-'));
  const p = spawn(CHROME, ['--headless=new', '--disable-gpu', '--hide-scrollbars', '--window-size=1600,900', '--remote-debugging-port=0', '--user-data-dir=' + prof, '--no-first-run', '--autoplay-policy=no-user-gesture-required', 'about:blank']);
  let buf = ''; const onData = d => { buf += d.toString(); const m = /DevTools listening on (ws:\/\/[^\s]+)/.exec(buf); if (m) { p.stderr.off('data', onData); res({ proc: p, ws: m[1] }); } };
  p.stderr.on('data', onData); p.on('exit', c => rej(new Error('chrome keluar lebih dulu: ' + c)));
  setTimeout(() => rej(new Error('chrome tidak memberi alamat DevTools')), 15000);
});

class CDP {
  constructor(ws) { this.ws = ws; this.id = 0; this.pending = new Map(); this.handlers = {}; }
  static async open(url) { const c = new CDP(new WebSocket(url)); await new Promise((r, j) => { c.ws.onopen = r; c.ws.onerror = j; }); c.ws.onmessage = ev => { const m = JSON.parse(ev.data); if (m.id && c.pending.has(m.id)) { const { res, rej } = c.pending.get(m.id); c.pending.delete(m.id); m.error ? rej(new Error(m.error.message)) : res(m.result); } else if (m.method && c.handlers[m.method]) c.handlers[m.method](m.params); }; return c; }
  send(method, params = {}) { const id = ++this.id; this.ws.send(JSON.stringify({ id, method, params })); return new Promise((res, rej) => this.pending.set(id, { res, rej })); }
  on(method, fn) { this.handlers[method] = fn; }
  async eval(expr) { const r = await this.send('Runtime.evaluate', { expression: expr, returnByValue: true, awaitPromise: true }); if (r.exceptionDetails) throw new Error('eval: ' + (r.exceptionDetails.exception && r.exceptionDetails.exception.description || r.exceptionDetails.text)); return r.result.value; }
}

// Dijalankan di dalam halaman: keadaan + daftar masalah tampilan saat ini.
const PROBE = `(() => {
  const $ = s => document.querySelector(s), $$ = s => [...document.querySelectorAll(s)];
  const cv = $('#cv').getBoundingClientRect(), top = $('.top') ? $('.top').getBoundingClientRect() : { bottom: 0 };
  const det = $('#detail'), detOpen = det && det.classList.contains('open'), dr = detOpen ? det.getBoundingClientRect() : null;
  const problems = [];
  $$('.call.in').forEach((g, i) => { const r = g.querySelector('.bub rect') || g.querySelector('rect'); if (!r) return; const b = r.getBoundingClientRect(); const txt = g.textContent.trim().slice(0, 50);
    if (b.left < cv.left - 1 || b.right > cv.right + 1) problems.push('keterangan keluar kanvas kiri/kanan: "' + txt + '"');
    if (b.top < top.bottom - 1) problems.push('keterangan tertutup bilah atas: "' + txt + '"');
    if (dr && b.right > dr.left + 1 && b.bottom > dr.top && b.top < dr.bottom) problems.push('keterangan tertutup kartu Rincian: "' + txt + '"');
    const w = +r.getAttribute('width'); g.querySelectorAll('tspan, text').forEach(t => { if (t.tagName === 'text' && t.querySelector('tspan')) return; const len = t.getComputedTextLength(); if (len > w - 4) problems.push('teks keterangan lebih lebar dari gelembung (' + Math.round(len) + ' > ' + Math.round(w) + '): "' + txt + '"'); }); });
  $$('.blk').forEach(bg => { const rect = bg.querySelector('rect'); const w = +rect.getAttribute('width'); bg.querySelectorAll('text').forEach(t => { if (t.getComputedTextLength() > w - 4) problems.push('label blok keluar blok: "' + t.textContent + '"'); }); });
  const moving = (() => { const d = $('.dot'); return d && parseFloat(getComputedStyle(d).opacity) > 0; })(); // juga saat kartu bab terbuka: keadaan langkah sebelumnya masih tampil
  const settling = moving || $('#chapter').classList.contains('open') || document.body.classList.contains('ended'); // titik sedang berjalan: kelas focus masih milik langkah sebelumnya
  const f = $('.node.focus'); if (f && f.classList.contains('shown') && !settling) { const b = f.querySelector('.box').getBoundingClientRect(); if (b.left < cv.left - 1 || b.right > cv.right + 1 || b.top < top.bottom - 1) problems.push('subjek fokus keluar pandangan: ' + f.querySelector('.lbl').textContent); if (dr && b.right > dr.left + 1 && b.top < dr.bottom) problems.push('subjek fokus tertutup kartu Rincian: ' + f.querySelector('.lbl').textContent); }
  if (detOpen) { const zb = $('#z-body'); if (zb && zb.scrollWidth > zb.clientWidth + 1) problems.push('kartu Rincian meluap horizontal (' + zb.scrollWidth + ' > ' + zb.clientWidth + ')');
    $$('#z-body .tbl .row span, #z-body .kv span, #z-body .list span').forEach(s => { if (s.scrollWidth > s.clientWidth + 1) problems.push('teks kartu keluar kolom: "' + s.textContent.trim().slice(0, 40) + '"'); }); }
  const ct = $('#c-title'); if (ct && ct.scrollWidth > ct.clientWidth + 1) problems.push('judul caption meluap');
  const idx = (() => { const m = /(\\d+)\\s*\\/\\s*(\\d+)/.exec(($('#c-n') || {}).textContent || ''); return m ? { i: +m[1], n: +m[2] } : null; })();
  return { hash: location.hash, idx, title: (ct || {}).textContent || '', ending: $('#ending').classList.contains('open'), choice: $('#choice').classList.contains('open'), intro: $('#intro').classList.contains('open'), playing: ($('#b-play') || {}).textContent !== '▶', problems };
})()`;

(async () => {
  const { proc, ws } = await launch();
  const targets = await (await fetch(ws.replace(/^ws:\/\/([^/]+).*/, 'http://$1/json'))).json();
  const page = targets.find(t => t.type === 'page');
  const cdp = await CDP.open(page.webSocketDebuggerUrl);
  await cdp.send('Page.enable'); await cdp.send('Runtime.enable'); await cdp.send('Log.enable');
  const consoleErrs = [];
  cdp.on('Runtime.exceptionThrown', p => consoleErrs.push('eksepsi: ' + (p.exceptionDetails.exception && p.exceptionDetails.exception.description || p.exceptionDetails.text).split('\n')[0]));
  cdp.on('Runtime.consoleAPICalled', p => { if (p.type === 'error') consoleErrs.push('console.error: ' + p.args.map(a => a.value || a.description).join(' ')); });
  cdp.on('Log.entryAdded', p => { if (p.entry.level === 'error' && !/favicon|fonts\.g/.test(p.entry.text)) consoleErrs.push('log: ' + p.entry.text); });
  const shot = async name => { const r = await cdp.send('Page.captureScreenshot', { format: 'png' }); fs.writeFileSync(path.join(OUT, name + '.png'), Buffer.from(r.data, 'base64')); };

  const report = { reel, runs: [], consoleErrs };
  const watch = async (label, start) => {
    const run = { label, steps: [], problems: [], reachedEnding: false, stalled: false, seconds: 0 };
    report.runs.push(run);
    await start();
    const t0 = Date.now(); let lastIdx = null, lastChange = Date.now(), shotDue = null, prevProblems = new Set();
    while (Date.now() - t0 < 8 * 60 * 1000) {
      await sleep(400);
      let s; try { s = await cdp.eval(PROBE); } catch (e) { run.problems.push('probe gagal: ' + e.message); break; }
      const key = s.idx ? s.hash + ':' + s.idx.i : s.hash;
      if (key !== lastIdx) { lastIdx = key; lastChange = Date.now(); run.steps.push({ hash: s.hash, i: s.idx && s.idx.i, n: s.idx && s.idx.n, title: s.title.trim(), at: Math.round((Date.now() - t0) / 1000) }); shotDue = Date.now() + 1800; }
      if (shotDue && Date.now() >= shotDue) { shotDue = null; await shot(`${label}-${String(s.idx ? s.idx.i : 0).padStart(2, '0')}`); }
      // masalah dicatat hanya bila bertahan dua sampel berturut-turut (800 ms): kamera yang sedang bergerak 650–700 ms bukan temuan
      const now = new Set(s.problems); s.problems.forEach(p => { if (!prevProblems.has(p)) return; const tag = `[${label} langkah ${s.idx ? s.idx.i : '?'}] ${p}`; if (!run.problems.includes(tag)) { run.problems.push(tag); shot(`${label}-masalah-${run.problems.length}`).catch(() => {}); } }); prevProblems = now;
      if (s.ending) { run.reachedEnding = true; await shot(`${label}-penutup`); break; }
      if (Date.now() - lastChange > 75000) { run.stalled = true; run.problems.push(`[${label}] macet ${Math.round((Date.now() - lastChange) / 1000)} s di ${s.hash} (playing=${s.playing}, choice=${s.choice}, intro=${s.intro})`); await shot(`${label}-macet`); break; }
    }
    run.seconds = Math.round((Date.now() - t0) / 1000);
    console.log(`${label}: ${run.steps.length} langkah, ${run.seconds} s, penutup=${run.reachedEnding}, masalah=${run.problems.length}`);
  };

  // jalur utama: muat tanpa hash → kartu pembuka → putar otomatis
  await watch('utama', async () => { await cdp.send('Page.navigate', { url: URL_BASE }); await sleep(1500); await cdp.eval(`document.querySelector('#intro').click(); true`); });
  const branches = await cdp.eval(`Object.keys(BRANCHES)`);
  for (const b of branches) {
    await watch(b, async () => { const ok = await cdp.eval(`(() => { const btn = document.querySelector('#ending [data-restart="${b}"]'); if (!btn) return false; btn.click(); return true; })()`); if (!ok) { await cdp.send('Page.navigate', { url: URL_BASE + '#' + b + '/1' }); await sleep(1500); await cdp.eval(`document.querySelector('#b-play').click(); true`); } });
  }
  try { cdp.ws.close(); } catch (_) {} proc.kill();
  const total = report.runs.reduce((a, r) => a + r.problems.length, 0) + consoleErrs.length;
  fs.writeFileSync(path.join(OUT, 'laporan.json'), JSON.stringify(report, null, 2));
  const md = [`# Tontonan otomatis · ${reel}`, '', `Keluaran: ${OUT}`, ''];
  report.runs.forEach(r => { md.push(`## ${r.label}: ${r.steps.length} langkah · ${r.seconds} s · ${r.reachedEnding ? 'sampai penutup' : r.stalled ? 'MACET' : 'tidak selesai'}`); r.steps.forEach(s => md.push(`- ${s.at}s · ${s.hash} · ${s.title}`)); if (r.problems.length) { md.push('', '**Temuan:**'); r.problems.forEach(p => md.push('- ' + p)); } md.push(''); });
  if (consoleErrs.length) { md.push('## Galat konsol'); [...new Set(consoleErrs)].forEach(e => md.push('- ' + e)); }
  fs.writeFileSync(path.join(OUT, 'laporan.md'), md.join('\n'));
  console.log(`\nSelesai: ${report.runs.length} urutan, ${total} temuan. Laporan: ${path.join(OUT, 'laporan.md')}`);
  process.exit(total ? 1 : 0);
})().catch(e => { console.error('gagal:', e.message); process.exit(2); });
