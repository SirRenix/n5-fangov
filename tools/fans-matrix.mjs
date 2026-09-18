// Scenario matrix of the Fans page control logic against the mock — every control path and the user mistakes around
// curves, presets and the override switch, run in headless Chrome over the DevTools protocol like docs/screenshots/shots.mjs.
// Usage: node tools/fans-matrix.mjs <mockBaseURL> <cdpPort>
//   e.g. serve internal/web/static on a port, start Chrome with --headless=new --remote-debugging-port=9237, then
//   node tools/fans-matrix.mjs http://127.0.0.1:8806/index.html 9237
// Prints PASS/FAIL per scenario and exits 1 on any FAIL. Runs against ?mock=1&user=1&lag=1 (the daemon's next-cycle lag on
// overrides); the poll interval is set to 5 s, so a lagged transition takes up to ~15 s and the whole matrix a few minutes.
const [BASE, PORT] = process.argv.slice(2);
if (!BASE || !PORT) { console.error('usage: node tools/fans-matrix.mjs <mockBaseURL> <cdpPort>'); process.exit(2); }
const sleep = ms => new Promise(r => setTimeout(r, ms));
const ver = await (await fetch(`http://127.0.0.1:${PORT}/json/version`)).json();
const ws = new WebSocket(ver.webSocketDebuggerUrl); await new Promise((res, rej) => { ws.onopen = res; ws.onerror = rej; });
let id = 0; const pending = new Map(); const listeners = [];
ws.onmessage = ev => { const m = JSON.parse(ev.data); if (m.id && pending.has(m.id)) { const { res, rej } = pending.get(m.id); pending.delete(m.id); m.error ? rej(new Error(m.error.message)) : res(m.result); } else if (m.method) for (const l of listeners) l(m); };
const send = (method, params = {}, sessionId) => new Promise((res, rej) => { const i = ++id; pending.set(i, { res, rej }); ws.send(JSON.stringify({ id: i, method, params, sessionId })); });
const waitEvent = (method, sessionId) => new Promise(res => { const l = m => { if (m.method === method && m.sessionId === sessionId) { listeners.splice(listeners.indexOf(l), 1); res(m.params); } }; listeners.push(l); });
const { targetId } = await send('Target.createTarget', { url: 'about:blank' });
const { sessionId: S } = await send('Target.attachToTarget', { targetId, flatten: true });
await send('Page.enable', {}, S); await send('Runtime.enable', {}, S); await send('Log.enable', {}, S);
const console_ = [];
listeners.push(m => { if (m.sessionId !== S) return;
	if (m.method === 'Runtime.exceptionThrown') console_.push('exception: ' + (m.params.exceptionDetails.exception?.description || m.params.exceptionDetails.text).split('\n')[0]);
	if (m.method === 'Runtime.consoleAPICalled' && /error|warning/.test(m.params.type)) console_.push(m.params.type + ': ' + m.params.args.map(a => a.value ?? a.description).join(' '));
	// beforeunload: the dirty-editor guard firing on a scripted navigation — wanted, not an error
	if (m.method === 'Log.entryAdded' && m.params.entry.level === 'error' && !/favicon|beforeunload/.test(m.params.entry.text)) console_.push('log: ' + m.params.entry.text); });
const js = async expr => { const r = await send('Runtime.evaluate', { expression: expr, awaitPromise: true, returnByValue: true }, S); if (r.exceptionDetails) throw new Error('JS: ' + (r.exceptionDetails.exception?.description || r.exceptionDetails.text).split('\n')[0]); return r.result.value; };
const viewport = (w, h) => send('Emulation.setDeviceMetricsOverride', { width: w, height: h, deviceScaleFactor: 1, mobile: w < 700 }, S);
const nav = async (q, wait = 1800) => { const cur = await js('location.href'), next = BASE + q;
	if (cur.split('#')[0] === next.split('#')[0]) { const b = waitEvent('Page.loadEventFired', S); await send('Page.navigate', { url: 'about:blank' }, S); await b; }
	const loaded = waitEvent('Page.loadEventFired', S); await send('Page.navigate', { url: next }, S); await loaded; await sleep(wait); };
const LS = { unit: 'C', interval: 5, theme: 'dark', range: '2h', nav: 'side' };
const setLS = () => js(`localStorage.setItem('n5-fangov', ${JSON.stringify(JSON.stringify(LS))}); 1`);
const FANS = '?mock=1&user=1&lag=1#fans';
// page helpers (selectors from app.js / index.html)
const q = (sel, prop) => js(`(()=>{const e=document.querySelector(${JSON.stringify(sel)}); if(!e) throw new Error('no '+${JSON.stringify(sel)}); return e.${prop};})()`);
const click = sel => js(`(()=>{const e=document.querySelector(${JSON.stringify(sel)}); if(!e) throw new Error('no '+${JSON.stringify(sel)}); e.click(); return 1;})()`);
const input = (sel, v) => js(`(()=>{const e=document.querySelector(${JSON.stringify(sel)}); if(!e) throw new Error('no '+${JSON.stringify(sel)}); e.value=${JSON.stringify(String(v))}; e.dispatchEvent(new Event('input',{bubbles:true})); e.dispatchEvent(new Event('change',{bubbles:true})); return 1;})()`);
const card = ch => `[...document.querySelectorAll('#fan-cards .fan')].find(c=>c.querySelector('h2.name').textContent===${JSON.stringify(ch)})`;
const badge = ch => js(`${card(ch)}.querySelector('.fh .badges .badge').textContent`); // the preset badge (names joined by · , or custom)
const badgeTip = ch => js(`${card(ch)}.querySelector('.fh .badges .badge').title`);
const mode = ch => js(`${card(ch)}.querySelector('.fh .mode').textContent`);
const sw = ch => js(`${card(ch)}.querySelector('[role=switch]').getAttribute('aria-checked')`);
const clickSw = ch => js(`${card(ch)}.querySelector('[role=switch]').click(); 1`);
const duty = ch => js(`+${card(ch)}.querySelector('.now .d').firstChild.textContent`);
const held = ch => js(`${card(ch)}.querySelector('.tgt span').textContent`);
const point = (ch, i, k) => js(`${card(ch)}.querySelector('tbody input[aria-label="point ${i} ${k}"]').value`);
const setPoint = (ch, i, k, v) => js(`(()=>{const e=${card(ch)}.querySelector('tbody input[aria-label="point ${i} ${k}"]'); e.value=${JSON.stringify(String(v))}; e.dispatchEvent(new Event('input',{bubbles:true})); return 1;})()`);
const setCrit = (ch, v) => js(`(()=>{const e=${card(ch)}.querySelector('.fields input[type=number][min="30"]'); e.value=${JSON.stringify(String(v))}; e.dispatchEvent(new Event('input',{bubbles:true})); return 1;})()`);
const activeSet = () => js(`document.querySelector('#ps-active b')?.textContent ?? null`);
const chips = () => js(`[...document.querySelectorAll('#preset-row .pchip .pn')].map(x=>x.textContent)`);
const activeChip = () => js(`document.querySelector('#preset-row .pchip.active .pn')?.textContent ?? null`);
const applyPreset = async name => { await js(`[...document.querySelectorAll('#preset-row .pchip')].find(c=>c.querySelector('.pn').textContent===${JSON.stringify(name)}).querySelector('.btn.sm').click(); 1`); await sleep(300); await click('#cf-ok'); await sleep(900); };
const isDirty = async () => (await q('#cv-dirty', 'hidden')) === false;
const toasts = () => js(`[...document.querySelectorAll('#toasts .toast, #toasts-a .toast')].map(t=>t.textContent.trim())`);
const clearToasts = () => js(`document.querySelectorAll('.toast').forEach(t=>t.remove()); 1`);
const mockCall = (path, opt) => js(`window.n5mock(${JSON.stringify(path)}, ${JSON.stringify(opt || {})}).then(r=>r.body)`);
const cfgChannel = async ch => (await mockCall('/api/config')).config.channel.find(c => c.name === ch);
// until: poll a condition for up to ms (lagged transitions: the mock reports the previous override state for two more polls)
const until = async (fn, ms = 20000, step = 500) => { const t0 = Date.now(); let last; while (Date.now() - t0 < ms) { last = await fn(); if (last === true) return true; await sleep(step); } return last; };
const eq = (what, got, want) => got === want ? true : `${what}: got ${JSON.stringify(got)}, want ${JSON.stringify(want)}`;
const all = (...rs) => rs.find(r => r !== true) || true;

const results = []; let failed = 0;
const scenario = async (key, title, fn) => { const before = console_.length; let r;
	try { r = await fn(); } catch (e) { r = e.message; }
	const errs = console_.slice(before); if (r === true && errs.length) r = 'console: ' + errs.join(' | ');
	if (r !== true) failed++; const line = `${r === true ? 'PASS' : 'FAIL'} (${key}) ${title}${r === true ? '' : ' — ' + r}`; results.push(line); console.log(line); };

await viewport(1280, 900);
await nav('?mock=1', 600); await setLS();

// (a) apply built-in → every badge and the active set follow
await scenario('a', 'apply built-in → all badges / active set follow', async () => { await nav(FANS);
	const r0 = all(eq('cpu badge at start', await badge('cpu'), 'alternative · n5pro-balanced'), eq('hdd badge at start', await badge('hdd'), 'n5pro-balanced'), eq('active set at start', await activeSet(), 'n5pro-balanced'));
	if (r0 !== true) return r0;
	await applyPreset('n5pro-quiet');
	return all(eq('active chip', await activeChip(), 'n5pro-quiet'), eq('active set', await activeSet(), 'n5pro-quiet'), eq('cpu badge', await badge('cpu'), 'n5pro-quiet'), eq('ssd badge', await badge('ssd'), 'n5pro-quiet'), eq('hdd badge', await badge('hdd'), 'n5pro-quiet'), eq('cpu point 1 temp', await point('cpu', 1, 'temp'), '49')); });

// (b) a user preset that shares channels with a built-in → the badge lists both, the active set names the user preset
await scenario('b', 'apply user preset sharing channels with a built-in → badge lists both, active set = user preset', async () => {
	await applyPreset('alternative');
	return all(eq('active set', await activeSet(), 'alternative'), eq('active chip', await activeChip(), 'alternative'), eq('cpu badge', await badge('cpu'), 'alternative · n5pro-balanced'), eq('ssd badge', await badge('ssd'), 'alternative · n5pro-balanced'), eq('hdd badge', await badge('hdd'), 'alternative'),
		eq('cpu tooltip', await badgeTip('cpu'), 'the presets these values match:\nalternative\nn5pro-balanced'), eq('balanced chip not active', await js(`document.querySelectorAll('#preset-row .pchip.active').length`), 1)); });

// (c) edit → dirty → Revert → clean, badges unchanged
await scenario('c', 'edit a curve → dirty → Revert → clean, badges unchanged', async () => {
	await setPoint('cpu', 1, 'duty', 120);
	const r1 = all(eq('dirty', await isDirty(), true), eq('nav dot', await q('#nav-dirty', 'hidden'), false), eq('Save as preset shown', await q('#cv-saveas', 'hidden'), false), eq('cpu badge while dirty', await badge('cpu'), 'alternative · n5pro-balanced'));
	if (r1 !== true) return r1;
	await click('#cv-revert'); await sleep(400);
	return all(eq('clean', await isDirty(), false), eq('Save as preset hidden', await q('#cv-saveas', 'hidden'), true), eq('cpu point 1 duty back', await point('cpu', 1, 'duty'), '85'), eq('cpu badge', await badge('cpu'), 'alternative · n5pro-balanced'), eq('active set', await activeSet(), 'alternative')); });

// (d) edit → Apply → badges recomputed (custom where nothing matches), active set custom
await scenario('d', 'edit → Apply → badges recomputed (custom), active set custom', async () => {
	await setPoint('cpu', 1, 'duty', 120); await clearToasts(); await click('#cv-apply'); await sleep(1200);
	const t = await toasts();
	return all(eq('toast', t.some(x => /Curves applied/.test(x)), true), eq('clean', await isDirty(), false), eq('cpu badge', await badge('cpu'), 'custom'), eq('ssd badge', await badge('ssd'), 'alternative · n5pro-balanced'), eq('hdd badge', await badge('hdd'), 'alternative'), eq('active set', await activeSet(), 'custom'), eq('no active chip', await activeChip(), null), eq('mock cpu duty', (await cfgChannel('cpu')).curve[0][1], 120)); });

// (e) edit → Save as preset… → preset exists, editor still dirty → Apply → active set = the new preset
await scenario('e', 'edit → Save as preset… → preset saved, editor dirty → Apply → active set = new preset', async () => {
	await setPoint('ssd', 1, 'duty', 90); await clearToasts(); await click('#cv-saveas'); await sleep(400);
	const r1 = all(eq('dialog open', await q('#preset-ed', 'open'), true), eq('start from = editor', await q('#pe-body .src select', 'value'), 'editor'), eq('start from locked', await q('#pe-body .src select', 'disabled'), true), eq('title', await q('#pe-title', 'textContent'), 'Save as preset'),
		eq('editor value in the dialog', await js(`[...document.querySelectorAll('#pe-body .pe-ch')].find(b=>b.querySelector('.name').textContent==='ssd').querySelector('tbody input[aria-label="ssd point 1 duty"]').value`), '90'));
	if (r1 !== true) return r1;
	await input('#pe-body .frow input[type=text]', 'mine'); await js(`[...document.querySelectorAll('#pe-body .act .btn')].find(b=>/Save/.test(b.textContent)).click(); 1`); await sleep(1200);
	const t = await toasts();
	const r2 = all(eq('toast', t.some(x => /Preset mine saved — the editor still has unsaved changes; Apply to daemon writes them/.test(x)), true), eq('dialog closed', await q('#preset-ed', 'open'), false), eq('chip mine', (await chips()).includes('mine'), true),
		eq('still dirty', await isDirty(), true), eq('active set unchanged', await activeSet(), 'custom'), eq('mock ssd unchanged', (await cfgChannel('ssd')).curve[0][1], 74));
	if (r2 !== true) return r2;
	await clearToasts(); await click('#cv-apply'); await sleep(1200);
	return all(eq('clean', await isDirty(), false), eq('active set', await activeSet(), 'mine'), eq('cpu badge', await badge('cpu'), 'mine'), eq('ssd badge', await badge('ssd'), 'mine'), eq('hdd badge', await badge('hdd'), 'alternative · mine'), eq('active chip', await activeChip(), 'mine')); });

// (f) New preset… from the daemon → the dialog holds the running config
await scenario('f', 'New preset… from daemon → values = running config', async () => {
	await click('#ps-new'); await sleep(400); const cpu = await cfgChannel('cpu'), ssd = await cfgChannel('ssd');
	const r = all(eq('dialog open', await q('#preset-ed', 'open'), true), eq('start from', await q('#pe-body .src select', 'value'), 'daemon'), eq('selectable', await q('#pe-body .src select', 'disabled'), false), eq('title', await q('#pe-title', 'textContent'), 'New preset'),
		eq('cpu point 1 duty', await js(`document.querySelector('#pe-body input[aria-label="cpu point 1 duty"]').value`), String(cpu.curve[0][1])), eq('ssd point 1 duty', await js(`document.querySelector('#pe-body input[aria-label="ssd point 1 duty"]').value`), String(ssd.curve[0][1])),
		eq('cpu critical', await js(`[...document.querySelectorAll('#pe-body .pe-ch')].find(b=>b.querySelector('.name').textContent==='cpu').querySelector('input[type=number][min="30"]').value`), String(cpu.critical)));
	await js(`[...document.querySelectorAll('#pe-body .act .btn')].find(b=>/Cancel/.test(b.textContent)).click(); 1`); await sleep(300);
	return all(r, eq('closed', await q('#preset-ed', 'open'), false)); });

// (g) override on → Set → off: badges and active set never move, the mode follows with the lag
await scenario('g', 'override on → Set → off → badges unchanged, mode follows with the lag', async () => {
	const b0 = await badge('cpu'), a0 = await activeSet(); await clearToasts(); await clickSw('cpu'); await sleep(600);
	const r1 = all(eq('switch on at once', await sw('cpu'), 'true'), eq('manual block enabled', await js(`${card('cpu')}.querySelector('.live').dataset.off`), 'false'), eq('toast', (await toasts()).some(x => /cpu: manual — holding/.test(x)), true));
	if (r1 !== true) return r1;
	const r2 = await until(async () => eq('mode after the lag', await mode('cpu'), 'manual')); if (r2 !== true) return r2;
	await input(`#fan-cards .fan:nth-child(1) .rg input[type=number]`, 200); await js(`${card('cpu')}.querySelector('.live .actions .btn').click(); 1`); await sleep(600);
	const r3 = await until(async () => all(eq('duty after Set', await duty('cpu'), 200), eq('held', await held('cpu'), 'held 200 · mode'))); if (r3 !== true) return r3;
	const r4 = all(eq('badge unchanged', await badge('cpu'), b0), eq('active set unchanged', await activeSet(), a0), eq('clean', await isDirty(), false)); if (r4 !== true) return r4;
	await clickSw('cpu'); await sleep(600);
	const r5 = all(eq('switch off at once', await sw('cpu'), 'false'), eq('manual block disabled', await js(`${card('cpu')}.querySelector('.live').dataset.off`), 'true')); if (r5 !== true) return r5;
	const r6 = await until(async () => eq('mode auto after the lag', await mode('cpu'), 'auto')); if (r6 !== true) return r6;
	return all(eq('badge unchanged', await badge('cpu'), b0), eq('active set unchanged', await activeSet(), a0)); });

// (h) override on, then Apply a preset → the override stays (mode manual), the curves change underneath, the switch still turns it off
await scenario('h', 'override on, then apply a preset → override stays, curves change, switch turns it off', async () => {
	await clickSw('cpu'); await sleep(600); const r1 = await until(async () => eq('mode manual', await mode('cpu'), 'manual')); if (r1 !== true) return r1;
	await applyPreset('n5pro-cool'); await sleep(600);
	const r2 = all(eq('switch still on', await sw('cpu'), 'true'), eq('mode manual after the apply', await mode('cpu'), 'manual'), eq('cpu curve followed', await point('cpu', 1, 'temp'), '39'), eq('cpu badge', await badge('cpu'), 'n5pro-cool'), eq('active set', await activeSet(), 'n5pro-cool'), eq('manual block enabled', await js(`${card('cpu')}.querySelector('.live').dataset.off`), 'false'));
	if (r2 !== true) return r2;
	await clickSw('cpu'); await sleep(600);
	return all(eq('switch off', await sw('cpu'), 'false'), await until(async () => eq('mode auto after the lag', await mode('cpu'), 'auto'))); });

// (i) override on, then edit + Apply → the same
await scenario('i', 'override on, then edit + Apply → override stays, switch turns it off', async () => {
	await clickSw('cpu'); await sleep(600); const r1 = await until(async () => eq('mode manual', await mode('cpu'), 'manual')); if (r1 !== true) return r1;
	await setPoint('cpu', 1, 'duty', 100); await clearToasts(); await click('#cv-apply'); await sleep(1200);
	const r2 = all(eq('applied', (await toasts()).some(x => /Curves applied/.test(x)), true), eq('switch still on', await sw('cpu'), 'true'), eq('mode manual', await mode('cpu'), 'manual'), eq('cpu badge custom', await badge('cpu'), 'custom'), eq('manual block enabled', await js(`${card('cpu')}.querySelector('.live').dataset.off`), 'false'));
	if (r2 !== true) return r2;
	await clickSw('cpu'); await sleep(600);
	return all(eq('switch off', await sw('cpu'), 'false'), await until(async () => eq('mode auto after the lag', await mode('cpu'), 'auto'))); });

// (j) HDD switch on while the channel runs below 60 → the override holds 60
await scenario('j', 'HDD switch on at duty < 60 → duty 60', async () => {
	await setPoint('hdd', 1, 'duty', 0); await setPoint('hdd', 2, 'duty', 40); await clearToasts(); await click('#cv-apply'); await sleep(1200);
	const r1 = await until(async () => { const d = await duty('hdd'); return d < 60 ? true : `hdd runs ${d}`; }); if (r1 !== true) return r1;
	await clearToasts(); await clickSw('hdd'); await sleep(600);
	const r2 = all(eq('toast holds 60', (await toasts()).some(x => /hdd: manual — holding 60 \(24 %\)/.test(x)), true), eq('slider at 60', await js(`${card('hdd')}.querySelector('input[type=range]').value`), '60'), eq('Set enabled at 60', await js(`${card('hdd')}.querySelector('.live .actions .btn').disabled`), false));
	if (r2 !== true) return r2;
	const r3 = await until(async () => all(eq('duty 60 after the lag', await duty('hdd'), 60), eq('mode manual', await mode('hdd'), 'manual'))); if (r3 !== true) return r3;
	await input(`#fan-cards .fan:nth-child(3) .rg input[type=number]`, 40);
	const r4 = all(eq('Set disabled below 60', await js(`${card('hdd')}.querySelector('.live .actions .btn').disabled`), true), eq('warn shown', await js(`${card('hdd')}.querySelector('.live .warn').classList.contains('on')`), true));
	if (r4 !== true) return r4;
	await clickSw('hdd'); await sleep(600); return await until(async () => eq('mode auto after the lag', await mode('hdd'), 'auto')); });

// (k) validation: an invalid curve sends nothing, the editor stays dirty
await scenario('k', 'validation: invalid curve → nothing sent, dirty stays', async () => {
	await applyPreset('n5pro-balanced'); const before = await cfgChannel('cpu');
	await setCrit('cpu', 20); await clearToasts(); await click('#cv-apply'); await sleep(600);
	return all(eq('notice', /Not applied — fix these first:[\s\S]*cpu: critical 20 must be a whole number/.test(await q('#cv-notice', 'textContent')), true), eq('notice role', await q('#cv-notice', 'className'), 'notice err'), eq('still dirty', await isDirty(), true),
		eq('nothing sent', (await cfgChannel('cpu')).critical, before.critical), eq('no toast', (await toasts()).length, 0), eq('active set still balanced', await activeSet(), 'n5pro-balanced')); });

// (l) sign-out with dirty edits asks; Cancel keeps them
await scenario('l', 'sign-out with dirty edits → ask; Cancel keeps the edits', async () => {
	const r1 = eq('dirty precondition', await isDirty(), true); if (r1 !== true) return r1;
	await click('#h-signout'); await sleep(300);
	const r2 = all(eq('confirm open', await q('#confirm', 'open'), true), eq('text', /Unsaved curve changes are discarded/.test(await q('#cf-text', 'textContent')), true)); if (r2 !== true) return r2;
	await click('#cf-cancel'); await sleep(300);
	return all(eq('still signed in', await q('#h-signout', 'hidden'), false), eq('still dirty', await isDirty(), true), eq('edit kept', await js(`${card('cpu')}.querySelector('.fields input[type=number][min="30"]').value`), '20')); });

// (m) session loss stashes the edits; the next sign-in restores them
await scenario('m', 'session loss → stash → sign-in → edits back', async () => {
	await nav('?mock=1&user=1&lag=1&expire=1#fans'); await setPoint('cpu', 1, 'duty', 111); await clearToasts();
	const r1 = await until(async () => eq('expired', (await toasts()).some(x => /Session expired — sign in again — curve edits kept/.test(x)), true), 30000, 1000); if (r1 !== true) return r1;
	const r2 = all(eq('anonymous', await q('#h-signin', 'hidden'), false), eq('fell back to the overview', await js(`document.querySelector('.pg:not([hidden])').id`), 'p-overview')); if (r2 !== true) return r2;
	await clearToasts(); await click('#h-signin'); await input('#l-user', 'admin'); await input('#l-pass', 'admin'); await js(`document.getElementById('login-f').requestSubmit(); 1`); await sleep(1500);
	const r3 = all(eq('signed in', await q('#h-signout', 'hidden'), false), eq('restore toast', (await toasts()).some(x => /Unsaved curve edits restored \(Fans page\)/.test(x)), true)); if (r3 !== true) return r3;
	await js(`location.hash = '#fans'; 1`); await sleep(800);
	return all(eq('dirty after the sign-in', await isDirty(), true), eq('edit back', await point('cpu', 1, 'duty'), '111'), eq('notice', /restored — apply or revert/.test(await q('#cv-notice', 'textContent')), true)); });

// (n) 375 px: the channel selector carries a dirty dot per edited channel
await scenario('n', '375 px selector: dirty dot per channel', async () => {
	await viewport(375, 812); await nav(FANS);
	const r1 = all(eq('selector shown', await q('#ch-sel', 'hidden'), false), eq('one card', await js(`[...document.querySelectorAll('#fan-cards .fan')].filter(f=>!f.hidden).length`), 1)); if (r1 !== true) return r1;
	await click('#ch-sel button[data-ch=hdd]'); await sleep(200); await setPoint('hdd', 1, 'duty', 110);
	const dots = await js(`[...document.querySelectorAll('#ch-sel button .dirty')].map(d=>d.hidden)`);
	await click('#ch-sel button[data-ch=cpu]'); await sleep(200); await setPoint('cpu', 1, 'duty', 90);
	const dots2 = await js(`[...document.querySelectorAll('#ch-sel button .dirty')].map(d=>d.hidden)`);
	await click('#cv-revert'); await sleep(300); const dots3 = await js(`[...document.querySelectorAll('#ch-sel button .dirty')].map(d=>d.hidden)`);
	await viewport(1280, 900); await sleep(300);
	return all(eq('hdd dot', JSON.stringify(dots), '[true,true,false]'), eq('cpu + hdd dots', JSON.stringify(dots2), '[false,true,false]'), eq('after Revert', JSON.stringify(dots3), '[true,true,true]'), eq('back to three cards', await js(`[...document.querySelectorAll('#fan-cards .fan')].filter(f=>!f.hidden).length`), 3)); });

// (o) deleting the active set → the active set becomes custom (or another preset)
await scenario('o', 'delete the active preset → active set custom / other', async () => {
	await nav(FANS); await applyPreset('alternative'); const r1 = eq('active set', await activeSet(), 'alternative'); if (r1 !== true) return r1;
	await js(`document.querySelector('#preset-row [aria-label="alternative: delete"]').click(); 1`); await sleep(300); await click('#cf-ok'); await sleep(1000);
	return all(eq('chip gone', (await chips()).includes('alternative'), false), eq('active set', await activeSet(), 'custom'), eq('cpu badge', await badge('cpu'), 'n5pro-balanced'), eq('hdd badge', await badge('hdd'), 'custom'), eq('no active chip', await activeChip(), null)); });

// (p) renaming the active preset → the badges follow the new name
await scenario('p', 'rename the active preset → badges follow', async () => {
	await applyPreset('summer'); const r1 = all(eq('active set', await activeSet(), 'summer'), eq('cpu badge', await badge('cpu'), 'summer')); if (r1 !== true) return r1;
	await js(`document.querySelector('#preset-row [aria-label="summer: edit"]').click(); 1`); await sleep(400); await input('#pe-body .frow input[type=text]', 'summer2');
	await js(`[...document.querySelectorAll('#pe-body .act .btn')].find(b=>/Save/.test(b.textContent)).click(); 1`); await sleep(1200);
	return all(eq('chips', (await chips()).includes('summer2') && !(await chips()).includes('summer'), true), eq('active set', await activeSet(), 'summer2'), eq('cpu badge', await badge('cpu'), 'summer2'), eq('hdd badge', await badge('hdd'), 'summer2'), eq('active chip', await activeChip(), 'summer2')); });

// (q) Apply with &restart=1 → the restart notice stays across polls
await scenario('q', 'apply with &restart=1 → notice stays', async () => {
	await nav('?mock=1&user=1&lag=1&restart=1#fans'); await setCrit('cpu', 89); await clearToasts(); await click('#cv-apply'); await sleep(1200);
	const r1 = all(eq('notice', /Written — restart required/.test(await q('#cv-notice', 'textContent')), true), eq('shown', await q('#cv-notice', 'hidden'), false), eq('clean', await isDirty(), false)); if (r1 !== true) return r1;
	await sleep(12000); // two polls
	return all(eq('notice still shown', await q('#cv-notice', 'hidden'), false), eq('text kept', /restart required/.test(await q('#cv-notice', 'textContent')), true)); });

// (r) two browsers: a preset applied elsewhere → the badges follow after the page's next refresh (config + presets re-read every 30 s)
await scenario('r', 'preset applied elsewhere → badges follow after the next refresh', async () => {
	await nav(FANS); const r0 = eq('active set at start', await activeSet(), 'n5pro-balanced'); if (r0 !== true) return r0;
	await mockCall('/api/presets/n5pro-quiet/apply', { method: 'POST' }); // the other browser
	const r1 = await until(async () => all(eq('active set', await activeSet(), 'n5pro-quiet'), eq('cpu badge', await badge('cpu'), 'n5pro-quiet'), eq('active chip', await activeChip(), 'n5pro-quiet'), eq('editor followed', await point('cpu', 1, 'temp'), '49')), 40000, 1000);
	if (r1 !== true) return r1;
	// a dirty editor keeps its edits while the badges follow the daemon
	await setPoint('ssd', 1, 'duty', 77); await mockCall('/api/presets/n5pro-cool/apply', { method: 'POST' });
	return await until(async () => all(eq('active set', await activeSet(), 'n5pro-cool'), eq('still dirty', await isDirty(), true), eq('edit kept', await point('ssd', 1, 'duty'), '77'), eq('cpu badge', await badge('cpu'), 'n5pro-cool')), 40000, 1000); });

console.log(`\n${results.length - failed}/${results.length} scenarios passed${failed ? `, ${failed} FAILED` : ''}`);
await send('Target.closeTarget', { targetId }); ws.close();
process.exit(failed ? 1 : 0);
