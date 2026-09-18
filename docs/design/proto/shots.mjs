// Comparison screenshots for the 0.4.0 prototype: old dashboard (index.html?mock=1) and prototype
// (proto.html?mock=1) at 1920 / 1280 / 375 px, dark and light, every page — plus the prototype's
// variants (nav rail / top bar, no sparklines, one channel at a time, Compatibility folded into About).
// Same harness as docs/screenshots/shots.mjs (Chrome headless over the DevTools protocol).
// Usage: node docs/design/proto/shots.mjs <outDir> <repoBaseURL> <cdpPort>
//   e.g. node docs/design/proto/shots.mjs ../n5-fangov-redesign/shots http://127.0.0.1:8797 9223
// Writes <outDir>/<name>.png and <outDir>/index.json (the list compare.html renders).
import { writeFileSync, mkdirSync } from 'node:fs';
import { join } from 'node:path';

const [outDir, BASE, PORT] = process.argv.slice(2);
mkdirSync(outDir, { recursive: true });
const sleep = ms => new Promise(r => setTimeout(r, ms));
const OLD = BASE + '/internal/web/static/index.html', NEW = BASE + '/docs/design/proto/proto.html';

const ver = await (await fetch(`http://127.0.0.1:${PORT}/json/version`)).json();
const ws = new WebSocket(ver.webSocketDebuggerUrl);
await new Promise((res, rej) => { ws.onopen = res; ws.onerror = rej; });
let id = 0; const pending = new Map(); const listeners = [];
ws.onmessage = ev => { const m = JSON.parse(ev.data);
	if (m.id && pending.has(m.id)) { const { res, rej } = pending.get(m.id); pending.delete(m.id); m.error ? rej(new Error(m.error.message)) : res(m.result); }
	else if (m.method) for (const l of listeners) l(m); };
const send = (method, params = {}, sessionId) => new Promise((res, rej) => { const i = ++id; pending.set(i, { res, rej }); ws.send(JSON.stringify({ id: i, method, params, sessionId })); });
const waitEvent = (method, sessionId) => new Promise(res => { const l = m => { if (m.method === method && m.sessionId === sessionId) { listeners.splice(listeners.indexOf(l), 1); res(m.params); } }; listeners.push(l); });

const { targetId } = await send('Target.createTarget', { url: 'about:blank' });
const { sessionId: S } = await send('Target.attachToTarget', { targetId, flatten: true });
await send('Page.enable', {}, S); await send('Runtime.enable', {}, S);
const evalJS = async expr => { const r = await send('Runtime.evaluate', { expression: expr, awaitPromise: true, returnByValue: true }, S); if (r.exceptionDetails) throw new Error('JS: ' + JSON.stringify(r.exceptionDetails.exception?.description || r.exceptionDetails.text)); return r.result.value; };
const hideToasts = () => evalJS(`document.querySelectorAll('.toast').forEach(t => t.remove()); 1`);
let VW = 1280, VH = 800;
const viewport = (w, h) => { VW = w; VH = h; return send('Emulation.setDeviceMetricsOverride', { width: w, height: h, deviceScaleFactor: 1, mobile: w < 700 }, S); };
const nav = async (url, wait = 1400) => { const loaded = waitEvent('Page.loadEventFired', S); await send('Page.navigate', { url }, S); await loaded; await sleep(wait); await hideToasts(); };
const click = sel => evalJS(`(()=>{const e=document.querySelector(${JSON.stringify(sel)}); if(!e) throw new Error('no '+${JSON.stringify(sel)}); e.click(); return 1;})()`);
const setLS = obj => evalJS(`localStorage.setItem('n5-fangov', ${JSON.stringify(JSON.stringify(obj))}); 1`);
const index = [];
// full-page capture: the viewport grows to the document height so 100vh elements (sidebar) span the whole image
const shot = async (name, meta, o = {}) => {
	await sleep(o.settle ?? 300); await hideToasts();
	const hgt = Math.min(o.h || await evalJS('Math.max(document.documentElement.scrollHeight, document.body.scrollHeight)'), 3600);
	const w = VW, h0 = VH; if (hgt > h0 && !o.viewportOnly) { await viewport(w, hgt); await sleep(350); await evalJS('window.dispatchEvent(new Event("resize")); 1'); await sleep(250); }
	const r = await send('Page.captureScreenshot', { format: 'png', captureBeyondViewport: true, clip: { x: 0, y: 0, width: w, height: o.viewportOnly ? h0 : hgt, scale: 1 } }, S);
	writeFileSync(join(outDir, name + '.png'), Buffer.from(r.data, 'base64')); index.push({ file: name + '.png', ...meta }); console.log('wrote', name);
	if (hgt > h0 && !o.viewportOnly) await viewport(w, h0);
};

const SIZES = [[1920, 1080], [1280, 800], [375, 812]], THEMES = ['dark', 'light'];
// page keys shared by both generations (the comparison pairs them); old pages via &tab=, new via &page=
const PAGES = [
	{ key: 'overview-anon', t: 'Overview (anonymous)', old: '?mock=1', neu: '?mock=1' },
	{ key: 'overview', t: 'Overview (signed in)', old: '?mock=1&user=1', neu: '?mock=1&user=1' },
	{ key: 'fans', t: 'Fans = Curves + Manual + Presets', old: '?mock=1&user=1&tab=curves', neu: '?mock=1&user=1&page=fans', oldExtra: [['manual', 'Manual (old)', '?mock=1&user=1&tab=manual'], ['presets', 'Presets + Schedules (old)', '?mock=1&user=1&tab=presets']] },
	{ key: 'schedules', t: 'Schedules', old: '?mock=1&user=1&tab=presets', neu: '?mock=1&user=1&page=schedules' },
	{ key: 'alerts', t: 'Alerts', old: '?mock=1&user=1&tab=alerts', neu: '?mock=1&user=1&page=alerts' },
	{ key: 'system', t: 'System', old: '?mock=1&user=1&tab=system', neu: '?mock=1&user=1&page=system' },
	{ key: 'log', t: 'Log', old: '?mock=1&user=1&tab=log', neu: '?mock=1&user=1&page=log' },
	{ key: 'settings', t: 'Settings = gear + Account + Certificate + transport', old: '?mock=1&user=1', oldClick: '#h-settings', neu: '?mock=1&user=1&page=settings' },
	{ key: 'compat', t: 'Compatibility', old: '?mock=1&user=1&tab=compat', neu: '?mock=1&user=1&page=compat' },
	{ key: 'about', t: 'About', old: '?mock=1&user=1&tab=about', neu: '?mock=1&user=1&page=about' },
	{ key: 'login', t: 'Sign-in dialog', old: '?mock=1', oldClick: '#h-signin', neu: '?mock=1&dlg=login' },
];
try {
	for (const [w, hh] of SIZES) for (const theme of THEMES) {
		await viewport(w, hh);
		await nav(OLD + '?mock=1', 600); await setLS({ unit: 'C', interval: 5, theme, range: '2h' });
		for (const p of PAGES) {
			const meta = { key: p.key, title: p.t, w, theme };
			await nav(OLD + p.old); if (p.oldClick) { await click(p.oldClick); await sleep(400); }
			await shot(`old-${p.key}-${w}-${theme}`, { ...meta, gen: 'old' }, { viewportOnly: !!p.oldClick });
			for (const [k, t, q] of p.oldExtra || []) { await nav(OLD + q); await shot(`old-${k}-${w}-${theme}`, { key: p.key, title: t, w, theme, gen: 'old', extra: k }); }
			await nav(NEW + p.neu + '&theme=' + theme); await shot(`new-${p.key}-${w}-${theme}`, { ...meta, gen: 'new' }, { viewportOnly: p.key === 'login' });
		}
		if (w === 375) { await nav(NEW + '?mock=1&user=1&page=overview&theme=' + theme + '&dlg=more'); await shot(`new-more-${w}-${theme}`, { key: 'more', title: 'Phone: More sheet', w, theme, gen: 'new' }, { viewportOnly: true }); }
	}
	// old dialogs the Settings page absorbs (1280 dark)
	await viewport(1280, 800); await nav(OLD + '?mock=1', 600); await setLS({ unit: 'C', interval: 5, theme: 'dark', range: '2h' });
	await nav(OLD + '?mock=1&user=1'); await click('#h-settings'); await sleep(300); await click('#s-acc'); await sleep(500); await shot('old-account-1280-dark', { key: 'settings', title: 'Account dialog (old)', w: 1280, theme: 'dark', gen: 'old', extra: 'account' }, { viewportOnly: true });
	await nav(OLD + '?mock=1&user=1'); await click('#h-sec'); await sleep(500); await shot('old-cert-1280-dark', { key: 'settings', title: 'Certificate dialog (old)', w: 1280, theme: 'dark', gen: 'old', extra: 'cert' }, { viewportOnly: true });
	// prototype variants (1280 dark) — the open decisions
	const V = [
		['nav-rail', 'Variant: icon rail (collapsed sidebar)', '?mock=1&user=1&page=overview&nav=rail'],
		['nav-top', 'Variant: top bar instead of sidebar', '?mock=1&user=1&page=overview&nav=top'],
		['nav-top-fans', 'Variant: top bar, Fans page', '?mock=1&user=1&page=fans&nav=top'],
		['nospark', 'Variant: tiles without sparklines', '?mock=1&user=1&page=overview&spark=0'],
		['fans-tabs', 'Variant: one channel at a time (selector)', '?mock=1&user=1&page=fans&fans=tabs'],
		['compat-about', 'Variant: Compatibility folded into About', '?mock=1&user=1&page=about&compat=about'],
		['preset-editor', 'Preset editor dialog (gate finding)', '?mock=1&user=1&page=fans&dlg=preset'],
		['cert-warn', 'Header: certificate warning chip (tls=soon)', '?mock=1&user=1&page=overview&tls=soon'],
		['pwm4', 'Four channels (&pwm4=1)', '?mock=1&user=1&page=overview&pwm4=1'],
	];
	for (const [k, t, q] of V) { await nav(NEW + q + '&theme=dark'); await shot(`var-${k}-1280-dark`, { key: k, title: t, w: 1280, theme: 'dark', gen: 'variant' }, { viewportOnly: /preset-editor|cert-warn/.test(k) }); }
	await viewport(375, 812); await nav(NEW + '?mock=1&user=1&page=fans&fans=tabs&theme=dark'); await shot('var-fans-tabs-375-dark', { key: 'fans-tabs', title: 'Variant: one channel at a time, phone', w: 375, theme: 'dark', gen: 'variant' });
	writeFileSync(join(outDir, 'index.json'), JSON.stringify(index, null, 1));
} finally { await send('Target.closeTarget', { targetId }); ws.close(); }
