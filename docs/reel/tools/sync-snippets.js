// Periksa cuplikan kode (CODE.*) di sebuah reel terhadap sumber di repo.
//
// Cuplikan di reel diringkas tangan (statement digabung, baris kosong dibuang), jadi ia tidak disalin ulang dari
// sumber. Yang diperiksa dan diperbaiki mekanis hanya dua hal:
//   1. rujukan `file: 'path:baris'` - baris pertama cuplikan (kode, bukan komentar) dicari di sumber sekarang dan
//      nomor barisnya diperbarui; baris yang tidak ketemu dilaporkan sebagai cuplikan yang perlu ditinjau;
//   2. setiap baris kode (bukan komentar) di cuplikan harus punya padanan di sumber: baris sumber yang, setelah
//      spasi dirapikan, memuat teks baris cuplikan itu - kalau tidak, kodenya sudah berubah dan cuplikan basi.
//
// Jalankan dari folder docs/reel:  node tools/sync-snippets.js <reel.html> <path-repo> [--check]
//   --check hanya melaporkan, tidak menulis rujukan baris yang diperbarui.
const fs = require('fs'), path = require('path');
const args = process.argv.slice(2);
const reelFile = args[0], repo = path.resolve(args[1] || '.'); const check = args.includes('--check');
if (!reelFile || !args[1]) { console.error('pakai: node tools/sync-snippets.js <reel.html> <path-repo> [--check]'); process.exit(2); }

const strip = s => s.replace(/<\/?[bc]>/g, '').replace(/&lt;/g, '<').replace(/&gt;/g, '>').replace(/&amp;/g, '&');
// baris yang tidak layak jadi bukti: penanda ringkasan (…), kurung penutup, atau terlalu pendek
const trivial = l => /…/.test(l) || /^[{}()\];,\s]*$/.test(l) || l.length < 16;
const unesc = s => s.replace(/\\`/g, '`').replace(/\\\$\{/g, '${').replace(/\\\\/g, '\\');
const squash = s => s.replace(/\s+/g, ' ').trim();
const commentMark = f => f.endsWith('.sql') ? '--' : '//';
const dropComment = (l, m) => { const i = l.indexOf(m); return (i >= 0 ? l.slice(0, i) : l); };

let repoSet = null;
const repoLines = () => {
  if (repoSet) return repoSet; repoSet = new Set();
  const walk = d => { for (const f of fs.readdirSync(d, { withFileTypes: true })) { const p = path.join(d, f.name); if (f.isDirectory()) { if (!/^(\.git|gen|vendor|node_modules|testdata)$/.test(f.name)) walk(p); } else if (/\.(go|sql)$/.test(f.name)) { const m = commentMark(f.name); for (const l of fs.readFileSync(p, 'utf8').split('\n')) { const s = squash(dropComment(l, m)); if (s) repoSet.add(s); } } } };
  for (const d of ['internal', 'cmd', 'migrations']) { const p = path.join(repo, d); if (fs.existsSync(p)) walk(p); }
  return repoSet;
};

let html = fs.readFileSync(reelFile, 'utf8');
const re = /(\b\w+): \{ file: '([^']+)', src: `((?:[^`\\]|\\[\s\S])*)` \}/g;
let updated = 0, stale = 0;
html = html.replace(re, (whole, key, fileRef, rawSrc) => {
  const rel = fileRef.replace(/:\d+$/, ''); const mark = commentMark(rel);
  const p = path.join(repo, rel);
  if (!fs.existsSync(p)) { stale++; console.log(`  BASI ${key}: ${rel} tidak ada`); return whole; }
  const source = fs.readFileSync(p, 'utf8').split('\n');
  const squashed = source.map(l => squash(dropComment(l, mark)));
  const lines = strip(unesc(rawSrc)).split('\n').map(l => dropComment(l, mark)).map(squash).filter(Boolean);
  // 1. rujukan baris: baris kode pertama cuplikan; statement yang digabung dicocokkan lewat awalannya
  // baris pertama yang bermakna (bukan "{" atau "…") dicari; rujukan menunjuk ke baris pertama cuplikan yang sebenarnya
  const lead = lines.findIndex(l => !trivial(l)); const first = lines[Math.max(lead, 0)]; let at = -1;
  for (let i = 0; i < source.length && at < 0; i++) { const s = squashed[i]; if (s && (s === first || (s.length >= 12 && first.startsWith(s)) || first.startsWith(s.replace(/[({,]\s*$/, '').trim()) && s.length >= 12)) at = i; }
  if (at < 0) { stale++; console.log(`  BASI ${key} (${fileRef}): baris pertama tidak ketemu: ${first.slice(0, 60)}`); return whole; }
  at = Math.max(0, at - Math.max(lead, 0));
  // 2. tiap baris kode punya padanan: teks cuplikan (yang mungkin gabungan) memuat baris sumber, atau sebaliknya
  const window = squashed.slice(at, Math.min(source.length, at + lines.length * 4)).filter(Boolean);
  // cuplikan boleh menggabungkan beberapa berkas (mis. worker.go + guard.go): baris yang tidak ada di jendela berkas ini
  // dicari di seluruh sumber Go/SQL repo sebelum dinyatakan basi
  // baris yang digabung tangan ("if x { return y }") diperiksa per penggalan; literal string dipotong di kutip pertama
  // karena cuplikan boleh meringkasnya
  const found = l => window.some(s => s === l || l.includes(s) && s.length >= 8 || s.includes(l) && l.length >= 8) || repoLines().has(l);
  const fragments = l => l.split(/[{};]/).map(f => squash(f.replace(/"[\s\S]*$/, ''))).filter(f => !trivial(f));
  const missing = lines.filter(l => !trivial(l) && !found(l) && !fragments(l).every(f => found(f) || [...repoLines()].some(s => s.startsWith(f))));
  if (missing.length) { stale++; console.log(`  BASI ${key} (${fileRef}): ${missing.length} baris kode tidak ketemu, mis. ${missing[0].slice(0, 60)}`); }
  const newRef = `${rel}:${at + 1}`;
  if (newRef === fileRef) return whole;
  updated++; console.log(`  ${key}: ${fileRef} -> ${newRef}`);
  return whole.replace(`file: '${fileRef}'`, `file: '${newRef}'`);
});
console.log(`${path.basename(reelFile)}: ${updated} rujukan baris diperbarui, ${stale} cuplikan basi${check ? ' (check saja)' : ''}`);
if (!check && updated) fs.writeFileSync(reelFile, html);
process.exit(stale ? 1 : 0);
