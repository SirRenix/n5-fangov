// Screenshot driver for docs/screenshots: Chrome headless over the DevTools protocol (0.4.0 pages).
// Usage: node docs/screenshots/shots.mjs <outDir> <mockBaseURL> <cdpPort>   (see README.md here)
// Pages are reached by hash (#fans, #settings/st-cert …); the mock's &tab= parameter still maps
// curves|manual|presets → fans and compat → about. Page shots are full-page captures: the viewport
// grows to the document height so the 100vh sidebar spans the whole image (docs/design/proto/shots.mjs);
// dialogs, the header and toasts are viewport clips.
import { writeFileSync, mkdirSync } from 'node:fs';
import { join } from 'node:path';

const [outDir, BASE, PORT] = process.argv.slice(2);
mkdirSync(outDir, { recursive: true });
const sleep = ms => new Promise(r => setTimeout(r, ms));

const ver = await (await fetch(`http://127.0.0.1:${PORT}/json/version`)).json();
const ws = new WebSocket(ver.webSocketDebuggerUrl);
await new Promise((res, rej) => { ws.onopen = res; ws.onerror = rej; });
let id = 0; const pending = new Map(); const listeners = [];
ws.onmessage = ev => {
	const m = JSON.parse(ev.data);
	if (m.id && pending.has(m.id)) { const { res, rej } = pending.get(m.id); pending.delete(m.id); m.error ? rej(new Error(m.error.message)) : res(m.result); }
	else if (m.method) for (const l of listeners) l(m);
};
const send = (method, params = {}, sessionId) => new Promise((res, rej) => { const i = ++id; pending.set(i, { res, rej }); ws.send(JSON.stringify({ id: i, method, params, sessionId })); });
const waitEvent = (method, sessionId) => new Promise(res => { const l = m => { if (m.method === method && m.sessionId === sessionId) { listeners.splice(listeners.indexOf(l), 1); res(m.params); } }; listeners.push(l); });

const { targetId } = await send('Target.createTarget', { url: 'about:blank' });
const { sessionId: S } = await send('Target.attachToTarget', { targetId, flatten: true });
await send('Page.enable', {}, S);
await send('Runtime.enable', {}, S);

const evalJS = async (expr) => { const r = await send('Runtime.evaluate', { expression: expr, awaitPromise: true, returnByValue: true }, S); if (r.exceptionDetails) throw new Error('JS: ' + JSON.stringify(r.exceptionDetails.exception?.description || r.exceptionDetails.text)); return r.result.value; };
const hideToasts = () => evalJS(`document.querySelectorAll('.toast').forEach(t => t.remove()); 1`);
let VW = 1280, VH = 800;
const viewport = (w, h) => { VW = w; VH = h; return send('Emulation.setDeviceMetricsOverride', { width: w, height: h, deviceScaleFactor: 1, mobile: w < 700 }, S); };
// nav: a fresh load every time — when only the hash differs from the current URL the browser would not reload
// (no load event), so such a step goes through about:blank first
const nav = async (q, opts = {}) => {
	const cur = await evalJS('location.href'), next = BASE + q;
	if (cur.split('#')[0] === next.split('#')[0]) { const b = waitEvent('Page.loadEventFired', S); await send('Page.navigate', { url: 'about:blank' }, S); await b; }
	const loaded = waitEvent('Page.loadEventFired', S);
	await send('Page.navigate', { url: next }, S);
	await loaded; await sleep(opts.wait ?? 1500); await hideToasts();
};
// shot: full page by default (viewport grows to the document height, capped), `clip` for a region, `view` for the viewport only
const shot = async (name, o = {}) => {
	await sleep(o.settle ?? 300); if (!o.keepToasts) await hideToasts();
	const p = { format: 'png' };
	if (o.clip) { const sy = await evalJS('scrollY'); p.clip = { ...o.clip, y: o.clip.y + sy, scale: 1 }; } // clips are document coordinates
	else if (o.view) { /* the visible viewport, wherever the page is scrolled */ }
	else {
		const hgt = Math.min(await evalJS('Math.max(document.documentElement.scrollHeight, document.body.scrollHeight)'), 3600), h0 = VH;
		if (hgt > h0) { await viewport(VW, hgt); await sleep(350); await evalJS('window.dispatchEvent(new Event("resize")); 1'); await sleep(250); }
		p.captureBeyondViewport = true; p.clip = { x: 0, y: 0, width: VW, height: hgt, scale: 1 };
		const r = await send('Page.captureScreenshot', p, S); writeFileSync(join(outDir, name), Buffer.from(r.data, 'base64')); console.log('wrote', name);
		if (hgt > h0) await viewport(VW, h0);
		return;
	}
	const r = await send('Page.captureScreenshot', p, S);
	writeFileSync(join(outDir, name), Buffer.from(r.data, 'base64'));
	console.log('wrote', name);
};
const click = sel => evalJS(`(()=>{const e=document.querySelector(${JSON.stringify(sel)}); if(!e) throw new Error('no '+${JSON.stringify(sel)}); e.click(); return 1;})()`);
const clickText = (sel, text) => evalJS(`(()=>{const e=[...document.querySelectorAll(${JSON.stringify(sel)})].find(x=>x.textContent.trim()===${JSON.stringify(text)}); if(!e) throw new Error('no '+${JSON.stringify(text)}); e.click(); return 1;})()`);
const input = (sel, value) => evalJS(`(()=>{const e=document.querySelector(${JSON.stringify(sel)}); if(!e) throw new Error('no '+${JSON.stringify(sel)}); e.value=${JSON.stringify(String(value))}; e.dispatchEvent(new Event('input',{bubbles:true})); e.dispatchEvent(new Event('change',{bubbles:true})); return 1;})()`);
const setLS = obj => evalJS(`localStorage.setItem('n5-fangov', ${JSON.stringify(JSON.stringify(obj))}); 1`);
const LS = { unit: 'C', interval: 5, theme: 'dark', range: '2h', nav: 'side' };

try {
	await viewport(1280, 800);
	// theme dark, 5 s interval, °C, 2 h, sidebar expanded — deterministic
	await nav('?mock=1', { wait: 600 }); await setLS(LS);

	// 01 overview anonymous (tiles + charts only)
	await nav('?mock=1'); await shot('01-overview-anonymous.png');
	// 02 login dialog
	await click('#h-signin'); await input('#l-user', 'admin'); await shot('02-login-dialog.png', { view: true });
	// 03 overview signed in, full page: tiles with sparklines, three charts, Sensors / System / Recent alerts
	await nav('?mock=1&user=1'); await shot('03-overview-signed-in.png');
	// 04 header crop: title, status chip, uptime, version + pre-release badge, live, user, Sign out
	await shot('04-header.png', { clip: { x: 0, y: 0, width: 1280, height: 60 } });
	// 05 sidebar collapsed to the icon rail (the panel-left toggle in the brand row; in the rail it stands under the logo and the header shows it too; state persists in localStorage)
	await click('#nav .tog'); await sleep(400); await shot('05-sidebar-rail.png', { view: true }); await setLS(LS);
	// 06 system page (full inventory tables)
	await nav('?mock=1&user=1#system'); await shot('06-system.png');
	// 07 fans page (full): channel cards with curve editor + Live & override, presets row, action bar
	await nav('?mock=1&user=1#fans'); await shot('07-fans.png');
	// 08 fans validation error: critical cleared, stop 300 → Apply → client-side notice, nothing sent
	await evalJS(`(()=>{const i=document.querySelector('#fan-cards input[type=text][placeholder=auto]'); i.value='300'; i.dispatchEvent(new Event('input',{bubbles:true})); const c=document.querySelector('#fan-cards input[type=number][min="30"]'); c.value=''; c.dispatchEvent(new Event('input',{bubbles:true})); return 1;})()`);
	await click('#cv-apply'); await sleep(300); await hideToasts(); await shot('08-fans-validation-error.png', { view: true });
	// 09 fans restart required: a real 202 through the mock (&restart=1); the notice stays until Revert
	await nav('?mock=1&user=1&restart=1#fans');
	await evalJS(`(()=>{const c=document.querySelector('#fan-cards input[type=number][min="30"]'); c.value=String(+c.value+1); c.dispatchEvent(new Event('input',{bubbles:true})); return 1;})()`);
	await click('#cv-apply'); await sleep(900); await hideToasts();
	await evalJS(`(()=>{const n=document.getElementById('cv-notice'); if(n.hidden||!/restart required/.test(n.textContent)) throw new Error('09: restart notice not shown after the 202'); return 1;})()`);
	await shot('09-fans-restart-required.png', { view: true });
	// 10 fans: the Auto / Manual switch of the hdd channel switched on, slider below 60 → minimum-60 hint
	await nav('?mock=1&user=1#fans');
	await evalJS(`(()=>{const card=[...document.querySelectorAll('#fan-cards .fan')].find(c=>/hdd/.test(c.textContent)); const sw=card.querySelector('[role=switch]'); if(sw.getAttribute('aria-checked')!=='true') sw.click(); return 1;})()`); await sleep(600); await hideToasts();
	await evalJS(`(()=>{const card=[...document.querySelectorAll('#fan-cards .fan')].find(c=>/hdd/.test(c.textContent)); const r=card.querySelector('input[type=range]'); r.value=40; r.dispatchEvent(new Event('input',{bubbles:true})); return 1;})()`);
	await evalJS(`document.querySelectorAll('#fan-cards .fan')[2]?.scrollIntoView(); 1`); await shot('10-fans-manual-switch.png', { view: true });
	// 11 preset editor dialog (New preset…), prefilled from the daemon's running curves
	await nav('?mock=1&user=1#fans'); await click('#ps-new'); await sleep(400); await input('#preset-ed input[type=text]', 'summer'); await shot('11-preset-editor.png', { view: true });
	// 12 schedules page: editable rows, Add entry, status block
	await nav('?mock=1&user=1#schedules'); await shot('12-schedules.png');
	// 13 schedules with a failed last switch (&schedfail=1): warn notice + error in the status block
	await nav('?mock=1&user=1&schedfail=1#schedules'); await shot('13-schedules-last-switch-failed.png');
	// 14 alerts page, 15 test-alert toast crop (lower right corner, the toast kept)
	await nav('?mock=1&user=1#alerts'); await shot('14-alerts.png');
	await click('#al-test'); await sleep(600); await shot('15-alerts-test-toast.png', { clip: { x: 640, y: 560, width: 640, height: 240 }, keepToasts: true });
	// 16 log
	await nav('?mock=1&user=1#log'); await shot('16-log.png');
	// 17 settings, full page: Display · Account & sessions · API tokens · Certificate · Alert transport · Backup · Danger zone
	await nav('?mock=1&user=1#settings'); await shot('17-settings.png');
	// 18 certificate section (deep link #settings/st-cert): automatic with "How to trust" open, then fallback / soon / off
	await nav('?mock=1&user=1#settings/st-cert'); await evalJS(`document.querySelector('#st-cert details').open = true; document.getElementById('st-cert').scrollIntoView(); 1`); await shot('18a-settings-certificate-auto.png', { view: true });
	for (const [q, n] of [['fallback', '18b-certificate-fallback.png'], ['soon', '18c-certificate-soon.png'], ['off', '18d-certificate-off.png']]) {
		await nav('?mock=1&user=1&tls=' + q + '#settings/st-cert'); await evalJS(`document.getElementById('st-cert').scrollIntoView(); 1`); await shot(n, { view: true });
	}
	// 19 settings → API tokens with a freshly created secret (mock secret, shown once)
	await nav('?mock=1&user=1#settings/st-tokens'); await click('#ac-tk'); await input('#ac-tk-name', 'home-assistant-2');
	await evalJS(`document.getElementById('ac-tk-f').requestSubmit(); 1`); await sleep(700); await hideToasts();
	await evalJS(`document.getElementById('st-tokens').scrollIntoView(); 1`); await shot('19-settings-tokens.png', { view: true });
	// 20 about with the Compatibility card (deep link #about/compat)
	await nav('?mock=1&user=1#about/compat'); await shot('20-about-compatibility.png');
	// 21 connection banner: the mock's &down=1 fails state / history / sensors from 2 s after the boot; the client counts two failures in a row
	// (the 5 s poll: state, then sensors) and shows the banner over the content it already has
	await nav('?mock=1&user=1&down=1', { wait: 7000 });
	await evalJS(`(()=>{if(document.getElementById('banner').hidden) throw new Error('21: banner not shown with &down=1'); return 1;})()`); await shot('21-connection-lost.png', { view: true });
	// 22 light theme
	await setLS({ ...LS, theme: 'light' }); await nav('?mock=1&user=1'); await shot('22-overview-light.png');
	await setLS(LS);
	// 23–25 mobile: 375 px, bottom bar, More sheet
	await viewport(375, 812);
	await nav('?mock=1'); await shot('23a-mobile-overview-anonymous.png');
	await nav('?mock=1&user=1'); await shot('23b-mobile-overview-signed-in.png');
	// 24 mobile fans: the channel selector (segmented control) shows one channel at a time
	await nav('?mock=1&user=1#fans'); await evalJS(`(()=>{const b=document.querySelectorAll('#ch-sel button')[1]; if(b) b.click(); return 1;})()`); await sleep(300); await shot('24-mobile-fans.png', { view: true });
	// 25 mobile More sheet (the remaining pages)
	await nav('?mock=1&user=1'); await click('#bnav .more'); await sleep(400); await shot('25-mobile-more-sheet.png', { view: true });
	// 26 overview at 24 h (the averaged tier; the selection persists in localStorage, reset afterwards) — desktop again
	await viewport(1280, 800);
	await nav('?mock=1&user=1'); await click('#rg-seg button[data-range="24h"]'); await sleep(1200); await shot('26-overview-24h.png');
	await setLS(LS);
	// 27 certificate warning chip in the header (expires soon), crop
	await nav('?mock=1&user=1&tls=soon'); await shot('27-header-cert-warning.png', { clip: { x: 0, y: 0, width: 1280, height: 60 } });
	// 28 home-assistant tiles: 28-home-assistant-tiles.png is a static asset from a real Home Assistant (README), not generated here
	console.log('done');
} catch (e) { console.error('FAILED', e); process.exitCode = 1; }
finally { await send('Browser.close').catch(() => {}); ws.close(); }
