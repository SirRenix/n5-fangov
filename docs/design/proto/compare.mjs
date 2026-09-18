// Builds the old/new comparison page from the screenshots shots.mjs wrote.
// Usage: node docs/design/proto/compare.mjs <shotsDir> <outHtml>   (image paths are relative to outHtml)
import { readFileSync, writeFileSync } from 'node:fs';
import { relative, dirname, join } from 'node:path';

const [shotsDir, outHtml] = process.argv.slice(2);
const idx = JSON.parse(readFileSync(join(shotsDir, 'index.json'), 'utf8'));
const rel = f => relative(dirname(outHtml), join(shotsDir, f)).replace(/\\/g, '/');
const esc = s => String(s).replace(/[&<>"]/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c]));
const ORDER = ['overview-anon', 'overview', 'fans', 'schedules', 'alerts', 'system', 'log', 'settings', 'compat', 'about', 'login', 'more'];
const title = k => (idx.find(i => i.key === k && !i.extra) || { title: k }).title;
const img = i => `<figure data-w="${i.w}" data-theme="${i.theme}"><img loading="lazy" src="${rel(i.file)}" alt="${esc(i.title)} ${i.w} ${i.theme}" data-full="${rel(i.file)}"><figcaption>${esc(i.title)} · ${i.w} px · ${i.theme}</figcaption></figure>`;
let body = '';
for (const k of ORDER) { const rows = idx.filter(i => i.key === k && i.gen !== 'variant'); if (!rows.length) continue;
	body += `<section id="s-${k}"><h2>${esc(title(k))}</h2><div class="pair"><div class="col old"><h3>0.3.1 (current)</h3>${rows.filter(i => i.gen === 'old').map(img).join('') || '<p class="none">— (no counterpart)</p>'}</div><div class="col new"><h3>0.4.0 prototype</h3>${rows.filter(i => i.gen === 'new').map(img).join('')}</div></div></section>`; }
const V = idx.filter(i => i.gen === 'variant');
const base = (key, w = 1280) => idx.find(i => i.gen === 'new' && i.key === key && i.w === w && i.theme === 'dark');
const VS = [['nav-rail', 'overview', 'Sidebar expanded (base) vs. icon rail'], ['nav-top', 'overview', 'Sidebar (base) vs. top bar'], ['nav-top-fans', 'fans', 'Sidebar (base) vs. top bar — Fans page'], ['nospark', 'overview', 'Sparklines (base) vs. none'], ['fans-tabs', 'fans', 'All channels stacked (base) vs. one at a time'], ['compat-about', 'about', 'Compatibility page (base: separate) vs. folded into About']];
body += `<section id="s-variants"><h2>Open decisions — variants (1280 px, dark)</h2>`;
for (const [k, b, t] of VS) { const v = V.find(i => i.key === k && i.w === 1280), bs = base(b); if (!v) continue;
	body += `<h3 class="vt">${esc(t)}</h3><div class="pair always"><div class="col">${bs ? img({ ...bs, title: 'base: ' + bs.title }) : ''}</div><div class="col">${img(v)}</div></div>`; }
const rest = V.filter(i => !VS.some(([k]) => k === i.key) || i.w !== 1280);
body += `<h3 class="vt">More prototype states</h3><div class="grid">${rest.map(img).join('')}</div></section>`;
const html = `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>n5-fangov 0.4.0 — old vs. new</title>
<style>
:root{color-scheme:dark;--bg:#0f1115;--bg2:#161a21;--line:#262c37;--fg:#e6e9ef;--fg2:#a3abb8;--info:#5aa9ff}
body{margin:0;background:var(--bg);color:var(--fg);font:14px/1.45 system-ui,Segoe UI,Roboto,sans-serif}
header{position:sticky;top:0;z-index:5;background:var(--bg2);border-bottom:1px solid var(--line);padding:10px 20px;display:flex;flex-wrap:wrap;gap:8px 20px;align-items:center}
header h1{font-size:16px;margin:0 12px 0 0}
.seg{display:inline-flex;border:1px solid var(--line);border-radius:6px;overflow:hidden}.seg button{background:none;border:0;color:var(--fg2);padding:4px 12px;cursor:pointer;font:inherit}.seg button.on{background:#1d222b;color:var(--fg)}
nav.toc{display:flex;flex-wrap:wrap;gap:4px 10px;font-size:12px}nav.toc a{color:var(--info);text-decoration:none}
main{padding:20px;max-width:1900px;margin:0 auto}
section{margin-bottom:40px}h2{font-size:18px;margin:0 0 8px;padding-top:8px;border-top:1px solid var(--line)}h3{font-size:12px;text-transform:uppercase;letter-spacing:.04em;color:var(--fg2);margin:8px 0}h3.vt{font-size:14px;text-transform:none;letter-spacing:0;color:var(--fg);margin-top:20px}
.pair{display:grid;grid-template-columns:1fr 1fr;gap:16px;align-items:start}.pair.always figure{display:block!important}
.col{min-width:0}figure{margin:0 0 12px;display:none}figure.show{display:block}
img{max-width:100%;height:auto;border:1px solid var(--line);border-radius:6px;background:#000;cursor:zoom-in;display:block}
.m375 img{max-width:375px}.m375 .pair{grid-template-columns:1fr 1fr}
figcaption{font-size:12px;color:var(--fg2);margin-top:4px}.none{color:var(--fg2)}
.grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(420px,1fr));gap:16px}
.hint{font-size:12px;color:var(--fg2)}
dialog.zoom{max-width:100vw;max-height:100vh;width:100vw;height:100vh;margin:0;padding:0;border:0;background:#000c;overflow:auto;cursor:zoom-out}dialog.zoom img{max-width:none;border:0;border-radius:0;cursor:zoom-out;margin:0 auto}
</style></head><body>
<header><h1>n5-fangov 0.4.0 — old vs. new</h1>
<div class="seg" id="w"><button data-w="1920">1920</button><button data-w="1280" class="on">1280</button><button data-w="375">375</button></div>
<div class="seg" id="t"><button data-t="dark" class="on">dark</button><button data-t="light">light</button></div>
<span class="hint">mock data · click an image for full size · variants at the end</span>
<nav class="toc">${ORDER.filter(k => idx.some(i => i.key === k)).map(k => `<a href="#s-${k}">${esc(title(k))}</a>`).join('')}<a href="#s-variants">Variants</a></nav></header>
<main>${body}</main>
<dialog class="zoom" id="z"><img alt=""></dialog>
<script>
const st={w:'1280',t:'dark'};const apply=()=>{document.body.classList.toggle('m375',st.w==='375');for(const f of document.querySelectorAll('figure'))f.classList.toggle('show',f.dataset.w===st.w&&f.dataset.theme===st.t);};
document.getElementById('w').addEventListener('click',e=>{const b=e.target.closest('button');if(!b)return;st.w=b.dataset.w;for(const x of b.parentNode.children)x.classList.toggle('on',x===b);apply();});
document.getElementById('t').addEventListener('click',e=>{const b=e.target.closest('button');if(!b)return;st.t=b.dataset.t;for(const x of b.parentNode.children)x.classList.toggle('on',x===b);apply();});
const z=document.getElementById('z');document.addEventListener('click',e=>{const i=e.target.closest('img[data-full]');if(i){z.querySelector('img').src=i.dataset.full;z.showModal();}});z.addEventListener('click',()=>z.close());
apply();
</script></body></html>`;
writeFileSync(outHtml, html); console.log('wrote', outHtml, idx.length, 'images');
