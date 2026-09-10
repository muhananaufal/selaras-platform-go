// Selaras Flow Reel — mesin bersama. Tiap reel HTML hanya berisi data (REEL, CODE, NODES, EDGES, RAILS, PANEL, STEPS, BRANCHES, CHAPTERS, PAYLOAD, ZOOM)
// dan memuat manifest.js lalu reel.js. Dokumentasi: blueprint.md.
// Teks data boleh string (Bahasa Indonesia) atau { id, en }; T() memilih sesuai bahasa yang dipilih pengguna.
const LANG = (() => { try { return localStorage.getItem('selaras-lang') || 'id'; } catch (_) { return 'id'; } })();
const T = v => (v && typeof v === 'object' && !Array.isArray(v) && 'id' in v) ? (v[LANG] || v.id) : v;
const SKELETON = () => `<div class="app">
  <div class="canvas" id="canvas"><svg id="cv" viewBox="0 0 1600 900" role="img" aria-label="alur personalisasi laporan dari klien sampai kembali"></svg><div class="vignette"></div></div>
  <div class="chapter" id="chapter"><div><div class="n" id="ch-n">BAB 1</div><h2 id="ch-title">Permintaan</h2><p id="ch-sub"></p><div class="bar"></div></div></div>
  <div class="credits" id="credits"><div class="roll" id="roll"></div><button class="btn skip" id="credits-skip">Lewati</button></div>

  <div class="top">
    <div class="title"><b>Selaras Flow Reel</b><span>${T(REEL.num)} · ${T(REEL.title)}</span></div>
    <div class="timeline" id="timeline" role="slider" aria-label="posisi langkah"><div class="track"></div><div class="fill" id="fill"></div></div>
    <div class="right">
      <span class="time" id="time">0:00 / 0:00</span>
      <button class="btn primary" id="b-play" title="putar / jeda (spasi) · ← → langkah">▶</button>
      <div class="more">
        <button class="btn" id="b-more" aria-expanded="false" title="menu">⋯</button>
        <div class="menu" id="menu">
          <button class="btn" id="b-prev" title="mundur (←)">◀ Mundur</button>
          <button class="btn" id="b-next" title="maju (→)">Maju ▶</button>
          <hr>
          <button class="btn" id="b-detail" aria-pressed="false">Rincian <kbd>R</kbd></button>
          <button class="btn" id="b-code" aria-pressed="false">Kode asli <kbd>K</kbd></button>
          <button class="btn" id="b-sfx" aria-pressed="true">Efek bunyi <kbd>S</kbd></button>
          <button class="btn" id="b-voice" aria-pressed="false">Narasi <kbd>N</kbd></button>
          <button class="btn" id="b-music" aria-pressed="true">Musik <kbd>M</kbd></button>
          <button class="btn" id="b-lang" title="ganti bahasa (L)">${LANG === 'id' ? 'English' : 'Bahasa Indonesia'} <kbd>L</kbd></button>
          <a class="btn" href="index.html">Hub ↗</a>
          <button class="btn" id="b-theme">Tema <kbd>D</kbd></button>
          <a class="btn" href="runtime.html">Peta sistem ↗</a>
        </div>
      </div>
    </div>
  </div>

  <div class="toast" id="toast"></div>
  <div class="intro" id="intro"><div><div class="e">${T(REEL.kicker)}</div><h2>${T(REEL.title)}</h2><p>${T(REEL.intro)}</p></div></div>
  <div class="choice" id="choice"><div><span class="q">${LANG === 'en' ? 'What if…?' : 'Bagaimana kalau…?'}</span><span id="choice-opts"></span><button data-branch="">${LANG === 'en' ? 'Continue' : 'Lanjut normal'}<span class="cd" id="choice-ring" style="--p:100"></span><span id="choice-cd">8</span></button></div></div>
  <div class="ending" id="ending"><div>
    <div class="e" id="end-branch">jalur normal</div>
    <h2>Alur selesai</h2>
    <p id="end-outcome"></p>
    <div class="stats">
      <div><b id="end-u">0 ms</b><span>pengguna menunggu</span></div>
      <div><b id="end-s">0 ms</b><span>sistem bekerja</span></div>
      <div><b id="end-tx">0</b><span>transaksi DB</span></div>
      <div><b id="end-msg">0</b><span>pesan Kafka</span></div>
      <div><b id="end-comp">0</b><span>komponen</span></div>
    </div>
    <div class="acts">
      <button class="btn primary" id="end-replay">↺ Putar ulang</button>
      <span id="end-branches"></span>
      <button class="btn" id="end-credits">Kredit</button>
      <a class="btn" href="index.html">↗ Hub</a><a class="btn" id="end-next" href="#" hidden>Reel berikutnya ▶</a>
    </div>
  </div></div>
  <div class="detail" id="detail"><h4 id="z-title">Rincian</h4><div id="z-body"></div><div class="code" id="code"><div class="file" id="code-file"></div><pre id="code-pre"></pre></div></div>

  <div class="bottom"><div class="cap" id="cap">
    <div class="n"><span class="lt" id="lt"><b id="lt-n">Bab 1</b> · <span id="lt-title">Permintaan</span></span><span class="sep">·</span><span id="c-n"></span><span class="sep">·</span><span class="lat" id="lat" title="ilustratif berskala dari docs/performance-report.md"><b id="lat-u">0 ms</b> <i>pengguna menunggu</i> <b id="lat-s">0 ms</b> <i>sistem</i></span></div>
    <h2 id="c-title">—</h2>
    <p id="c-text"></p>
    <div class="refs" id="c-refs"></div>
  </div></div>
</div>`;
// ---------- MESIN ----------
(() => {
  // kerangka DOM dibangun dari REEL (data reel) supaya tiap reel hanya berisi data
  document.body.insertAdjacentHTML('afterbegin', SKELETON());
  document.body.classList.add('tier-' + (REEL.tier || 2));
  document.title = T(REEL.title) + ' · Selaras Flow Reel';
  const NS = 'http://www.w3.org/2000/svg';
  const svg = document.getElementById('cv');
  const $ = s => document.querySelector(s);
  const reduced = window.matchMedia('(prefers-reduced-motion: reduce)').matches;
  const el = (tag, attrs, parent) => { const n = document.createElementNS(NS, tag); for (const [k, v] of Object.entries(attrs)) n.setAttribute(k, v); (parent || svg).appendChild(n); return n; };
  const ICON = {
    phone: g => { el('rect', { class: 'ic', x: -16, y: -30, width: 32, height: 60, rx: 6 }, g); el('line', { class: 'ic', x1: -6, y1: 22, x2: 6, y2: 22 }, g); },
    gate: g => { el('path', { class: 'ic', d: 'M-26,-22 L0,-32 L26,-22 V6 C26,20 12,30 0,34 C-12,30 -26,20 -26,6 Z' }, g); el('path', { class: 'ic', d: 'M-10,2 L-3,9 L11,-7' }, g); },
    svc: g => { el('path', { class: 'ic', d: 'M0,-32 L28,-16 V16 L0,32 L-28,16 V-16 Z' }, g); el('circle', { class: 'ic', r: 9 }, g); el('circle', { class: 'icf', r: 3 }, g); },
    db: g => { g.classList.add('cyl'); el('path', { class: 'side', d: 'M-28,-20 V22 A28,10 0 0 0 28,22 V-20' }, g); el('ellipse', { class: 'cap', cx: 0, cy: -20, rx: 28, ry: 10 }, g); el('rect', { class: 'row r1', x: -18, y: -4, width: 36, height: 5, rx: 2 }, g); el('rect', { class: 'row r2', x: -18, y: 6, width: 36, height: 5, rx: 2 }, g); },
    redis: g => { [-14, 0, 14].forEach((y, i) => el('rect', { class: 'ic', x: -26, y: y - 6, width: 52, height: 12, rx: 3, style: `opacity:${1 - i * 0.25}` }, g)); },
    worker: g => { el('rect', { class: 'ic', x: -24, y: -18, width: 48, height: 40, rx: 8 }, g); el('circle', { class: 'icf', cx: -9, cy: 0, r: 3.5 }, g); el('circle', { class: 'icf', cx: 9, cy: 0, r: 3.5 }, g); el('line', { class: 'ic', x1: 0, y1: -18, x2: 0, y2: -30 }, g); el('circle', { class: 'icf', cy: -32, r: 3 }, g); el('line', { class: 'ic', x1: -8, y1: 12, x2: 8, y2: 12 }, g); },
    gemini: g => { el('path', { class: 'ic', d: 'M0,-32 C2,-12 12,-2 32,0 C12,2 2,12 0,32 C-2,12 -12,2 -32,0 C-12,-2 -2,-12 0,-32 Z' }, g); },
    // relay: dua roda dan sabuk pengangkut
    relay: g => { el('circle', { class: 'ic', cx: -18, cy: 8, r: 9 }, g); el('circle', { class: 'ic', cx: 18, cy: 8, r: 9 }, g); el('line', { class: 'ic', x1: -18, y1: -1, x2: 18, y2: -1 }, g); el('line', { class: 'ic', x1: -18, y1: 17, x2: 18, y2: 17 }, g); el('rect', { class: 'ic', x: -10, y: -20, width: 20, height: 14, rx: 3 }, g); },
    // tabel: kepala + baris yang bisa menyala (row r1..r3) seperti silinder DB
    table: g => { g.classList.add('cyl'); el('rect', { class: 'side', x: -30, y: -28, width: 60, height: 56, rx: 6 }, g); el('rect', { class: 'cap', x: -30, y: -28, width: 60, height: 14, rx: 6 }, g); ['r1', 'r2', 'r3'].forEach((r, i) => el('rect', { class: 'row ' + r, x: -22, y: -8 + i * 11, width: 44, height: 6, rx: 2 }, g)); },
    // proses/konsumen: layar dengan prompt
    proc: g => { el('rect', { class: 'ic', x: -28, y: -22, width: 56, height: 44, rx: 6 }, g); el('path', { class: 'ic', d: 'M-16,-6 L-8,0 L-16,6' }, g); el('line', { class: 'ic', x1: -2, y1: 6, x2: 12, y2: 6 }, g); },
  };
  const NODE_EL = {}, EDGE_EL = {}, LBL_EL = {}, SLOT_EL = {}, MSG_EL = {}, RAIL_EL = {}, TICK_EL = {}, RING_EL = {}, INNER_EL = {}, BLOCK_EL = {};
  const defs = el('defs', {});
  const pat = el('pattern', { id: 'dots', width: 40, height: 40, patternUnits: 'userSpaceOnUse' }, defs);
  el('circle', { cx: 20, cy: 20, r: 1.3, fill: 'currentColor', opacity: 0.16 }, pat);
  const bg = el('rect', { class: 'bgdots', x: -1200, y: -900, width: 4000, height: 2700, style: 'color: var(--ink-3)' });
  const grad = el('linearGradient', { id: 'sheen', x1: 0, x2: 1, y1: 0, y2: 0 }, defs);
  [[0, 0], [0.5, 0.55], [1, 0]].forEach(([o, a]) => el('stop', { offset: o, 'stop-color': '#ffffff', 'stop-opacity': a }, grad));
  const gE = el('g', {});
  Object.entries(EDGES).forEach(([id, e]) => {
    EDGE_EL[id] = el('path', { class: `edge ${e.kind}`, d: e.d }, gE);
    const t = el('text', { class: 'elbl', x: e.lbl[1], y: e.lbl[2], 'text-anchor': 'middle' }, gE); t.textContent = e.lbl[0]; LBL_EL[id] = t;
  });
  const RX = typeof RAIL_X !== 'undefined' ? RAIL_X : 560;
  Object.entries(RAILS).forEach(([k, r]) => {
    const g = el('g', { class: 'rail' }); RAIL_EL[k] = g; SLOT_EL[k] = [];
    const w = r.n === 1 ? 240 : r.n <= 4 ? 160 + r.n * 56 : 700;
    el('rect', { class: 'base', x: RX, y: r.y - 34, width: w, height: 68, rx: 12 }, g);
    const t = el('text', { class: 't', x: RX + 14, y: r.y - 16 }, g); t.textContent = r.label;
    const n = el('text', { class: 'n', x: RX + w - 14, y: r.y - 16, 'text-anchor': 'end' }, g); n.textContent = r.note != null ? r.note : (r.n + (r.n === 1 ? ' partisi · dibaca manusia' : ' partisi · kunci aggregate_id'));
    const hot = r.hot != null ? r.hot : (r.n === 1 ? 0 : 7);
    for (let i = 0; i < r.n; i++) {
      const x = SLOT_X(i) - 24;
      SLOT_EL[k].push(el('rect', { class: 'slot', x, y: r.y - 8, width: 48, height: 32, rx: 5 }, g));
      const sn = el('text', { class: 'sn', x: x + 24, y: r.y + 20 }, g); sn.textContent = i;
      if (i === hot) MSG_EL[k] = el('rect', { class: 'msg', x: x + 17, y: r.y - 2, width: 14, height: 14, rx: 3 }, g);
    }
    RAIL_EL[k]._hot = hot;
  });
  Object.entries(NODES).forEach(([id, n]) => {
    const outer = el('g', { transform: `translate(${n.x},${n.y})` });
    const g = el('g', { class: 'node' }, outer); NODE_EL[id] = g;
    el('rect', { class: 'box', x: -56, y: -56, width: 112, height: 112, rx: 22 }, g);
    RING_EL[id] = el('rect', { class: 'ring', x: -62, y: -62, width: 124, height: 124, rx: 26, 'stroke-dasharray': 470, 'stroke-dashoffset': 470 }, g);
    const ig = el('g', {}, g); ICON[n.icon](ig);
    const l = el('text', { class: 'lbl', y: 80 }, g); l.textContent = n.label;
    const s = el('text', { class: 'sub', y: 98 }, g); s.textContent = n.sub;
    // grup luar memegang posisi, grup dalam memegang animasi cap (transform CSS tidak boleh menimpa posisi)
    const numOuter = el('g', { transform: 'translate(46,-46)' }, g); const num = el('g', { class: 'num' }, numOuter); el('circle', { r: 14 }, num); el('text', { y: 5 }, num);
    TICK_EL[id] = el('text', { class: 'tick', y: -72 }, g);
    const pg = PANEL_GEOM(id);
    if (pg) {
      const inner = el('g', { class: 'inner' }, g); INNER_EL[id] = inner; BLOCK_EL[id] = [];
      const p = PANEL[id];
      el('path', { class: 'lead', d: p.side === 'below' ? `M0,56 V${pg.y}` : `M0,-56 V${pg.y + pg.h}` }, inner);
      el('rect', { class: 'panel', x: pg.x, y: pg.y, width: pg.w, height: pg.h, rx: 14 }, inner);
      const cap = el('text', { class: 'cap', x: pg.x + 16, y: pg.y + 20 }, inner); cap.textContent = p.title;
      p.blocks.forEach(([name, sub], i) => {
        const bx = pg.x + 20 + i * (pg.bw + pg.gap), by = pg.y + 32;
        const bg = el('g', { class: 'blk' }, inner); BLOCK_EL[id].push(bg);
        el('rect', { x: bx, y: by, width: pg.bw, height: 50, rx: 8 }, bg);
        const cp = el('clipPath', { id: `clip-${id}-${i}` }, defs); el('rect', { x: bx, y: by, width: pg.bw, height: 50, rx: 8 }, cp);
        el('rect', { class: 'sheen', x: bx - 10, y: by, width: 36, height: 50, 'clip-path': `url(#clip-${id}-${i})` }, bg);
        el('path', { class: 'chk', d: `M${bx + pg.bw - 20},${by + 10} l3,3 l6,-7` }, bg);
        const t1 = el('text', { x: bx + pg.bw / 2, y: by + 22 }, bg); t1.textContent = name;
        // label yang lebih lebar dari bloknya dirapatkan, bukan dibiarkan keluar (txService.Update, events(q).Write)
        const fitBlk = t => { try { if (t.getComputedTextLength() > pg.bw - 10) { t.setAttribute('textLength', pg.bw - 10); t.setAttribute('lengthAdjust', 'spacingAndGlyphs'); } } catch (_) {} }; fitBlk(t1);
        const t2 = el('text', { class: 'k', x: bx + pg.bw / 2, y: by + 38 }, bg); t2.textContent = sub; fitBlk(t2);
        if (i < p.blocks.length - 1) el('path', { class: 'arr', d: `M${bx + pg.bw + 2},${by + 25} L${bx + pg.bw + pg.gap - 2},${by + 25}` }, bg);
      });
    }
  });
  const pulse = el('circle', { class: 'pulse', r: 40 });
  // titik bermuatan: grup luar untuk posisi, grup dalam untuk bentuk
  const gGhost = el('g', {}); const ghosts = [0, 1, 2].map(i => el('circle', { class: 'ghost', r: 6 - i * 1.5 }, gGhost));
  const dotOuter = el('g', { class: 'dotpos' }); const dot = el('g', { class: 'dot' }, dotOuter);
  const gCall = el('g', {});

  // ---------- kamera ----------
  let cam = { x: 0, y: 0, w: 1600, h: 900 }, camRaf = null;
  const fit16 = (x, y, w, h, pad) => { let W = w + pad * 2, H = h + pad * 2; if (W / H > 16 / 9) H = W * 9 / 16; else W = H * 16 / 9; W = Math.min(1900, Math.max(560, W)); H = W * 9 / 16; let X = x + w / 2 - W / 2, Y = y + h / 2 - H / 2 + H * 0.09; X = Math.max(0, Math.min(1600 - W, X)); Y = Math.max(-140, Math.min(900 - H + H * 0.2, Y)); return { x: X, y: Y, w: W, h: H }; }; // subjek duduk di atas tengah, karena caption menutup tepi bawah
  const boxOf = ids => { const bs = ids.map(BOX); const x = Math.min(...bs.map(b => b.x)), y = Math.min(...bs.map(b => b.y)); return { x, y, w: Math.max(...bs.map(b => b.x + b.w)) - x, h: Math.max(...bs.map(b => b.y + b.h)) - y }; };
  const setView = v => { svg.setAttribute('viewBox', `${v.x} ${v.y} ${v.w} ${v.h}`); const z = v.w < 1150; svg.classList.toggle('zoomed', z); document.getElementById('canvas').classList.toggle('zoomed', z);
    // paralaks: latar bergeser 35 % dari pergerakan kamera, jadi tampak lebih jauh
    bg.setAttribute('transform', `translate(${v.x * 0.35},${v.y * 0.35})`); };
  const camera = (target, ms, linear) => new Promise(res => {
    if (camRaf) cancelAnimationFrame(camRaf);
    if (reduced || ms === 0) { cam = target; setView(cam); res(); return; }
    const from = { ...cam }; let t0 = null;
    const frame = now => { if (t0 === null) t0 = now; const t = Math.min(1, (now - t0) / ms), e = linear ? t : (t < 0.5 ? 2 * t * t : -1 + (4 - 2 * t) * t);
      cam = { x: from.x + (target.x - from.x) * e, y: from.y + (target.y - from.y) * e, w: from.w + (target.w - from.w) * e, h: from.h + (target.h - from.h) * e }; setView(cam);
      if (t < 1) camRaf = requestAnimationFrame(frame); else res(); };
    camRaf = requestAnimationFrame(frame);
  });
  const OVERVIEW = { x: 0, y: 0, w: 1600, h: 900 };
  // dorong masuk pelan (Ken Burns): 5 % lebih dekat sepanjang durasi proses, tidak ditunggu
  const pushIn = (v, ms) => { if (reduced) return; const w = v.w * 0.95, hh = v.h * 0.95; camera({ x: v.x + (v.w - w) / 2, y: v.y + (v.h - hh) / 2, w, h: hh }, ms, true); };
  // kartu Rincian menutup sepertiga kanan layar; saat terbuka, subjek digeser ke kiri supaya tidak tertutup
  // Geserannya dibatasi oleh kotak subjek: tepi kiri subjek tidak boleh keluar dari pandangan (subjek di tepi kiri kanvas, X sudah 0, dulu ikut tergeser dan terpotong).
  // Kartu Rincian menutup ~34 % sisi kanan layar. Saat terbuka, subjek ditempatkan di 64 % kiri: pandangan diperlebar bila
  // subjeknya lebih lebar dari ruang itu, lalu digeser sehingga tepi kanan subjek berada di kiri kartu dan tepi kirinya tetap terlihat.
  const sideBias = (v, b) => { if (!$('#detail').classList.contains('open')) return v; const FREE = 0.64;
    let W = v.w, H = v.h, Y = v.y; const need = (b.w + 80) / FREE; if (need > W) { W = need; H = W * 9 / 16; Y = v.y + (v.h - H) / 2; }
    let X = b.x + b.w / 2 - W * FREE / 2; X = Math.min(X, b.x - 40); X = Math.max(X, b.x + b.w - W * FREE + 40);
    if (X < 0 && b.x + b.w <= W * FREE - 40) X = 0; // subjek di tepi kiri: tidak perlu menggeser kanvas ke kanan
    return { x: X, y: Math.max(Y, -60), w: W, h: H }; };
  // ruang di atas node untuk gelembung keterangan (jangkar 92 px di atas node, 60 px di atas panel), supaya tidak masuk ke bilah atas
  const focusView = id => { const b = BOX(id); return sideBias(id.startsWith('rail-') ? fit16(b.x, b.y - 30, b.w, b.h + 30, 120) : fit16(b.x, b.y - 100, b.w, b.h + 100, 110), b); }; // padding lebih rapat supaya tetangga di bawah tidak masuk ke area caption
  const procView = step => { const ids = new Set([step.at]); (step.proc || []).forEach(p => (p.go || []).forEach(t => { ids.add(EDGES[t.e].a); ids.add(EDGES[t.e].b); })); if (step.after && step.after.length) { ids.add(EDGES[step.after[0].e].a); ids.add(EDGES[step.after[0].e].b); } const b = boxOf([...ids]); return ids.size > 1 ? sideBias(fit16(b.x, b.y - 100, b.w, b.h + 100, 120), b) : focusView(step.at); };
  const travelView = list => { const ids = new Set(); list.forEach(t => { ids.add(EDGES[t.e].a); ids.add(EDGES[t.e].b); }); const b = boxOf([...ids]); return sideBias(fit16(b.x, b.y - 100, b.w, b.h + 100, 110), b); };

  // ---------- render dari state ----------
  const railKeys = Object.keys(RAILS);
  const slotState = (S, k) => (S.slots && k in S.slots) ? S.slots[k] : (k === 'jobs' ? S.slotJobs : k === 'results' ? S.slotRes : S.slotDlq);
  const paint = (S, order) => {
    Object.entries(NODE_EL).forEach(([id, g]) => {
      const shown = S.shown.includes(id);
      g.classList.toggle('shown', shown); g.classList.toggle('focus', S.focus === id); g.classList.toggle('dim', shown && S.focus && S.focus !== id); g.classList.toggle('dead', !!(S.dead && S.dead.includes(id)));
      if (shown) { g.querySelector('.num text').textContent = order.filter(x => NODES[x]).indexOf(id) + 1; g.querySelector('.lbl').textContent = NODES[id].label; }
      TICK_EL[id].textContent = S.ticks[id] || ''; TICK_EL[id].classList.toggle('on', !!S.ticks[id]);
      if (S.rows && S.rows[id]) { [1, 2, 3].forEach(n => { const r = g.querySelector('.r' + n); if (r) r.classList.toggle('on', S.rows[id].includes(n)); }); }
      else if (id === 'pg' && 'ra' in S) { g.querySelector('.r1').classList.toggle('on', S.ra !== 'not_requested'); g.querySelector('.r2').classList.toggle('on', S.outbox.length > 0); }
    });
    railKeys.forEach(k => {
      const shown = S.shown.includes('rail-' + k); RAIL_EL[k].classList.toggle('shown', shown);
      const v = slotState(S, k), hot = RAIL_EL[k]._hot;
      SLOT_EL[k][hot].classList.toggle('lit', v === 'lit'); MSG_EL[k].classList.toggle('on', v === 'lit'); MSG_EL[k].classList.toggle('used', v === 'used');
      RAIL_EL[k].style.opacity = shown ? (S.focus === 'rail-' + k ? 1 : (S.focus ? 0.55 : 1)) : 0;
      RAIL_EL[k].classList.toggle('faraway', shown && S.focus && S.focus !== 'rail-' + k);
    });
    Object.entries(EDGE_EL).forEach(([id, p]) => { p.classList.toggle('shown', S.edges.includes(id)); p.classList.remove('on'); p.classList.toggle('trail', (S.trail || []).includes(id)); LBL_EL[id].classList.remove('on'); });
    if (S.focus && S.focus.startsWith('rail-')) { const k = S.focus.slice(5); pulse.setAttribute('cx', SLOT_X(RAIL_EL[k]._hot)); pulse.setAttribute('cy', RAILS[k].y + 8); }
    else if (S.focus) { pulse.setAttribute('cx', NODES[S.focus].x); pulse.setAttribute('cy', NODES[S.focus].y); }
  };
  const ping = () => { pulse.classList.remove('go'); void pulse.getBoundingClientRect(); if (!reduced) pulse.classList.add('go'); };

  // ---------- suara ----------
  let actx = null, sfxOn = true, voiceOn = false, voice = null;
  try { sfxOn = localStorage.getItem('selaras-sfx') !== 'off'; voiceOn = localStorage.getItem('selaras-voice') === 'on'; } catch (_) {}
  const toast = msg => { const t = $('#toast'); t.textContent = msg; t.classList.add('on'); setTimeout(() => t.classList.remove('on'), 2200); };
  const ensureAudio = () => { if (!actx) { try { actx = new (window.AudioContext || window.webkitAudioContext)(); } catch (_) { actx = null; } } if (actx && actx.state === 'suspended') actx.resume(); return actx; };
  const tone = (freq, ms, type, gain, slide) => { const c = actx; if (!c) return; const o = c.createOscillator(), g = c.createGain(); o.type = type || 'sine'; o.frequency.setValueAtTime(freq, c.currentTime); if (slide) o.frequency.exponentialRampToValueAtTime(slide, c.currentTime + ms / 1000); g.gain.setValueAtTime(0.0001, c.currentTime); g.gain.exponentialRampToValueAtTime(gain || 0.12, c.currentTime + 0.012); g.gain.exponentialRampToValueAtTime(0.0001, c.currentTime + ms / 1000); o.connect(g).connect(c.destination); o.start(); o.stop(c.currentTime + ms / 1000 + 0.02); };
  const noise = (ms, gain, from, to) => { const c = actx; if (!c) return; const n = Math.floor(c.sampleRate * ms / 1000), buf = c.createBuffer(1, n, c.sampleRate), d = buf.getChannelData(0); for (let i = 0; i < n; i++) d[i] = (Math.random() * 2 - 1) * (1 - i / n); const s = c.createBufferSource(); s.buffer = buf; const f = c.createBiquadFilter(); f.type = 'bandpass'; f.frequency.setValueAtTime(from, c.currentTime); f.frequency.exponentialRampToValueAtTime(to, c.currentTime + ms / 1000); f.Q.value = 1.2; const g = c.createGain(); g.gain.value = gain; s.connect(f).connect(g).connect(c.destination); s.start(); };
  const sfx = {
    travel: () => { if (sfxOn && actx) noise(260, 0.18, 400, 1600); },
    arrive: () => { if (sfxOn && actx) tone(880, 90, 'sine', 0.10); },
    pop: () => { if (sfxOn && actx) tone(720, 80, 'sine', 0.07, 1040); },
    bad: () => { if (sfxOn && actx) tone(330, 220, 'sawtooth', 0.05, 180); },
    done: () => { if (sfxOn && actx) { tone(660, 140, 'sine', 0.09); setTimeout(() => tone(990, 220, 'sine', 0.09), 120); } },
    step: () => { if (sfxOn && actx) tone(520, 60, 'triangle', 0.05, 640); },
    end: () => { if (sfxOn && actx) { [523, 659, 784, 1046].forEach((f, i) => setTimeout(() => tone(f, 260, 'sine', 0.08), i * 110)); } },
  };
  // Musik latar: pad ambien disintesis (dua osilator detune + filter yang bernapas). Mengecil saat narasi bicara.
  let musicOn = true; try { musicOn = localStorage.getItem('selaras-music') !== 'off'; } catch (_) {}
  const music = { nodes: null, mood: 'major',
    start() { if (!actx || this.nodes || !musicOn) return; const c = actx; const master = c.createGain(); master.gain.value = 0.0001; const filt = c.createBiquadFilter(); filt.type = 'lowpass'; filt.frequency.value = 520; filt.Q.value = 0.7;
      const lfo = c.createOscillator(), lfoG = c.createGain(); lfo.frequency.value = 0.07; lfoG.gain.value = 220; lfo.connect(lfoG).connect(filt.frequency); lfo.start();
      const voices = [110, 164.81, 277.18, 329.63].map((f, i) => { const o = c.createOscillator(); o.type = i < 2 ? 'sine' : 'triangle'; o.frequency.value = f; o.detune.value = (i % 2 ? 6 : -6); const g = c.createGain(); g.gain.value = i < 2 ? 0.5 : 0.22; o.connect(g).connect(filt); o.start(); return { o, g, base: f }; });
      filt.connect(master).connect(c.destination); master.gain.exponentialRampToValueAtTime(0.05, c.currentTime + 2.5); this.nodes = { master, voices, filt }; },
    level(v, sec) { if (!this.nodes || !actx) return; this.nodes.master.gain.cancelScheduledValues(actx.currentTime); this.nodes.master.gain.setTargetAtTime(v, actx.currentTime, sec || 0.4); },
    duck(on) { this.level(on ? 0.014 : 0.05, on ? 0.15 : 0.9); },
    setMood(m) { if (!this.nodes || !actx || this.mood === m) return; this.mood = m; const third = this.nodes.voices[2]; third.o.frequency.setTargetAtTime(m === 'minor' ? 261.63 : 277.18, actx.currentTime, 0.6); this.nodes.filt.frequency.setTargetAtTime(m === 'minor' ? 380 : 520, actx.currentTime, 0.8); },
    bright() { if (!this.nodes || !actx) return; this.nodes.filt.frequency.setTargetAtTime(1400, actx.currentTime, 0.5); this.level(0.07, 0.3); setTimeout(() => { if (this.nodes) { this.nodes.filt.frequency.setTargetAtTime(520, actx.currentTime, 2); this.level(0.05, 2); } }, 2600); },
    stop() { if (!this.nodes) return; this.level(0.0001, 0.4); const n = this.nodes; this.nodes = null; setTimeout(() => { try { n.voices.forEach(v => v.o.stop()); } catch (_) {} }, 800); },
  };
  const setLang = l => { try { localStorage.setItem('selaras-lang', l); } catch (_) {} location.reload(); };
  $('#b-lang').addEventListener('click', () => setLang(LANG === 'id' ? 'en' : 'id'));
  const bMusic = $('#b-music'); bMusic.setAttribute('aria-pressed', musicOn);
  const setMusic = on => { musicOn = on; bMusic.setAttribute('aria-pressed', on); try { localStorage.setItem('selaras-music', on ? 'on' : 'off'); } catch (_) {} if (on) { ensureAudio(); music.start(); } else music.stop(); };
  bMusic.addEventListener('click', () => setMusic(!musicOn));
  ['pointerdown', 'keydown'].forEach(ev => document.addEventListener(ev, () => { if (musicOn) { ensureAudio(); music.start(); } }, { once: true }));
  const KEEP = []; let lastCancel = 0;
  const pickVoice = () => { const vs = window.speechSynthesis ? speechSynthesis.getVoices() : []; voice = vs.find(v => /^id[-_]/i.test(v.lang) && /google/i.test(v.name)) || vs.find(v => /^id[-_]/i.test(v.lang)) || null; const b = $('#b-voice'); if (!voice) { b.disabled = true; b.title = 'narasi tidak tersedia: tidak ada suara Bahasa Indonesia di browser ini'; if (voiceOn) { voiceOn = false; b.setAttribute('aria-pressed', 'false'); } } else { b.disabled = false; b.title = 'narasi: ' + voice.name + ' (N)'; } };
  if (window.speechSynthesis) { pickVoice(); speechSynthesis.onvoiceschanged = pickVoice; }
  const speak = text => new Promise(res => {
    if (!voiceOn || !voice || !window.speechSynthesis) { res(); return; }
    const go = attempt => { const u = new SpeechSynthesisUtterance(text); u.voice = voice; u.lang = voice.lang; u.rate = 1.04; u.pitch = 1; KEEP.push(u); if (KEEP.length > 40) KEEP.shift(); music.duck(true); u.onend = () => { music.duck(false); res(); }; u.onerror = ev => { music.duck(false); if (attempt < 1 && ev.error !== 'interrupted' && ev.error !== 'canceled') setTimeout(() => go(attempt + 1), 120); else res(); }; speechSynthesis.speak(u); };
    const since = performance.now() - lastCancel; if (since < 150) setTimeout(() => go(0), 150 - since); else go(0);
  });
  const hush = () => { if (window.speechSynthesis) { speechSynthesis.cancel(); lastCancel = performance.now(); } };
  const bSfx = $('#b-sfx'), bVoice = $('#b-voice');
  bSfx.setAttribute('aria-pressed', sfxOn); bVoice.setAttribute('aria-pressed', voiceOn);
  const setSfx = on => { sfxOn = on; bSfx.setAttribute('aria-pressed', on); try { localStorage.setItem('selaras-sfx', on ? 'on' : 'off'); } catch (_) {} if (on) { ensureAudio(); sfx.arrive(); } };
  const setVoice = on => { if (on && !voice) { toast('Tidak ada suara Bahasa Indonesia di browser ini'); return; } voiceOn = on; bVoice.setAttribute('aria-pressed', on); try { localStorage.setItem('selaras-voice', on ? 'on' : 'off'); } catch (_) {} if (!on) hush(); else speak('Narasi aktif.'); };
  bSfx.addEventListener('click', () => setSfx(!sfxOn));
  bVoice.addEventListener('click', () => setVoice(!voiceOn));
  ['pointerdown', 'keydown'].forEach(ev => document.addEventListener(ev, () => ensureAudio(), { once: true }));

  // ---------- animasi: titik bermuatan, keterangan, cincin ----------
  const timers = new Set(), rafs = new Set();
  let gen = 0;
  const stopAnim = () => { gen++; Object.values(SLOT_EL).forEach(arr => arr.forEach(s => s.classList.remove('busy'))); timers.forEach(clearTimeout); timers.clear(); rafs.forEach(cancelAnimationFrame); rafs.clear(); dot.style.opacity = 0; dot.classList.remove('land'); ghosts.forEach(gh => { gh.style.opacity = 0; }); gE.querySelectorAll('.edge.draw').forEach(n => n.remove()); gCall.innerHTML = ''; Object.values(RING_EL).forEach(r => r.classList.remove('run')); hush(); };
  const wait = ms => new Promise(r => { const t = setTimeout(() => { timers.delete(t); r(); }, ms); timers.add(t); });
  const drawPayload = (p, kind) => {
    dot.innerHTML = ''; const spec = PAYLOAD[p] || PAYLOAD[{ http: 'req', grpc: 'req', db: 'sql', evt: 'evt', ext: 'llm' }[kind]] || PAYLOAD.req;
    dot.setAttribute('class', 'dot ' + kind + ' ' + spec.shape);
    if (spec.shape === 'card') { el('rect', { x: -22, y: -12, width: 44, height: 24, rx: 5 }, dot); const t = el('text', { y: 4 }, dot); t.textContent = spec.text; }
    else if (spec.shape === 'jwt') { el('rect', { x: -24, y: -11, width: 48, height: 22, rx: 5 }, dot); [-16, -2, 12].forEach((x, i) => el('rect', { class: 'seg s' + i, x, y: -6, width: i === 1 ? 12 : 8, height: 12, rx: 2 }, dot)); }
    else if (spec.shape === 'env') { el('rect', { x: -18, y: -12, width: 36, height: 24, rx: 4 }, dot); el('path', { class: 'ln', d: 'M-18,-12 L0,2 L18,-12' }, dot); }
    else if (spec.shape === 'doc') { el('path', { d: 'M-14,-17 H8 L16,-9 V17 H-14 Z' }, dot); [-7, -1, 5].forEach(y => el('line', { class: 'ln', x1: -8, y1: y, x2: 10, y2: y }, dot)); }
    else if (spec.shape === 'spark') { el('path', { d: 'M0,-16 C1,-6 6,-1 16,0 C6,1 1,6 0,16 C-1,6 -6,1 -16,0 C-6,-1 -1,-6 0,-16 Z' }, dot); }
    else if (spec.shape === 'ack') { el('circle', { r: 11 }, dot); el('path', { class: 'ln', d: 'M-5,0 L-1,4 L6,-4' }, dot); }
    else if (spec.shape === 'err') { el('circle', { r: 11 }, dot); el('path', { class: 'ln', d: 'M-4,-4 L4,4 M4,-4 L-4,4' }, dot); }
  };
  const travel = list => new Promise(res => {
    if (!list || !list.length || reduced) { (list || []).forEach(t => EDGE_EL[t.e].classList.add('shown', 'trail')); res(); return; }
    let k = 0;
    const one = () => {
      if (k >= list.length) { res(); return; }
      const { e, rev, p } = list[k++]; const path = EDGE_EL[e], L = path.getTotalLength(), dur = 600 + Math.min(500, L * 0.9);
      path.classList.add('shown'); LBL_EL[e].classList.add('on'); drawPayload(p, EDGES[e].kind); sfx.travel();
      // garis "digambar" mengikuti titik: salinan jalur dengan dasharray yang dibuka bertahap
      const draw = el('path', { class: 'edge draw ' + EDGES[e].kind, d: EDGES[e].d, 'stroke-dasharray': L, 'stroke-dashoffset': rev ? -L : L }, gE);
      const hist = [];
      ghosts.forEach(gh => { gh.setAttribute('class', 'ghost ' + EDGES[e].kind); });
      let t0 = null;
      const frame = now => { if (t0 === null) t0 = now; const t = Math.min(1, (now - t0) / dur), ease = t < 0.5 ? 2 * t * t : -1 + (4 - 2 * t) * t;
        const pt = path.getPointAtLength((rev ? 1 - ease : ease) * L); dotOuter.setAttribute('transform', `translate(${pt.x},${pt.y})`); dot.style.opacity = 1;
        draw.setAttribute('stroke-dashoffset', rev ? -L * (1 - ease) : L * (1 - ease));
        hist.unshift(pt); if (hist.length > 10) hist.pop();
        ghosts.forEach((gh, i) => { const q = hist[(i + 1) * 3]; if (q) { gh.setAttribute('cx', q.x); gh.setAttribute('cy', q.y); gh.style.opacity = 0.45 - i * 0.13; } else gh.style.opacity = 0; });
        if (t < 1) { rafs.add(requestAnimationFrame(frame)); }
        else { ghosts.forEach(gh => { gh.style.opacity = 0; }); dot.classList.add('land'); draw.remove(); path.classList.add('trail', 'hot'); LBL_EL[e].classList.remove('on'); sfx.arrive();
          const tmH = setTimeout(() => path.classList.remove('hot'), 900); timers.add(tmH);
          if (!reduced) { const endPt = path.getPointAtLength(rev ? 0 : L); for (let s = 0; s < 6; s++) { const ang = Math.random() * Math.PI * 2, d = 18 + Math.random() * 22; const sp = el('circle', { class: 'spark', cx: endPt.x, cy: endPt.y, r: 2.2, style: `--dx:${Math.cos(ang) * d}px; --dy:${Math.sin(ang) * d}px` }, gGhost); const ts = setTimeout(() => sp.remove(), 600); timers.add(ts); } }
          const tm = setTimeout(() => { dot.classList.remove('land'); dot.style.opacity = 0; one(); }, 240); timers.add(tm); } };
      rafs.add(requestAnimationFrame(frame));
    };
    one();
  });
  const openPanel = id => { Object.entries(INNER_EL).forEach(([k, g]) => { const on = k === id; g.classList.toggle('open', on); NODE_EL[k].classList.toggle('expanded', on); if (!on) BLOCK_EL[k].forEach(b => b.classList.remove('lit', 'done')); }); };
  const litBlock = (id, i) => { if (!BLOCK_EL[id]) return; BLOCK_EL[id].forEach((b, k) => { if (k === i) { b.classList.remove('done'); b.classList.add('lit'); } else if (b.classList.contains('lit')) { b.classList.remove('lit'); b.classList.add('done'); } }); };
  const blockAnchor = (id, i) => { const pg = PANEL_GEOM(id); const n = NODES[id]; return { x: n.x + pg.x + 20 + i * (pg.bw + pg.gap) + pg.bw / 2, y: n.y + pg.y - 24 }; };
  const anchorOf = id => id.startsWith('rail-') ? { x: SLOT_X(RAIL_EL[id.slice(5)]._hot), y: RAILS[id.slice(5)].y - 46 } : { x: NODES[id].x, y: NODES[id].y - 92 };
  // Keterangan: teks panjang dipecah maksimal tiga baris, lebar gelembung diukur dari teks sebenarnya (bukan taksiran per huruf),
  // dan gelembungnya digeser agar tetap di dalam pandangan kamera (dan di kiri kartu Rincian bila terbuka); tangkainya tetap di jangkar.
  const wrapText = (text, max) => { if (text.length <= max) return [text]; const lines = []; let cur = ''; text.split(' ').forEach(w => { if (cur && (cur + ' ' + w).length > max) { lines.push(cur); cur = w; } else cur = cur ? cur + ' ' + w : w; }); if (cur) lines.push(cur); return lines.slice(0, 3); };
  const callout = (id, text, cls, blk) => {
    const a = (blk != null && PANEL[id]) ? blockAnchor(id, blk) : anchorOf(id);
    const lines = wrapText(text, 56), lh = 18, hgt = 30 + (lines.length - 1) * lh, top = 14 - hgt;
    const outer = el('g', { transform: `translate(${a.x},${a.y})` }, gCall);
    const g = el('g', { class: 'call ' + (cls || '') }, outer);
    const stemLen = (blk != null && PANEL[id]) ? 42 : id.startsWith('rail-') ? 0 : 22;
    if (stemLen) el('path', { class: 'stem', d: `M0,14 V${14 + stemLen}` }, g);
    const bub = el('g', { class: 'bub' }, g);
    const rect = el('rect', { x: -60, y: top, width: 120, height: hgt, rx: 8 }, bub);
    const t = el('text', { y: top + 21 }, bub);
    const spans = lines.map((ln, i) => { const s = el('tspan', { x: 0, dy: i ? lh : 0 }, t); s.textContent = ln; return s; });
    let w = 120; try { w = Math.max(120, Math.max(...spans.map(s => s.getComputedTextLength())) + 28); } catch (_) { w = Math.max(120, Math.max(...lines.map(l => l.length)) * 7.6 + 28); }
    rect.setAttribute('x', -w / 2); rect.setAttribute('width', w);
    const L = cam.x + 14, R = cam.x + cam.w * ($('#detail').classList.contains('open') ? 0.66 : 1) - 14;
    let dx = 0; if (a.x + w / 2 > R) dx = R - (a.x + w / 2); if (a.x - w / 2 + dx < L) dx = L - (a.x - w / 2);
    if (dx) bub.setAttribute('transform', `translate(${dx},0)`);
    const offs = lines.map((_, i) => lines.slice(0, i).reduce((n, l) => n + l.length + 1, 0));
    if (!reduced) { spans.forEach(s => { s.textContent = ''; }); text.split('').forEach((ch, k) => { const tm = setTimeout(() => { const pre = text.slice(0, k + 1); spans.forEach((s, i) => { s.textContent = pre.slice(offs[i], offs[i] + lines[i].length); }); }, 120 + k * 16); timers.add(tm); }); }
    void g.getBoundingClientRect(); g.classList.add('in');
    return g;
  };
  window.__reelDebug = { callout, view: () => cam }; // kait untuk penonton otomatis (tools/tonton.js)
  const dismiss = g => { g.classList.remove('in'); g.classList.add('out'); const t = setTimeout(() => g.parentNode && g.parentNode.remove(), 400); timers.add(t); };
  const fx = cmd => { const [k, a, b] = cmd.split(':');
    if (k === 'slot') { const hot = RAIL_EL[a]._hot; const sl = SLOT_EL[a][hot]; sl.classList.remove('ripple'); void sl.getBoundingClientRect(); if (b === 'lit') sl.classList.add('ripple'); sl.classList.toggle('lit', b === 'lit'); MSG_EL[a].classList.toggle('on', b === 'lit'); MSG_EL[a].classList.toggle('used', b === 'used'); RAIL_EL[a].classList.add('shown'); RAIL_EL[a].style.opacity = 1; }
    else if (k === 'row') NODE_EL[a].querySelector('.r' + b).classList.add('on');
    else if (k === 'unrow') NODE_EL[a].querySelector('.r' + b).classList.remove('on');
    else if (k === 'shake') { const g = NODE_EL[a]; g.classList.remove('shake'); void g.getBoundingClientRect(); g.classList.add('shake'); }
    else if (k === 'off') { const g = NODE_EL[a]; g.classList.add('dead'); }
    else if (k === 'on') { const g = NODE_EL[a]; g.classList.remove('dead'); }
    else if (k === 'tick') { TICK_EL[a].textContent = b; TICK_EL[a].classList.add('on'); }
    else if (k === 'flash') { const g = NODE_EL[a]; g.classList.remove('flash'); void g.getBoundingClientRect(); g.classList.add('flash'); }
    else if (k === 'busy') { const hot = RAIL_EL[a]._hot; SLOT_EL[a].forEach((s, i) => { if (i === hot) return; s.style.animationDelay = (i * 90) + 'ms'; s.classList.add('busy'); }); const t = setTimeout(() => SLOT_EL[a].forEach(s => s.classList.remove('busy')), 4200); timers.add(t); } };
  const proc = async (id, items, g0) => {
    const per = reduced ? 120 : 1150;
    const ring = RING_EL[id];
    const goCount = items.reduce((a, p) => a + (p.go ? p.go.length : 0), 0);
    if (ring) { ring.style.setProperty('--dur', (items.length * per + goCount * 900) + 'ms'); ring.classList.remove('run'); void ring.getBoundingClientRect(); ring.classList.add('run'); }
    for (const raw of items) {
      if (g0 !== gen) return;
      const item = typeof raw === 'string' ? { t: raw } : raw;
      const txt = T(item.t);
      if (item.b != null) litBlock(id, item.b);
      if (item.bad) sfx.bad(); else sfx.pop();
      const c = callout(id, txt, item.bad ? 'bad' : '', item.b);
      (item.fx || []).forEach(fx);
      const said = speak(txt);
      if (item.go) { await wait(reduced ? 0 : 250); if (g0 !== gen) return; await travel(item.go); if (g0 !== gen) return; await Promise.all([wait(per * 0.45), said]); }
      else await Promise.all([wait(per), said]);
      if (g0 !== gen) return; dismiss(c); await wait(reduced ? 0 : 120);
    }
    if (BLOCK_EL[id]) BLOCK_EL[id].forEach(b => { if (b.classList.contains('lit')) { b.classList.remove('lit'); b.classList.add('done'); } });
  };

  // ---------- penghitung waktu ----------
  const fmtMs = ms => ms < 1000 ? (Math.round(ms * 10) / 10).toLocaleString('id-ID') + ' ms' : ms < 60000 ? (Math.round(ms / 100) / 10).toLocaleString('id-ID') + ' s' : Math.floor(ms / 60000) + ' mnt ' + Math.round((ms % 60000) / 1000) + ' s';
  const latEl = { u: $('#lat-u'), s: $('#lat-s') };
  let latShown = { u: 0, s: 0 };
  const setLat = (S, animate) => {
    const target = { u: S.latU || 0, s: S.latS || 0 };
    ['u', 's'].forEach(k => { const from = latShown[k], to = target[k]; if (!animate || reduced || to === from) { latShown[k] = to; latEl[k].textContent = fmtMs(to); return; }
      const t0 = performance.now(), dur = 900; const frame = now => { const t = Math.min(1, (now - t0) / dur); latShown[k] = from + (to - from) * t; latEl[k].textContent = fmtMs(latShown[k]); if (t < 1) rafs.add(requestAnimationFrame(frame)); }; rafs.add(requestAnimationFrame(frame)); });
    $('#lat').classList.toggle('frozen', !!S.userDone);
  };

  // ---------- urutan aktif (utama + cabang) ----------
  let seq = STEPS.slice(), branchName = null;
  // Titik cabang: langkah dengan branch: true (semua cabang) atau branch: ['a','b'] (cabang tertentu); cabang boleh menyebut after: <indeks langkah>.
  const branchAt = k => Object.keys(BRANCHES).filter(name => { const b = BRANCHES[name]; if (b.after != null) return b.after === k; const st = STEPS[k]; return st.branch === true || (Array.isArray(st.branch) && st.branch.includes(name)); });
  const buildSeq = name => { branchName = name; if (!name) return STEPS.slice(); const b = BRANCHES[name]; let k = b.after != null ? b.after : STEPS.findIndex(s => s.branch === true || (Array.isArray(s.branch) && s.branch.includes(name))); if (k < 0) k = STEPS.findIndex(s => s.branch); return b.resume ? [...STEPS.slice(0, k + 1), ...b.steps, ...STEPS.slice(k + 1)] : [...STEPS.slice(0, k + 1), ...b.steps]; };
  const applyStep = (S, st) => { st.mut(S); if (st.lat) { if (st.lat.u) S.latU = (S.latU || 0) + st.lat.u; if (st.lat.s) S.latS = (S.latS || 0) + st.lat.s; } S.trail = S.trail || []; [...(st.travel || []), ...(st.after || []), ...(st.proc || []).flatMap(p => (typeof p === 'string' ? [] : (p.go || [])))].forEach(t => { if (!S.trail.includes(t.e)) S.trail.push(t.e); }); };

  // ---------- langkah ----------
  let idx = -1, playing = false, showCode = REEL.tier === 3, S = initial(), order = [];
  const durOf = s => 0.9 + (s.proc ? s.proc.length * 1.3 : 0) + ((s.travel ? s.travel.length : 0) + (s.after ? s.after.length : 0) + (s.proc || []).reduce((a, p) => a + (p.go ? p.go.length : 0), 0)) * 0.85 + Math.min(3, (s.text || '').length / 90);
  const totalOf = () => seq.reduce((a, s) => a + durOf(s), 0);
  const fmt = s => `${Math.floor(s / 60)}:${String(Math.floor(s % 60)).padStart(2, '0')}`;
  const rebuild = i => { S = initial(); order = []; for (let k = 0; k < i; k++) { applyStep(S, seq[k]); S.shown.forEach(id => { if (!order.includes(id)) order.push(id); }); } };
  const renderDetail = step => {
    const [title, html] = ZOOM[step.zoom](S); $('#z-title').textContent = title; $('#z-body').innerHTML = html;
    const code = step.code && CODE[step.code]; $('#code').classList.toggle('open', showCode && !!code);
    if (code) { $('#code-file').textContent = code.file + (Array.isArray(step.lines) ? `  ·  baris ${step.lines[0]}${step.lines[1] && step.lines[1] !== step.lines[0] ? '–' + step.lines[1] : ''} dari cuplikan` : '');
      let src = code.src.replace(/<c>/g, '<span class="c">').replace(/<\/c>/g, '</span>');
      if (step.lines) { const rows = src.split('\n'); const hit = k => Array.isArray(step.lines) ? (k + 1 >= step.lines[0] && k + 1 <= (step.lines[1] || step.lines[0])) : rows[k].replace(/<[^>]+>/g, '').includes(step.lines);
        src = rows.map((ln, k) => hit(k) ? '<b>' + ln.replace(/<\/?b>/g, '') + '</b>' : ln).join('\n'); }
      $('#code-pre').innerHTML = src;
      const hl = $('#code-pre b'); if (hl && showCode) hl.scrollIntoView({ block: 'center', behavior: reduced ? 'auto' : 'smooth' }); }
  };
  // tautan turun ke mekanisme (lapisan 3) bila reel-nya sudah ada di manifes
  const downLink = step => { if (!step.down) return ''; const [id, n] = String(step.down).split('/'); const r = (typeof REELS !== 'undefined' ? REELS : []).find(x => x.id === id); if (!r) return ''; const ok = r.status === 'jadi'; const label = LANG === 'en' ? 'See the mechanism' : 'Lihat mekanismenya'; return ok ? `<a class="down" href="${r.file}#${n || 1}">↓ ${label} · ${id}</a>` : `<span class="down" aria-disabled="true" title="${LANG === 'en' ? 'not built yet' : 'belum dibuat'}">↓ ${id} · ${T(r.title)}</span>`; };
  const caption = (i, step) => {
    const cap = $('#cap'); cap.classList.add('swap');
    const t = setTimeout(() => { $('#c-n').textContent = `Langkah ${i + 1} / ${seq.length}` + (branchName ? ` · cabang: ${T(BRANCHES[branchName].label)}` : ''); $('#c-title').innerHTML = T(step.title).split(' ').map((w, k) => `<span class="w" style="--i:${k}">${w}</span>`).join(' '); $('#c-text').textContent = T(step.text);
      $('#c-refs').innerHTML = (step.refs || []).map(r => `<span class="${/^ADR-/.test(r) ? 'adr' : ''}">${r}</span>`).join('') + (step.proof ? [].concat(step.proof).map(p => `<span class="proof" title="${LANG === 'en' ? 'test that proves this step' : 'test yang membuktikan langkah ini'}">✓ ${p}</span>`).join('') : '') + downLink(step); cap.classList.remove('swap'); }, reduced ? 0 : 300); timers.add(t);
  };
  const reveal = step => {
    const P = JSON.parse(JSON.stringify(S)); step.mut(P);
    P.shown.filter(id => !S.shown.includes(id)).forEach(id => { if (!order.includes(id)) order.push(id);
      if (NODE_EL[id]) { const g = NODE_EL[id]; g.classList.add('shown', 'pop'); g.querySelector('.num text').textContent = order.filter(x => NODES[x]).indexOf(id) + 1; typeLabel(id); const t = setTimeout(() => g.classList.remove('pop'), 900); timers.add(t); }
      else if (id.startsWith('rail-')) { RAIL_EL[id.slice(5)].classList.add('shown'); RAIL_EL[id.slice(5)].style.opacity = 1; } });
    P.edges.filter(e => !S.edges.includes(e)).forEach(e => EDGE_EL[e].classList.add('shown'));
  };
  const typeLabel = id => { if (reduced) return; const lbl = NODE_EL[id].querySelector('.lbl'), full = NODES[id].label; lbl.textContent = ''; full.split('').forEach((ch, k) => { const t = setTimeout(() => { lbl.textContent = full.slice(0, k + 1); }, 260 + k * 30); timers.add(t); }); };
  const focusNow = id => { Object.entries(NODE_EL).forEach(([k, g]) => { g.classList.toggle('focus', k === id); if (g.classList.contains('shown')) g.classList.toggle('dim', k !== id); }); railKeys.forEach(k => { if (RAIL_EL[k].classList.contains('shown')) { RAIL_EL[k].style.opacity = ('rail-' + k === id) ? 1 : 0.55; RAIL_EL[k].classList.toggle('faraway', 'rail-' + k !== id); } }); };

  // pilihan cabang
  const choiceEl = $('#choice');
  let choiceTimer = null;
  const hideChoice = () => { choiceEl.classList.remove('open'); svg.classList.remove('frozen'); clearInterval(choiceTimer); };
  const offerChoice = names => new Promise(res => {
    $('#choice-opts').innerHTML = names.map(n => `<button data-branch="${n}">${T(BRANCHES[n].label)}</button>`).join('');
    choiceEl.classList.add('open'); svg.classList.add('frozen'); hush();
    const total = 8; let left = total; const cd = $('#choice-cd'), ring = $('#choice-ring'); cd.textContent = left; ring.style.setProperty('--p', 100);
    const t0 = performance.now(); let ringRaf = null;
    const tick = now => { const p = Math.max(0, 100 - (now - t0) / (total * 1000) * 100); ring.style.setProperty('--p', p); if (p > 0 && choiceEl.classList.contains('open')) ringRaf = requestAnimationFrame(tick); };
    const pick = (name, btn) => { clearInterval(choiceTimer); if (ringRaf) cancelAnimationFrame(ringRaf); svg.classList.remove('frozen');
      if (btn && !reduced) { btn.classList.add('picked'); sfx.done(); setTimeout(() => { btn.classList.remove('picked'); hideChoice(); res(name); }, 320); } else { hideChoice(); res(name); } };
    choiceEl.querySelectorAll('button[data-branch]').forEach(b => { b.onclick = () => pick(b.dataset.branch || null, b); });
    if (!playing) { const cd = $('#choice-cd'); cd.textContent = '…'; }
    if (playing) { choiceTimer = setInterval(() => { left--; cd.textContent = left; if (left <= 0) pick(null, null); }, 1000); ringRaf = requestAnimationFrame(tick); }
    else { cd.textContent = '…'; ring.style.setProperty('--p', 0); }
  });
  const takeBranch = (name, fromIdx) => { seq = buildSeq(name); renderTimeline(); idx = fromIdx; };

  // penutup
  const endingEl = $('#ending');
  const showEnding = () => {
    $('#end-outcome').textContent = T(S.outcome) || '';
    try { const w = new Set(JSON.parse(localStorage.getItem('selaras-watched') || '[]')); w.add(REEL.id); localStorage.setItem('selaras-watched', JSON.stringify([...w])); } catch (_) {}
    const nextReel = (typeof REELS !== 'undefined' ? REELS : []).filter(r => r.status === 'jadi').find(r => r.order > (REELS.find(x => x.id === REEL.id) || { order: -1 }).order);
    const nb = $('#end-next'); if (nextReel) { nb.hidden = false; nb.href = nextReel.file; nb.textContent = (LANG === 'en' ? 'Next reel: ' : 'Reel berikutnya: ') + T(nextReel.title) + ' ▶'; } else nb.hidden = true;
    const countUp = (sel, to, f) => { const e = $(sel); if (reduced) { e.textContent = f(to); return; } const t0 = performance.now(); const fr = now => { const t = Math.min(1, (now - t0) / 1100), ease = 1 - Math.pow(1 - t, 3); e.textContent = f(to * ease); if (t < 1) rafs.add(requestAnimationFrame(fr)); }; rafs.add(requestAnimationFrame(fr)); };
    countUp('#end-u', S.latU || 0, fmtMs); countUp('#end-s', S.latS || 0, fmtMs);
    countUp('#end-tx', S.txCount || 0, v => Math.round(v)); countUp('#end-msg', S.msgCount || 0, v => Math.round(v)); countUp('#end-comp', order.filter(x => NODES[x]).length, v => Math.round(v));
    endingEl.querySelectorAll('.stats div').forEach((d, i) => d.style.setProperty('--i', i));
    endingEl.querySelectorAll('.confetti').forEach(c => c.remove());
    if (!(branchName && BRANCHES[branchName].fail) && !reduced) { const colors = ['var(--accent)', 'var(--ok)', 'var(--event)', 'var(--ext)']; for (let i = 0; i < 28; i++) { const c = document.createElement('i'); c.className = 'confetti'; const ang = Math.random() * Math.PI * 2, dist = 140 + Math.random() * 260; c.style.setProperty('--dx', Math.cos(ang) * dist + 'px'); c.style.setProperty('--dy', Math.sin(ang) * dist - 80 + 'px'); c.style.setProperty('--rot', (Math.random() * 720 - 360) + 'deg'); c.style.background = colors[i % 4]; c.style.animationDelay = (Math.random() * 120) + 'ms'; endingEl.appendChild(c); } }
    $('#end-branch').textContent = branchName ? 'cabang: ' + T(BRANCHES[branchName].label) : 'jalur normal';
    endingEl.classList.add('open'); sfx.end(); if (!(branchName && BRANCHES[branchName].fail)) music.bright();
    $('#end-branches').innerHTML = Object.keys(BRANCHES).map(n => `<button class="btn" data-restart="${n}">${LANG === 'en' ? 'Try: ' : 'Coba: '}${T(BRANCHES[n].label)}</button>`).join('');
    endingEl.querySelectorAll('[data-restart]').forEach(b => b.addEventListener('click', () => restart(b.dataset.restart))); speak((LANG === 'en' ? 'Done. ' : 'Selesai. ') + (T(S.outcome) || ''));
  };
  const hideEnding = () => endingEl.classList.remove('open');
  const creditsEl = $('#credits');
  const showCredits = () => {
    const refs = [...new Set(seq.flatMap(s => s.refs || []))];
    const files = refs.filter(r => !/^ADR-/.test(r)), adrs = refs.filter(r => /^ADR-/.test(r));
    $('#roll').innerHTML = `<h3>Selaras Flow Reel</h3><p>Alur 04 · Personalisasi laporan lewat LLM${branchName ? ' · cabang ' + T(BRANCHES[branchName].label) : ''}</p>
      <h4>Berkas sumber yang dilewati alur ini</h4>${files.map(f => `<p><code>${f}</code></p>`).join('')}
      <h4>Keputusan arsitektur</h4>${adrs.map(a => `<p><code>${a}</code></p>`).join('')}
      <h4>Angka</h4><p>Latensi ilustratif berskala dari <code>docs/performance-report.md</code></p><p>Pesan log dan nama span diambil apa adanya dari kode Go</p>
      <h4>Dibangun dengan</h4><p>SVG + Web Audio + Web Speech · satu berkas HTML tanpa dependensi</p><p style="margin-top:1.4rem">selaras-platform-go · 2026</p>`;
    $('#roll').style.setProperty('--dur', (14 + files.length * 0.9 + adrs.length * 0.6) + 's');
    hideEnding(); creditsEl.classList.add('open'); music.level(0.04, 1);
    const t = setTimeout(hideCredits, (16 + files.length * 0.9 + adrs.length * 0.6) * 1000); timers.add(t);
  };
  const hideCredits = () => { creditsEl.classList.remove('open'); camera(OVERVIEW, 800); };
  $('#end-credits').addEventListener('click', showCredits);
  $('#credits-skip').addEventListener('click', hideCredits);

  // bilah isi bergerak kontinu selama langkah berjalan; saat jeda dibekukan di posisi tampilnya
  const fillEl = $('#fill');
  const setFill = (pct, ms) => { if (ms === 0) { const cur = getComputedStyle(fillEl).width; fillEl.style.transition = 'none'; fillEl.style.width = cur; void fillEl.offsetWidth; fillEl.style.width = pct + '%'; return; } fillEl.style.transition = `width ${ms}ms linear`; fillEl.style.width = pct + '%'; };
  const freezeFill = () => { const cur = getComputedStyle(fillEl).width; fillEl.style.transition = 'none'; fillEl.style.width = cur; };
  let curChapter = -1;
  const chapterEl = $('#chapter'), ltEl = $('#lt');
  const setLowerThird = ch => { $('#lt-n').textContent = 'Bab ' + (ch + 1); $('#lt-title').textContent = T(CHAPTERS[ch].name); ltEl.classList.add('on'); };
  const chapterCard = ch => new Promise(res => { $('#ch-n').textContent = 'BAB ' + (ch + 1) + ' DARI 4'; $('#ch-title').textContent = T(CHAPTERS[ch].name); $('#ch-sub').textContent = T(CHAPTERS[ch].sub); chapterEl.classList.add('open'); sfx.step(); speak(T(CHAPTERS[ch].name)); const t = setTimeout(() => { chapterEl.classList.remove('open'); res(); }, reduced ? 200 : 1700); timers.add(t); });
  if (!STEPS.some(s => s.lat) && !Object.values(BRANCHES).some(b => b.steps.some(s => s.lat))) { const l = $('#lat'); l.hidden = true; l.previousElementSibling && (l.previousElementSibling.hidden = true); }
  const show = async (i, animate) => {
    stopAnim(); hideChoice(); hideEnding(); creditsEl.classList.remove('open'); chapterEl.classList.remove('open'); document.body.classList.remove('ended'); const g0 = gen;
    const jump = i !== idx + 1;
    if (jump) { svg.classList.add('jumpfade'); rebuild(i); svg.classList.add('noanim'); paint(S, order); void svg.getBoundingClientRect(); svg.classList.remove('noanim'); setLat(S, false); const tf = setTimeout(() => svg.classList.remove('jumpfade'), 260); timers.add(tf); }
    idx = i;
    const step = seq[i];
    caption(i, step);
    const elapsed = seq.slice(0, i).reduce((a, s) => a + durOf(s), 0), total = totalOf();
    setFill(elapsed / total * 100, 0);
    if (animate) setFill((elapsed + durOf(step)) / total * 100, durOf(step) * 1000);
    $('#time').textContent = `${fmt(elapsed)} / ${fmt(total)}`;
    document.querySelectorAll('.timeline .mk').forEach((m, k) => { m.className = 'mk' + (seq[k] && seq[k].branchStep ? ' br' : '') + (k < i ? ' done' : k === i ? ' cur' : ''); });
    const hash = `#${branchName ? branchName + '/' : ''}${i + 1}`; if (location.hash !== hash) history.replaceState(null, '', hash);
    const finish = () => { applyStep(S, step); S.shown.forEach(id => { if (!order.includes(id)) order.push(id); }); paint(S, order); ping(); renderDetail(step); setLat(S, true); };
    if (step.ch !== curChapter && !animate) { curChapter = step.ch; setLowerThird(step.ch); }
    if (!animate) { finish(); openPanel(PANEL[step.at] ? step.at : null); if (i === seq.length - 1) { camera(OVERVIEW, jump ? 420 : 0); showEnding(); } else camera(procView(step), jump ? 420 : 0); return; }

    if (step.ch !== curChapter) { curChapter = step.ch; setLowerThird(step.ch); if (i > 0 || true) { await chapterCard(step.ch); if (g0 !== gen) return; } }
    music.setMood(branchName && BRANCHES[branchName].fail && step.ch >= 2 ? 'minor' : 'major');
    reveal(step);
    openPanel(null);
    sfx.step();
    const titleSaid = speak(T(step.title));
    if (step.travel) { await camera(travelView(step.travel), 700); if (g0 !== gen) return; await travel(step.travel); if (g0 !== gen) return; }
    focusNow(step.at);
    if (PANEL[step.at]) openPanel(step.at);
    await camera(procView(step), 650); if (g0 !== gen) return;
    await titleSaid; if (g0 !== gen) return;
    const items = step.proc || []; const goN = items.reduce((a, p) => a + (p.go ? p.go.length : 0), 0);
    if (!goN) pushIn(cam, items.length * 1150 + 600); // dorong masuk pelan selama proses tanpa perjalanan titik
    await proc(step.at, items, g0); if (g0 !== gen) return;
    if (step.after) { const P = JSON.parse(JSON.stringify(S)); step.mut(P); P.edges.forEach(e => EDGE_EL[e].classList.add('shown')); await camera(travelView(step.after), 700); if (g0 !== gen) return; await travel(step.after); if (g0 !== gen) return; }
    finish();
    if (step.after || (S.focus && S.focus !== step.at)) await camera(focusView(S.focus), 650);
    if (g0 !== gen) return;
    // keterangan "selesai" muncul di tempat kamera berhenti: fokus akhir bila berbeda dari tempat prosesnya (hasilnya mendarat di sana)
    if (step.done) { sfx.done(); const target = (S.focus && S.focus !== step.at) ? S.focus : step.at; const last = target === step.at ? (step.proc || []).map(p => typeof p === 'string' ? null : p.b).filter(b => b != null).pop() : null; const c = callout(target, T(step.done), 'ok', last); const t = setTimeout(() => dismiss(c), 1400); timers.add(t); await wait(500); if (g0 !== gen) return; }
    const offers = (!branchName && seq === STEPS) ? branchAt(i) : (!branchName ? branchAt(i) : []);
    if (offers.length) { // tawarkan cabang yang terpasang di langkah ini
      const name = await offerChoice(offers); if (g0 !== gen) return;
      if (name) { takeBranch(name, i); }
    }
    if (i === seq.length - 1) { await wait(900); if (g0 !== gen) return; document.body.classList.add('ended'); await camera(OVERVIEW, 1200); if (g0 === gen) showEnding(); return; } // kartu Rincian disembunyikan saat kamera mundur ke penutup
    if (playing) { await wait(Math.min(3000, 800 + step.text.length * 10)); if (g0 === gen && playing) next(); }
  };
  const next = () => { if (idx + 1 >= seq.length) { setPlaying(false); return; } show(idx + 1, true); };
  const prev = () => { setPlaying(false); if (idx > 0) show(idx - 1, false); };
  const goTo = i => { setPlaying(false); show(i, false); };
  const setPlaying = p => { playing = p; $('#b-play').textContent = p ? '❚❚' : '▶'; if (!p) freezeFill(); };
  const play = () => { if (idx + 1 >= seq.length) { idx = -1; seq = buildSeq(null); renderTimeline(); } setPlaying(true); if (idx < 0) show(0, true); else next(); };
  const restart = name => { hideEnding(); seq = buildSeq(name || null); renderTimeline(); idx = -1; play(); };

  const tl = $('#timeline');
  const renderTimeline = () => { tl.querySelectorAll('.mk').forEach(m => m.remove()); const total = totalOf(); seq.forEach((s, k) => { const m = document.createElement('i'); m.className = 'mk fresh'; m.title = `${k + 1}. ${T(s.title)}`; m.style.setProperty('--i', k); const at = seq.slice(0, k).reduce((a, x) => a + durOf(x), 0); m.style.left = (at / total * 100) + '%'; m.addEventListener('click', ev => { ev.stopPropagation(); goTo(k); }); tl.appendChild(m); }); };
  renderTimeline();
  tl.addEventListener('click', ev => { const r = tl.getBoundingClientRect(); const total = totalOf(); const f = (ev.clientX - r.left) / r.width * total; let acc = 0, k = 0; for (; k < seq.length; k++) { acc += durOf(seq[k]); if (acc > f) break; } goTo(Math.min(k, seq.length - 1)); });
  $('#b-play').addEventListener('click', () => playing ? setPlaying(false) : play());
  $('#b-next').addEventListener('click', () => { setPlaying(false); next(); });
  $('#b-prev').addEventListener('click', prev);
  $('#end-replay').addEventListener('click', () => restart(null));

  const bDetail = $('#b-detail'), bCode = $('#b-code');
  if (REEL.tier === 3) { bCode.setAttribute('aria-pressed', 'true'); }
  const setDetail = on => { bDetail.setAttribute('aria-pressed', on); $('#detail').classList.toggle('open', on); };
  if (REEL.tier === 3) setDetail(true);
  const setCode = on => { showCode = on; bCode.setAttribute('aria-pressed', on); if (on) setDetail(true); if (idx >= 0) renderDetail(seq[idx]); };
  bDetail.addEventListener('click', () => setDetail(bDetail.getAttribute('aria-pressed') !== 'true'));
  bCode.addEventListener('click', () => setCode(!showCode));
  const THEMES = ['', 'dark', 'light']; let ti = 0;
  const applyTheme = t => { if (t) document.documentElement.setAttribute('data-theme', t); else document.documentElement.removeAttribute('data-theme'); try { localStorage.setItem('selaras-theme', t); } catch (_) {} };
  // transisi warna disediakan CSS pada body/panel/kanvas; tidak perlu kelas tambahan
  try { ti = Math.max(0, THEMES.indexOf(localStorage.getItem('selaras-theme') || '')); } catch (_) {}
  applyTheme(THEMES[ti]);
  $('#b-theme').addEventListener('click', () => { ti = (ti + 1) % THEMES.length; applyTheme(THEMES[ti]); });
  const bMore = $('#b-more'), menu = $('#menu');
  const setMenu = on => { menu.classList.toggle('open', on); bMore.setAttribute('aria-expanded', on); };
  bMore.addEventListener('click', e => { e.stopPropagation(); setMenu(!menu.classList.contains('open')); });
  document.addEventListener('click', e => { if (!menu.contains(e.target)) setMenu(false); });
  document.addEventListener('keydown', e => {
    if (e.target.matches('input, textarea, select')) return;
    if (e.key === ' ') { e.preventDefault(); playing ? setPlaying(false) : play(); }
    else if (e.key === 'ArrowRight') { setPlaying(false); next(); }
    else if (e.key === 'ArrowLeft') prev();
    else if (e.key === 'r' || e.key === 'R') bDetail.click();
    else if (e.key === 'k' || e.key === 'K') setCode(!showCode);
    else if (e.key === 'd' || e.key === 'D') $('#b-theme').click();
    else if (e.key === 's' || e.key === 'S') setSfx(!sfxOn);
    else if (e.key === 'n' || e.key === 'N') setVoice(!voiceOn);
    else if (e.key === 'm' || e.key === 'M') setMusic(!musicOn);
    else if (e.key === 'l' || e.key === 'L') setLang(LANG === 'id' ? 'en' : 'id');
    else if (e.key === 'Escape') { setDetail(false); hideEnding(); setMenu(false); }
  });
  // #7 → langkah 7 jalur normal; #kuota/15 → langkah 15 pada urutan bercabang
  const m = /^#(?:([a-z][a-z0-9-]*)\/)?(\d+)$/.exec(location.hash || '');
  if (m && m[1] && !BRANCHES[m[1]]) m[1] = null;
  const intro = $('#intro');
  const openWithIntro = () => { intro.classList.add('open'); paint(S, order); setView(OVERVIEW); const go = () => { intro.classList.remove('open'); intro.onclick = null; play(); }; const t = setTimeout(go, reduced ? 300 : 2600); intro.onclick = () => { clearTimeout(t); go(); }; };
  if (m) { seq = buildSeq(m[1] || null); renderTimeline(); const h = parseInt(m[2], 10); if (h >= 1 && h <= seq.length) show(h - 1, false); else openWithIntro(); } else openWithIntro();
})();
