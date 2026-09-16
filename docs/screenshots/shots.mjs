// Screenshot driver for docs/screenshots: Chrome headless over the DevTools protocol.
// Usage: node docs/screenshots/shots.mjs <outDir> <mockBaseURL> <cdpPort>   (see README.md here)
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
const hideToasts = () => evalJS(`(()=>{const t=document.getElementById('toasts'); if(t) t.replaceChildren(); return 1;})()`);
const viewport = (w, h, mobile = false) => send('Emulation.setDeviceMetricsOverride', { width: w, height: h, deviceScaleFactor: 1, mobile }, S);
const nav = async (q, opts = {}) => {
	const loaded = waitEvent('Page.loadEventFired', S);
	await send('Page.navigate', { url: BASE + q }, S);
	await loaded; await sleep(opts.wait ?? 900); await hideToasts();
};
const shot = async (name, o = {}) => {
	await sleep(o.settle ?? 250);
	const p = { format: 'png' };
	if (o.clip) p.clip = { ...o.clip, scale: 1 };
	if (o.full) { p.captureBeyondViewport = true; const h = await evalJS('document.documentElement.scrollHeight'); p.clip = { x: 0, y: 0, width: o.w || 1280, height: h, scale: 1 }; }
	const r = await send('Page.captureScreenshot', p, S);
	writeFileSync(join(outDir, name), Buffer.from(r.data, 'base64'));
	console.log('wrote', name);
};
const click = sel => evalJS(`(()=>{const e=document.querySelector(${JSON.stringify(sel)}); if(!e) throw new Error('no '+${JSON.stringify(sel)}); e.click(); return 1;})()`);
const clickText = (sel, text) => evalJS(`(()=>{const e=[...document.querySelectorAll(${JSON.stringify(sel)})].find(x=>x.textContent.trim()===${JSON.stringify(text)}); if(!e) throw new Error('no '+${JSON.stringify(text)}); e.click(); return 1;})()`);
const setLS = obj => evalJS(`localStorage.setItem('n5-fangov', ${JSON.stringify(JSON.stringify(obj))}); 1`);

try {
	await viewport(1280, 800);
	// theme dark, 5 s interval, °C — deterministic
	await nav('?mock=1'); await setLS({ unit: 'C', interval: 5, theme: 'dark' });

	// 01 overview anonymous
	await nav('?mock=1'); await shot('01-overview-anonymous.png');
	// 02 login dialog
	await click('#h-signin'); await evalJS(`document.getElementById('l-user').value='admin'; 1`); await shot('02-login-dialog.png');
	// 03/04 overview signed in
	await nav('?mock=1&user=1', { wait: 1500 }); await shot('03-overview-signed-in-top.png');
	await evalJS('window.scrollTo(0, document.documentElement.scrollHeight); 1'); await shot('04-overview-signed-in-bottom.png', { settle: 500 });
	await evalJS('window.scrollTo(0,0); 1');
	// 05 header crop
	await shot('05-header.png', { clip: { x: 0, y: 0, width: 1280, height: 92 } });
	// 06 settings popover
	await click('#h-settings'); await shot('06-settings-popover.png');
	// 07 curves
	await nav('?mock=1&user=1&tab=curves', { wait: 1500 }); await shot('07-curves.png');
	// 08 curves validation error: second cpu point below the first -> client-side notice, no PUT
	await evalJS(`(()=>{const i=document.querySelector('#editors input[type=text][placeholder=auto]'); i.value='300'; i.dispatchEvent(new Event('input',{bubbles:true})); const c=document.querySelector('#editors input[type=number][min="30"]'); c.value=''; c.dispatchEvent(new Event('input',{bubbles:true})); return 1;})()`);
	await click('#cv-apply'); await sleep(300); await hideToasts(); await shot('08-curves-error.png');
	// 09 curves restart required (mock: fewer [[channel]] tables -> 202); the notice is visible only
	// between the PUT answer and the editor reload in this build, so capture right after the answer
	// (editor state is closure-scoped; the notice text below is the client's verbatim string)
	await nav('?mock=1&user=1&tab=curves', { wait: 1500 });
	await evalJS(`(()=>{const n=document.getElementById('cv-notice'); n.hidden=false; n.className='notice'; n.textContent='Saved — restart required (channel set or profile changed): systemctl restart n5-fangov'; return 1;})()`);
	await shot('09-curves-restart-required.png');
	// 10 manual with hdd slider below 60
	await nav('?mock=1&user=1&tab=manual', { wait: 1500 });
	await evalJS(`(()=>{const r=document.getElementById('rg-hdd'); r.value=40; r.dispatchEvent(new Event('input',{bubbles:true})); return 1;})()`);
	await shot('10-manual.png');
	// 11 presets with details
	await nav('?mock=1&user=1&tab=presets', { wait: 1500 }); await clickText('#presets button', 'Details'); await sleep(500); await shot('11-presets.png');
	// 12 alerts, 13 test toast
	await nav('?mock=1&user=1&tab=alerts', { wait: 1500 }); await shot('12-alerts.png');
	await click('#al-test'); await sleep(600); await shot('13-alerts-test-toast.png', { clip: { x: 640, y: 560, width: 640, height: 240 } });
	// 14 certificate automatic with "how to trust" open
	await nav('?mock=1&user=1', { wait: 1500 }); await click('#h-sec'); await sleep(500);
	await evalJS(`document.querySelector('#cert details').open = true; 1`); await shot('14-certificate-auto.png');
	// 15 upload form after force_required
	await click('#ct-upload'); await sleep(200);
	await evalJS(`(()=>{document.getElementById('ct-cpem').value='-----BEGIN CERTIFICATE-----\\nMIIB...example...\\n-----END CERTIFICATE-----'; document.getElementById('ct-kpem').value='-----BEGIN PRIVATE KEY-----\\nMIIE...example...\\n-----END PRIVATE KEY-----'; return 1;})()`);
	await evalJS(`document.getElementById('ct-upload-f').requestSubmit(); 1`); await sleep(700); await hideToasts(); await shot('15-certificate-upload.png');
	// 16 fallback / soon / off
	for (const [q, n] of [['fallback', '16a-certificate-fallback.png'], ['soon', '16b-certificate-soon.png'], ['off', '16c-certificate-off.png']]) {
		await nav('?mock=1&user=1&tls=' + q, { wait: 1500 });
		if (q === 'off') { await click('#h-settings'); await click('#s-cert'); } else await click('#h-sec');
		await sleep(500); await shot(n);
	}
	// 17 account dialog with password form
	await nav('?mock=1&user=1', { wait: 1500 }); await click('#h-settings'); await click('#s-acc'); await sleep(400); await click('#ac-pw'); await sleep(200); await shot('17-account.png');
	// 18 system (two screens), 19 error notice
	await nav('?mock=1&user=1&tab=system', { wait: 1500 }); await shot('18a-system.png');
	await evalJS('window.scrollTo(0, 800); 1'); await shot('18b-system-scrolled.png', { settle: 500 });
	await nav('?mock=1&user=1&tab=system&syserr=1', { wait: 1500 }); await shot('19-system-error.png');
	// 20 log, 21 compat, 22 about
	await nav('?mock=1&user=1&tab=log', { wait: 1500 }); await shot('20-log.png');
	await nav('?mock=1&user=1&tab=compat', { wait: 1500 }); await shot('21-compatibility.png');
	await nav('?mock=1&user=1&tab=about', { wait: 1500 }); await shot('22-about.png');
	// 23 connection banner (same DOM the client shows after two failed polls)
	await nav('?mock=1&user=1', { wait: 1500 });
	await evalJS(`document.getElementById('banner').hidden=false; document.getElementById('h-live').classList.add('err'); 1`); await shot('23-connection-lost.png');
	// 24 light theme
	await setLS({ unit: 'C', interval: 5, theme: 'light' }); await nav('?mock=1&user=1', { wait: 1500 }); await shot('24-overview-light.png');
	await setLS({ unit: 'C', interval: 5, theme: 'dark' });
	// 25/26 mobile
	await viewport(375, 812, true);
	await nav('?mock=1', { wait: 1500 }); await shot('25a-mobile-overview-anonymous.png', { full: true, w: 375 });
	await nav('?mock=1&user=1', { wait: 1500 }); await shot('25b-mobile-overview-signed-in.png');
	await nav('?mock=1&user=1&tab=curves', { wait: 1500 }); await shot('26-mobile-curves.png');
	console.log('done');
} catch (e) { console.error('FAILED', e); process.exitCode = 1; }
finally { await send('Browser.close').catch(() => {}); ws.close(); }
