// n5-fangov dashboard — vanilla JS, CSP-safe
'use strict';
(() => {
const $ = (s, r) => (r || document).querySelector(s);
const $$ = s => [...document.querySelectorAll(s)];
const h = (tag, attrs, ...kids) => {
	const e = document.createElement(tag); if (tag === 'button') e.type = 'button';
	if (attrs) for (const k in attrs) {
		const v = attrs[k];
		if (k === 'class') e.className = v;
		else if (k.startsWith('on')) e.addEventListener(k.slice(2), v);
		else if (v != null && v !== false) e.setAttribute(k, v === true ? '' : v);
	}
	for (const c of kids.flat()) if (c != null) e.append(c.nodeType ? c : String(c));
	return e;
};
const clear = e => { while (e.firstChild) e.removeChild(e.firstChild); return e; };
const backdrop = d => d.addEventListener('click', ev => { if (ev.target === d) d.close(); });
const on = (sel, ev, fn) => $(sel).addEventListener(ev, fn);
const clamp = (v, a, b) => Math.min(b, Math.max(a, v));
const SERIES = Array.from({ length: 8 }, (_, i) => '--s' + (i + 1));
const cssVar = n => getComputedStyle(document.documentElement).getPropertyValue(n).trim();
const Q = new URLSearchParams(location.search), MOCK = Q.get('mock') === '1';
const REF = { // N5 Pro duty→RPM (measured)
	cpu: [[85, 2000], [140, 3120], [179, 3830], [217, 4445], [255, 5073]],
	ssd: [[74, 2130], [140, 3280], [179, 3790], [217, 4230], [255, 4687]],
	hdd: [[87, 1237], [105, 1650], [140, 2250], [179, 2725], [217, 3160], [255, 3540]] };

// settings
const S = { unit: 'C', interval: 5, theme: 'dark' };
try { Object.assign(S, JSON.parse(localStorage.getItem('n5-fangov') || '{}')); } catch (e) {}
const saveS = () => { try { localStorage.setItem('n5-fangov', JSON.stringify(S)); } catch (e) {} };
const tC = v => S.unit === 'F' ? v * 9 / 5 + 32 : v;
const unit = () => S.unit === 'F' ? '°F' : '°C';
const fmtT = (v, d = 1) => !(v > -900) ? '—' : tC(v).toFixed(d);
const pct = d => Math.round(d / 255 * 100);
const rel = ts => { const s = Math.max(0, Date.now() / 1000 - ts | 0);
	return s < 60 ? s + ' s ago' : s < 3600 ? (s / 60 | 0) + ' min ago' : s < 86400 ? `${s / 3600 | 0} h ${s % 3600 / 60 | 0} min ago` : `${s / 86400 | 0} d ${s % 86400 / 3600 | 0} h ago`; };
const fmtUp = s => { const d = s / 86400 | 0, hh = s % 86400 / 3600 | 0, m = s % 3600 / 60 | 0; return d ? `${d}d ${hh}h` : hh ? `${hh}h ${m}m` : `${m}m`; };
const hm = ts => new Date(ts * 1000).toTimeString().slice(0, 5);
const toTs = v => typeof v === 'number' ? v : Date.parse(v) / 1000; // unix seconds or RFC 3339
const abs = ts => new Date(ts * 1000).toLocaleString();
const tm = (v, future) => { const ts = toTs(v); return !(ts > 0) ? '—' : h('time', { datetime: new Date(ts * 1000).toISOString(), title: future ? null : abs(ts) }, future ? abs(ts) : rel(ts)); };
const notice = id => (msg, kind) => { const n = $(id); n.hidden = !msg; if (kind !== undefined) n.className = 'notice ' + (kind || ''); n.textContent = msg || ''; };
const kv = (el, rows) => { clear(el); for (const [k, v] of rows) el.append(h('dt', null, k), v && v.nodeType ? v : h('dd', null, v)); return el; };
const interp = (curve, t) => {
	if (!curve.length) return 0;
	if (t <= curve[0][0]) return curve[0][1];
	for (let i = 1; i < curve.length; i++) if (t <= curve[i][0]) {
		const [t0, d0] = curve[i - 1], [t1, d1] = curve[i];
		return t1 === t0 ? d1 : d0 + (d1 - d0) * (t - t0) / (t1 - t0);
	}
	return curve[curve.length - 1][1];
};

// toasts
const toast = (msg, kind, ms) => {
	const t = h('div', { class: 'toast ' + (kind || '') }, msg,
		h('button', { 'aria-label': 'Dismiss', onclick: () => t.remove() }, '×'));
	$('#toasts').append(t);
	setTimeout(() => t.remove(), ms || (kind === 'err' ? 12000 : 5000));
};

// API: cookie session; a 401 while signed in = session gone
const anon = mode => ({ authenticated: false, mode: mode || 'basic', user: '' });
let failures = 0, sess = anon();
const signedIn = () => !!sess.authenticated;
const sessionLost = () => { if (!signedIn()) return; sess = anon(sess.mode); toast('Session expired — sign in again', 'warn'); applyAuth(); };
const api = async (path, opt) => {
	opt = opt || {};
	if (MOCK) return mock(path, opt).catch(e => { if (e.status === 401) sessionLost(); throw e; });
	const headers = Object.assign({}, opt.headers || {});
	if (opt.method && opt.method !== 'GET') headers['X-N5-Fangov-Csrf'] = '1';
	if (opt.json !== undefined) { headers['Content-Type'] = 'application/json'; opt.body = JSON.stringify(opt.json); }
	let r;
	try { r = await fetch(path, { method: opt.method || 'GET', headers, body: opt.body, cache: 'no-store', credentials: 'same-origin' }); }
	catch (e) { failures++; connState(); throw new Error('network: ' + e.message); }
	failures = r.status < 500 ? 0 : failures + 1; connState();
	const ct = r.headers.get('content-type') || '';
	const body = r.status === 204 ? null : ct.includes('json') ? await r.json().catch(() => null) : await r.text();
	if (!r.ok) {
		if (r.status === 401) sessionLost();
		const msg = body && typeof body === 'object'
			? (body.error || body.message || '') + (Array.isArray(body.errors) ? '\n' + body.errors.join('\n') : '')
			: String(body || r.statusText);
		const err = new Error(msg || `HTTP ${r.status}`); err.status = r.status; err.body = body; throw err;
	}
	return { status: r.status, body };
};
const connState = () => {
	$('#banner').hidden = failures < 2;
	$('#h-live').classList.toggle('err', failures >= 2);
};
// downloads: fetch (cookie rides along) + blob anchor, CSP-safe
const saveBlob = (b, name) => { const u = URL.createObjectURL(b), a = h('a', { href: u, download: name }); document.body.append(a); a.click(); a.remove(); setTimeout(() => URL.revokeObjectURL(u), 30000); };
const download = async (path, fallback) => {
	if (MOCK) { const r = await api(path); return saveBlob(new Blob([typeof r.body === 'string' ? r.body : JSON.stringify(r.body)]), r.filename || fallback); }
	const r = await fetch(path, { cache: 'no-store', credentials: 'same-origin' });
	if (r.status === 401) { sessionLost(); throw new Error('sign in first'); }
	if (!r.ok) { const b = await r.json().catch(() => null); throw new Error(b && b.error || `HTTP ${r.status}`); }
	const m = /filename="?([^";]+)/.exec(r.headers.get('content-disposition') || '');
	return saveBlob(await r.blob(), m ? m[1] : fallback);
};

// mock: ?mock=1 anonymous, &user=1 signed in, &auth=none auth off, &tls=off|file|soon|fallback, &tab=<id>
const mock = (() => {
	const t0 = Date.now() / 1000;
	const M = { auth: Q.get('auth') === 'none' ? 'none' : 'basic', in: Q.get('user') === '1' || Q.get('auth') === 'none', user: 'admin', remember: false };
	const cfg = { daemon: { interval: '10s', step_up: 40, step_down: 15, stall_min_duty: 60, stall_cycles: 2, profile: 'auto' },
		web: { listen: '0.0.0.0:8010', auth: M.auth }, log: { file: '/var/log/n5-fangov/n5-fangov.log', max_size_mb: 10, max_files: 5 },
		channel: [
			{ name: 'cpu', pwm: 1, sensor: 'k10temp', curve: [[45, 85], [80, 255]], critical: 88, stop: 'auto' },
			{ name: 'ssd', pwm: 2, sensor: 'nvme:max', curve: [[40, 74], [70, 255]], critical: 75, stop: 'auto' },
			{ name: 'hdd', pwm: 3, sensor: 'drivetemp:max', curve: [[36, 105], [46, 255]], critical: 56, stop: 87 }] };
	const shift = n => cfg.channel.map(c => Object.assign({}, c, { curve: c.curve.map(p => [p[0] + n, p[1]]) }));
	const presets = {
		'n5pro-balanced': { builtin: true, description: 'Recommended: HDDs held near 40 °C, audible under load only', ch: cfg.channel },
		'n5pro-quiet': { builtin: true, description: 'Quiet: lowest noise, HDDs around 45 °C', ch: shift(4) }, summer: { ch: shift(-4) } };
	const overrides = {};
	const temp = (name, t) => ({ cpu: 38 + 9 * Math.sin(t / 900) + 3 * Math.sin(t / 130), ssd: 41 + 4 * Math.sin(t / 1400 + 1), hdd: 39 + 2.5 * Math.sin(t / 2600 + 2) })[name];
	// sensor catalogue: id → [description, base curve, offset]
	const SENS = { k10temp: ['CPU Tctl', 'cpu', 0], 'nvme:max': ['hottest NVMe', 'ssd', 0], 'drivetemp:max': ['hottest drive', 'hdd', 0], 'ec:ambient': ['EC ambient', 'sys', -6], 'ec:board': ['EC mainboard', 'sys', 2], 'ec:cpu': ['EC CPU probe', 'cpu', 1.5],
		'ec:system': ['EC system', 'sys', 0], 'hwmon:acpitz:temp1': ['ACPI thermal zone', 'sys', 5], 'hwmon:nic1:temp1': ['NIC PHY', 'sys', 18], 
		'hwmon:spd5118:temp1': ['DIMM 0 SPD', 'sys', 8], 'hwmon:amdgpu:temp1': ['iGPU edge', 'cpu', -3], 'hwmon:minisforum_n5_it5571:temp1': ['EC chip', 'sys', 4] };
	const PAT = [{ id: 'hwmon:<name>:tempN', description: 'any hwmon device by name and temperature index' }, { id: 'ec:<label>', description: 'extra temperature of the detected fan controller profile' }];
	const sv = (id, t) => { const [, src, off] = SENS[id]; return +((src === 'sys' ? 32 + 1.5 * Math.sin(t / 700) : temp(src, t)) + off).toFixed(1); };
	let dash = ['hwmon:amdgpu:temp1', 'hwmon:nic1:temp1'];
	const rpmOf = (name, d) => Math.round(interp(REF[name], d) + 20 * Math.sin(d));
	const point = t => { const p = { ts: Math.floor(t), temp: {}, duty: {}, rpm: {} };
		for (const c of cfg.channel) { const tv = temp(c.name, t); const d = c.name in overrides ? overrides[c.name] : Math.round(interp(c.curve, tv));
			p.temp[c.name] = +tv.toFixed(1); p.duty[c.name] = d; p.rpm[c.name] = rpmOf(c.name, d); }
		if (dash.length) { p.extra = {}; for (const id of dash) if (SENS[id]) p.extra[id] = sv(id, t); } return p; };
	const sec = n => `[${n}]\n` + Object.entries(cfg[n]).map(([k, v]) => `${k} = ${typeof v === 'string' ? `"${v}"` : v}`).join('\n') + '\n\n';
	const raw = () => sec('daemon') + sec('web') + sec('log') + cfg.channel.map(tomlChannel).join('\n');
	const logs = [];
	for (let i = 0; i < 200; i++) { const t = t0 - (200 - i) * 300; const p = point(t);
		logs.push(`${new Date(t * 1000).toISOString().slice(0, 19)} ${i % 37 === 5 ? 'WARN stall: hdd rpm=0 at duty=105' : i % 53 === 7 ? 'ERROR sensor drivetemp:max: no devices' : 'INFO'} cpu ${p.temp.cpu}/${p.duty.cpu} hdd ${p.temp.hdd}/${p.duty.hdd}`); }
	const wait = v => new Promise(r => setTimeout(r, 120, v)), ok = (body, filename) => wait({ status: 200, body, filename });
	const q = Q.get('tls'), T = { mode: q === 'off' || q === 'file' || q === 'fallback' ? (q === 'fallback' ? 'file' : q) : 'auto', fb: q === 'fallback', n: 0 };
	const tlsInfo = () => { const up = T.mode === 'file' && !T.fb, cn = up ? 'CN=fans.example,O=Homelab' : 'CN=n5.lan,O=n5-fangov', d = new Date(t0 * 1000); d.setFullYear(d.getFullYear() + (up ? 1 : 10));
		return { subject: cn, issuer: up ? 'CN=Homelab CA' : cn, dns_names: up ? ['fans.example'] : ['n5.lan', 'n5host', 'localhost'], ips: up ? [] : ['192.0.2.20', '127.0.0.1', '::1'],
			not_before: new Date(t0 * 1000 - 36e5).toISOString(), not_after: q === 'soon' ? new Date(t0 * 1000 + 12 * 864e5).toISOString() : d.toISOString(), is_ca: !up, key_algo: up ? 'RSA 2048' : 'ECDSA P-256',
			serial_hex: '3F0' + T.n + 'A9C1', fingerprint_sha256: Array.from({ length: 32 }, (_, i) => ((i * 37 + T.n * 11) % 256 | 256).toString(16).slice(1).toUpperCase()).join(':') }; };
	const fail = (msg, status, extra) => Promise.reject(Object.assign(new Error(msg), { status, body: Object.assign({ error: msg }, extra || {}) }));
	const lacks = (...hs) => hs.map(x => 'SAN list lacks host ' + x);
	const mockTLS = (p, opt) => {
		if (p === '/api/tls') return ok({ mode: T.fb ? 'auto (fallback from file)' : T.mode, fallback: !!T.fb, info: T.mode === 'off' ? null : tlsInfo(), hosts: ['192.0.2.20', 'n5.lan', 'n5host', 'localhost'],
			warnings: T.mode === 'file' && !T.fb ? lacks('192.0.2.20', 'n5.lan', 'n5host') : q === 'soon' ? ['certificate expires in 12 days'] : [] });
		if (T.mode === 'off') return fail('tls is off', 409);
		if (p === '/api/tls/cert.crt') return ok('-----BEGIN CERTIFICATE-----\nMIIBmock\n-----END CERTIFICATE-----\n', 'n5-fangov-n5host.crt');
		if (p === '/api/tls/cert.cer') return ok('0\u0082\u0001mock', 'n5-fangov-n5host.cer');
		if (p === '/api/tls/regenerate') { if (T.mode === 'file') return fail('custom certificate active; reset to auto first', 409);
			T.n++; const keep = !opt.json || opt.json.keep_key !== false; return ok({ ok: true, keep_key: keep, info: tlsInfo(), warning: keep ? undefined : 'new private key: re-download and trust the certificate' }); }
		if (p === '/api/tls/upload') { const j = opt.json || {}; if (!/BEGIN CERTIFICATE/.test(j.cert || '') || !/PRIVATE KEY/.test(j.key || '')) return fail('certificate: no PEM CERTIFICATE block', 400);
			if (!j.force) return fail('certificate does not cover "' + location.hostname + '"', 400, { host: location.hostname, force_required: true });
			T.mode = 'file'; T.fb = false; T.n++; return ok({ ok: true, mode: 'file', info: tlsInfo(), warnings: lacks('192.0.2.20', 'n5host') }); }
		if (p === '/api/tls/reset') { T.mode = 'auto'; T.fb = false; return ok({ ok: true, mode: 'auto', info: tlsInfo() }); }
		return fail('mock: not found ' + p, 404);
	};
	// alerts panel
	const A = { transport: 'auto', mail_to: 'root', tpl: { installed: true, current: true, writable: true, path: '/etc/pve/notification-templates/default' } };
	const KINDS = { sensor: 'sensor unreadable', stall: 'fan at 0 rpm', temp: 'critical temperature', write: 'pwm write failed', config: 'config replaced', 'config-channels': 'channel set changed', restart: 'daemon restarted', failed: 'unit failed', kernel: 'kernel/DKMS changed', tls: 'certificate unreadable', test: 'test alert' };
	const recent = [[720, 'stall', 'hdd: rpm=0 at duty 105, raised to 255'], [11220, 'sensor', 'drivetemp:max: no devices'], [93600, 'restart', 'n5-fangov 0.3.0-beta.1 started'], [3 * 86400, 'tls', 'certificate unreadable']]
		.map(([ago, kind, msg]) => ({ ts: Math.floor(t0 - ago), kind, msg }));
	const effective = () => A.transport === 'auto' || A.transport === 'pve' ? 'pve-notify' : A.transport;
	const alertStatus = () => ({ transport: A.transport, effective: effective(), mail_to: A.mail_to, pve_available: true, mail_available: false, template: Object.assign({}, A.tpl), cooldown: '10m0s', kinds: Object.entries(KINDS).map(([kind, description]) => ({ kind, description })) });
	const iso = ago => new Date((t0 - ago) * 1000).toISOString();
	const sessions = () => [{ id: 'a1b2c3d4', created: iso(5400), expires: iso(5400 - (M.remember ? 30 : .5) * 86400), last_seen: iso(30), remember: M.remember, ip: '192.0.2.30', current: true },
		{ id: '9f8e7d6c', created: iso(6 * 86400), expires: iso(-24 * 86400), last_seen: iso(4 * 3600), remember: true, ip: '192.0.2.31', current: false }];
	const GH = 'https://github.com/', PUB = /^\/api\/(version|about|session|login|logout|state|history)$/;
	return (path, opt) => {
		const m = opt.method || 'GET', u = new URL(path, location.origin), p = u.pathname, now = Date.now() / 1000;
		if (p === '/api/session') return ok({ authenticated: M.in, mode: M.auth, user: M.in ? M.user : undefined, remember: M.remember, via: M.in && M.auth === 'basic' ? 'cookie' : 'none' });
		if (p === '/api/login') { const j = opt.json || {}; if (j.user !== M.user || j.password !== 'admin') return fail('invalid user or password', 401);
			M.in = true; M.remember = !!j.remember; return ok({ ok: true, user: M.user, expires: Math.floor(now + (M.remember ? 30 : .5) * 86400), remember: M.remember }); }
		if (p === '/api/logout') { M.in = M.auth === 'none'; return wait({ status: 204, body: null }); }
		if (p === '/api/version') return ok({ name: 'n5-fangov', version: '0.3.0-beta.1', prerelease: 'beta.1', auth: M.auth, tls: T.mode !== 'off' });
		if (p === '/api/about') return ok({ name: 'n5-fangov', version: '0.3.0-beta.1', prerelease: 'beta.1', license: 'GPL-2.0-only', license_url: 'https://www.gnu.org/licenses/old-licenses/gpl-2.0.html',
			repo: GH + 'SirRenix/n5-fangov', author: 'SirRenix', author_url: GH + 'SirRenix', go: 'go1.25.1',
			credits: [['ltdstudio/minisforum-n5-it5571', 'the kernel driver'], ['Sl0thC0der/proxfansx', 'dashboard idea']].map(([name, note]) => ({ name, url: GH + name, note })) });
		if (p === '/api/state') { const pt = point(now), stall = (now | 0) % 40 < 3;
			const body = { ts: pt.ts, status: 'ok', profile: 'n5pro', verified: true, dry_run: false, uptime_s: 435723,
				channels: cfg.channel.map(c => { const n = c.name, st = n === 'hdd' && stall; return { name: n, pwm: c.pwm, sensor: c.sensor, temp: pt.temp[n], duty: pt.duty[n], target: n === 'cpu' ? pt.duty.cpu + 22 : pt.duty[n], rpm: st ? 0 : pt.rpm[n],
					mode: n in overrides ? 'manual' : st ? 'stall' : 'auto' }; }) };
			if (M.in) Object.assign(body, { hwmon_path: '/sys/class/hwmon/hwmon14', extra_temps: { 'ec:cpu': +(pt.temp.cpu + 1.5).toFixed(1) }, alerts: { stall: Math.floor(now - 720) }, watched: pt.extra || {} });
			return ok(body); }
		if (p === '/api/history') { const since = +u.searchParams.get('since') || 0, out = [];
			for (let t = now - 7200; t <= now; t += 10) if (t > since) { const pt = point(t); if (!M.in) delete pt.extra; out.push(pt); } return ok(out); }
		if (!M.in && !PUB.test(p)) return fail('unauthorized', 401);
		if (p === '/api/config' && m === 'GET') return ok({ config: cfg, raw: raw() });
		if (p === '/api/config' && m === 'PUT') { if (/critical = 9\d\d/.test(opt.body)) return fail('validation failed\nchannel cpu: critical out of range 30..110', 400);
			const n = (opt.body.match(/\[\[channel\]\]/g) || []).length; return wait({ status: n === cfg.channel.length ? 200 : 202, body: { ok: true } }); }
		if (p === '/api/sensors') return ok(Object.entries(SENS).map(([id, [d]]) => ({ id, description: `${d} (now ${sv(id, now)} °C)`, temp: sv(id, now) })).concat(PAT));
		if (p === '/api/dashboard') { if (m === 'PUT') { const ids = (opt.json || {}).sensors || []; if (ids.length > 8) return fail('at most 8 sensors', 400);
				dash = ids; return ok({ ok: true, sensors: dash, warnings: ids.filter(i => !SENS[i]).map(i => i + ': unresolved') }); }
			return ok({ sensors: dash }); }
		if (p.startsWith('/api/override/')) { const n = p.split('/')[3];
			if (m === 'DELETE') { delete overrides[n]; return ok({ ok: true }); }
			if (n === 'hdd' && opt.json.duty < 60) return fail('duty 40 below stall_min_duty 60 for hdd', 400);
			overrides[n] = opt.json.duty; return ok({ ok: true }); }
		if (p === '/api/presets') return ok(Object.entries(presets).map(([name, v]) => ({ name, channels: v.ch.map(c => c.name), builtin: !!v.builtin, description: v.description })));
		if (p.startsWith('/api/presets/')) { const n = p.split('/')[3], b = presets[n] && presets[n].builtin;
			if (m === 'PUT') { if (b) return fail('built-in preset', 409); presets[n] = { ch: cfg.channel }; return ok({ ok: true }); }
			if (m === 'DELETE') { if (b) return fail('built-in preset', 409); if (!presets[n]) return fail('no such preset', 404); delete presets[n]; return ok({ ok: true }); }
			return wait({ status: n === 'summer' ? 202 : 200, body: { ok: true } }); }
		if (p === '/api/log' && m === 'DELETE') { logs.length = 0; return ok({ cleared: true, note: 'journal untouched' }); }
		if (p === '/api/log') return ok({ lines: logs.slice(-(+u.searchParams.get('lines') || 100)), source: 'file' });
		if (p === '/api/log/export') return ok(logs.join('\n') + '\n', 'n5-fangov-mock-20260915-120000.log');
		if (p === '/api/config/export') return ok({ format: 1, version: '0.3.0-beta.1', exported: Math.floor(t0), config: raw(), presets: {} }, 'n5-fangov-settings-mock.json');
		if (p === '/api/config/import') { let j; try { j = JSON.parse(opt.body); } catch (e) { j = null; }
			if (!j || j.format !== 1) return fail('import rejected: bundle format missing\nexpected "format": 1', 400);
			return wait({ status: /restart/.test(opt.body) ? 202 : 200, body: { ok: true } }); }
		if (p === '/api/profiles') return ok([{ name: 'n5pro', title: 'Minisforum N5 Pro (IT5571 EC)', verified: true, notes: 'EC does not resume HDD regulation after a write; stop = fixed duty.' }, { name: 'nct67xx', title: 'Nuvoton NCT67xx (SmartFan IV)', verified: false, notes: 'Auto = pwmN_enable 5.' }]);
		if (p === '/api/alerts') { if (m === 'PUT') { const j = opt.json || {}; if (!/^(auto|pve|mail|log|off)$/.test(j.transport)) return fail('transport: unknown value', 400);
				if (/[\s"']/.test(j.mail_to || '')) return fail('mail_to: no spaces or quotes', 400); A.transport = j.transport; A.mail_to = j.mail_to || 'root'; return ok({ ok: true, status: alertStatus() }); }
			const last = {}; for (const r of recent) if (!(r.kind in last)) last[r.kind] = r.ts; return ok(Object.assign(alertStatus(), { last, recent: recent.slice(0, 50) })); }
		if (p === '/api/alerts/test') { const e = effective(); if (e === 'mail') return fail('mail: exit status 127', 502, { transport: e });
			recent.unshift({ ts: Math.floor(now), kind: 'test', msg: 'test alert from the dashboard' }); return ok({ ok: true, transport: e }); }
		if (p === '/api/alerts/template') { A.tpl.installed = A.tpl.current = true; return ok({ ok: true, path: A.tpl.path }); }
		if (p === '/api/account') return ok({ user: M.user, mode: M.auth, sessions: sessions() });
		if (p.startsWith('/api/account/')) { const j = opt.json || {}; if (M.auth === 'none') return fail('auth is none', 409);
			if (p.endsWith('/sessions/revoke')) return ok({ ok: true, revoked: j.others ? 1 : 0 });
			if (j.current_password !== 'admin') return fail('current password wrong', 403);
			if (p.endsWith('/password')) return ok({ ok: true });
			if (p.endsWith('/user')) { if (!/^[A-Za-z0-9_.-]{1,32}$/.test(j.user || '')) return fail('user: invalid', 400); M.user = j.user; return ok({ ok: true, user: M.user }); } }
		if (p.startsWith('/api/tls')) return mockTLS(p, opt);
		return fail('mock: not found ' + p, 404);
	};
})();

// chart engine
const charts = [];
function chart(wrap, series, opt) {
	const cv = $('canvas', wrap), tip = $('.tip', wrap);
	const st = wrap._st || (wrap._st = { hover: null });
	st.series = series; st.opt = opt;
	const draw = () => {
		const dpr = window.devicePixelRatio || 1, W = wrap.clientWidth, H = wrap.clientHeight;
		if (!W || !H) return;
		cv.width = W * dpr; cv.height = H * dpr;
		const ctx = cv.getContext('2d'); ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
		const fg2 = cssVar('--fg2'), fg3 = cssVar('--fg3'), line = cssVar('--line');
		const pad = { l: 40, r: 58, t: 8, b: 22 }, pw = W - pad.l - pad.r, ph = H - pad.t - pad.b;
		const all = series.flatMap(s => s.data);
		if (!all.length) { ctx.fillStyle = fg3; ctx.font = '12px system-ui'; ctx.fillText('no history', pad.l, H / 2); return; }
		const now = Date.now() / 1000, x0 = Math.min(now - 7200, all[0][0]), x1 = now;
		let yMin = opt.yMin, yMax = opt.yMax;
		if (yMin === undefined || yMax === undefined) {
			let lo = Infinity, hi = -Infinity; for (const [, v] of all) { if (v < lo) lo = v; if (v > hi) hi = v; }
			if (!isFinite(lo)) { lo = 0; hi = 1; }
			const span = Math.max(hi - lo, opt.minSpan || 10), m = span * .15;
			if (yMin === undefined) yMin = Math.floor((lo - m) / 5) * 5; if (yMax === undefined) yMax = Math.ceil((hi + m) / 5) * 5;
		}
		const X = t => pad.l + (t - x0) / (x1 - x0) * pw, Y = v => pad.t + (1 - (v - yMin) / (yMax - yMin)) * ph;
		st.X = X; st.pad = pad; st.pw = pw;
		ctx.font = '11px system-ui'; ctx.textBaseline = 'middle'; ctx.textAlign = 'right';
		const step = niceStep((yMax - yMin) / 4);
		for (let v = Math.ceil(yMin / step) * step; v <= yMax + 1e-9; v += step) {
			const y = Math.round(Y(v)) + .5; ctx.strokeStyle = line; seg(ctx, pad.l, y, W - pad.r, y);
			ctx.fillStyle = fg3; ctx.fillText(opt.fmt(v, true), pad.l - 6, y);
		}
		ctx.textAlign = 'center'; ctx.textBaseline = 'top';
		for (let t = Math.ceil(x0 / 1800) * 1800; t <= x1; t += 1800) {
			const x = Math.round(X(t)) + .5; ctx.strokeStyle = line; seg(ctx, x, pad.t, x, pad.t + ph);
			ctx.fillStyle = fg3; ctx.fillText(hm(t), x, pad.t + ph + 6);
		}
		const labels = [];
		for (const s of series) {
			if (!s.data.length) continue;
			const col = cssVar(s.color);
			ctx.beginPath(); s.data.forEach(([t, v], i) => { const x = X(t), y = Y(clamp(v, yMin, yMax)); i ? ctx.lineTo(x, y) : ctx.moveTo(x, y); });
			ctx.strokeStyle = col; ctx.lineWidth = 2; ctx.lineJoin = 'round'; ctx.stroke();
			const last = s.data[s.data.length - 1];
			ctx.lineTo(X(last[0]), pad.t + ph); ctx.lineTo(X(s.data[0][0]), pad.t + ph); ctx.closePath();
			ctx.globalAlpha = .08; ctx.fillStyle = col; ctx.fill(); ctx.globalAlpha = 1;
			labels.push({ y: Y(clamp(last[1], yMin, yMax)), col, txt: opt.fmt(last[1]) });
		}
		labels.sort((a, b) => a.y - b.y); for (let i = 1; i < labels.length; i++) if (labels[i].y - labels[i - 1].y < 13) labels[i].y = labels[i - 1].y + 13;
		ctx.textAlign = 'left'; ctx.textBaseline = 'middle'; ctx.font = '600 11px system-ui';
		for (const l of labels) { ctx.fillStyle = l.col; ctx.fillText(l.txt, W - pad.r + 6, l.y); }
		if (st.hover !== null) {
			const t = st.hover; const x = Math.round(X(t)) + .5; ctx.strokeStyle = fg2; seg(ctx, x, pad.t, x, pad.t + ph, [3, 3]);
			for (const s of series) { const p = nearest(s.data, t); if (p) dot(ctx, X(p[0]), Y(clamp(p[1], yMin, yMax)), cssVar(s.color)); }
		}
	};
	st.draw = draw;
	if (!wrap._bound) {
		wrap._bound = true; charts.push(wrap);
		const move = ev => {
			const r = cv.getBoundingClientRect(), x = ev.clientX - r.left, s = wrap._st;
			if (!s.X || x < s.pad.l || x > s.pad.l + s.pw) return leave();
			const now = Date.now() / 1000, x0 = now - 7200; s.hover = x0 + (x - s.pad.l) / s.pw * 7200; s.draw();
			clear(tip); tip.append(h('div', { class: 't' }, hm(s.hover)));
			for (const sr of s.series) { const p = nearest(sr.data, s.hover); if (!p) continue;
				const key = h('i'); key.style.background = cssVar(sr.color);
				tip.append(h('div', null, h('span', null, key, sr.name), h('b', null, s.opt.fmt(p[1])))); }
			tip.hidden = false; const tw = tip.offsetWidth; tip.style.left = (x + 12 + tw > r.width ? x - tw - 12 : x + 12) + 'px'; tip.style.top = Math.min(ev.clientY - r.top + 12, r.height - tip.offsetHeight - 4) + 'px';
		};
		const leave = () => { const s = wrap._st; if (s.hover === null) return; s.hover = null; tip.hidden = true; s.draw(); };
		cv.addEventListener('pointermove', move); cv.addEventListener('pointerleave', leave);
		new ResizeObserver(() => wrap._st.draw()).observe(wrap);
	}
	draw();
}
const seg = (ctx, x0, y0, x1, y1, dash) => { ctx.setLineDash(dash || []); ctx.beginPath(); ctx.moveTo(x0, y0); ctx.lineTo(x1, y1); ctx.stroke(); ctx.setLineDash([]); };
const niceStep = r => { const p = Math.pow(10, Math.floor(Math.log10(r || 1))), f = r / p; return (f < 1.5 ? 1 : f < 3.5 ? 2 : f < 7.5 ? 5 : 10) * p; };
const dot = (ctx, x, y, col) => { ctx.beginPath(); ctx.arc(x, y, 4.5, 0, 7); ctx.fillStyle = col; ctx.fill(); ctx.strokeStyle = cssVar('--bg2'); ctx.lineWidth = 2; ctx.stroke(); };
const nearest = (data, t) => { if (!data.length) return null; let lo = 0, hi = data.length - 1;
	while (hi - lo > 1) { const m = (lo + hi) >> 1; data[m][0] < t ? lo = m : hi = m; }
	return Math.abs(data[lo][0] - t) < Math.abs(data[hi][0] - t) ? data[lo] : data[hi]; };
const redrawAll = () => { for (const w of charts) w._st && w._st.draw(); drawEds(); };

// state
let snap = null, cfg = null, cfgRaw = '', profiles = [], sensors = [], version = '', tls = null, dash = [], alerts = null, builtinNames = [];
const LOOPBACK = /^(localhost|127\.\d+\.\d+\.\d+|\[::1\])$/i.test(location.hostname);
let cert = null;
const dLeft = iso => Math.ceil((new Date(iso) - Date.now()) / 86400e3);
const secState = () => { const e = $('#h-sec'), on = tls === null ? location.protocol === 'https:' : !!tls, i = cert && cert.info;
	e.textContent = on ? '🔒 TLS' : '🔓 HTTP'; e.className = 'meta sec ' + (on ? 'ok' : LOOPBACK ? '' : 'warn');
	e.title = (on ? 'TLS connection' : LOOPBACK ? 'plain HTTP on loopback' : 'plain HTTP off loopback — credentials travel unencrypted')
		+ (cert ? `\ncertificate: ${cert.mode}` + (i ? ` · expires ${i.not_after.slice(0, 10)}${dLeft(i.not_after) < 30 ? ' (soon!)' : ''}` : '') : '') + '\nclick for the certificate panel'; };
let hist = [], lastTs = 0, fanMetric = 'rpm';
const critOf = name => { const c = cfg && chList().find(x => x.name === name); return c && +c.critical > 0 ? +c.critical : null; };
const chList = () => (cfg && (cfg.channel || cfg.channels)) || [];
const tempClass = (t, crit) => { if (t === null || t <= -900) return 'na';
	const r = crit ? t / crit : t / 75; return r < .6 ? 't-ok' : r < .85 ? 't-warm' : r < 1 ? 't-hot' : 't-crit'; };
const isHdd = c => /hdd|drive|disk/.test(c.name) || /^drivetemp/.test(c.sensor || '');
const minDuty = c => isHdd(c) ? ((cfg && cfg.daemon && +cfg.daemon.stall_min_duty) || 60) : 0;
const seriesColor = i => SERIES[i % SERIES.length];
const modeBadge = (el, m) => { el.className = 'mode m-' + (m || 'unknown'); el.textContent = m || 'unknown'; };
const chanHead = (c, ...pre) => h('span', null, ...pre, h('span', { class: 'name' }, c.name), h('span', { class: 'sub' }, `pwm${c.pwm} · ${c.sensor || '?'}`));
const act = async (fn, ok) => { try { const r = await fn(); if (ok) toast(ok, 'ok'); return r; } catch (e) { toast(e.message, 'err'); } };
const verTxt = v => v ? 'verified on hardware' : 'from documentation · untested';
const preBadge = (el, pre) => { el.hidden = !pre; el.textContent = (pre || '').split('.')[0]; };

// header
function renderHeader() {
	if (!snap) return;
	const pr = profiles.find(p => p.name === snap.profile);
	$('#h-profile').textContent = pr ? pr.title : snap.profile || '—';
	const b = $('#h-verified'); b.hidden = false;
	b.className = 'badge ' + (snap.verified ? 'ok' : 'warn'); b.textContent = verTxt(snap.verified);
	const st = snap.dry_run ? 'dry-run' : snap.status || 'unknown';
	const c = $('#h-status'); c.className = 'chip ' + st; c.textContent = st;
	$('#h-uptime').textContent = 'up ' + fmtUp(snap.uptime_s || 0);
	if (version) $('#h-version').textContent = 'v' + version.replace(/^v/, '');
}

// auth: mirrors the server's visibility split
async function applyAuth() {
	const on = signedIn(), basic = sess.mode !== 'none';
	for (const t of $$('#tabs [data-auth]')) t.hidden = !on;
	for (const [id, show] of [['#h-settings', on], ['#h-sec', on], ['#h-signin', !on && basic], ['#h-signout', on && basic], ['#h-user', on && basic], ['#s-account', on && basic], ['#ov-more', on]]) $(id).hidden = !show;
	$('#h-user').textContent = sess.user || '';
	if ($('#tab-' + curTab).hidden) selectTab('overview');
	if (!on) { cert = null; cfg = null; edState = null; alerts = null; dash = []; profiles = []; SN.key = null; for (const id of ['#editors', '#presets', '#log']) clear($(id)); secState(); renderCharts(); return; }
	hist = []; lastTs = 0; // history is re-read with the extra series
	await loadConfig(); loadCert(); loadDash(); loadAlerts(); resetHistory();
	api('/api/profiles').then(r => { profiles = r.body || []; renderHeader(); renderSystem(); renderProfiles(); }).catch(() => {});
	if (curTab === 'curves') loadEditor(); poll();
}
const ld = $('#login'), lf = $('#login-f'), lErr = notice('#l-err');
on('#h-signin', 'click', () => { lErr(''); ld.showModal(); $('#l-user').focus(); });
on('#l-close', 'click', () => ld.close()); backdrop(ld); ld.addEventListener('close', () => lf.reset());
lf.addEventListener('submit', async ev => { ev.preventDefault(); const u = $('#l-user').value.trim(), p = $('#l-pass').value; if (!u || !p) return lErr('user and password needed');
	const bt = $('#l-submit'); bt.disabled = true; lErr('');
	try { const r = await api('/api/login', { method: 'POST', json: { user: u, password: p, remember: $('#l-remember').checked } });
		sess = { authenticated: true, mode: 'basic', user: r.body.user || u }; ld.close(); toast('Signed in', 'ok'); applyAuth(); }
	catch (e) { lErr(e.status === 401 ? 'invalid user or password' : e.status === 429 ? 'too many attempts' : e.message); $('#l-pass').value = ''; $('#l-pass').focus(); }
	bt.disabled = false; });
on('#h-signout', 'click', async () => { try { await api('/api/logout', { method: 'POST' }); } catch (e) {}
	sess = anon(sess.mode); toast('Signed out', ''); applyAuth(); });

// overview
const cards = {};
function renderCards() {
	const host = $('#cards');
	const names = snap.channels.map(c => c.name);
	for (const k of Object.keys(cards)) if (!names.includes(k)) { cards[k].el.remove(); delete cards[k]; }
	snap.channels.forEach((c, i) => {
		let k = cards[c.name];
		if (!k) {
			k = cards[c.name] = { el: h('div', { class: 'card ch' }) };
			k.mode = h('span', { class: 'mode' }); k.dot = h('i', { class: 'dot' });
			k.temp = h('div', { class: 'temp' }); k.duty = h('span', { class: 'v' }); k.bar = h('i'); k.tgt = h('b', { hidden: true }); k.rpm = h('span', { class: 'v' });
			k.el.append(h('div', { class: 'top' }, chanHead(c, k.dot), k.mode), k.temp,
				h('div', { class: 'row' }, h('span', { class: 'k' }, 'duty'), h('div', { class: 'bar-h' }, k.bar, k.tgt), k.duty),
				h('div', { class: 'row' }, h('span', { class: 'k' }, 'fan'), h('span'), k.rpm));
			host.append(k.el);
		}
		k.dot.style.background = `var(${seriesColor(i)})`;
		const crit = critOf(c.name);
		modeBadge(k.mode, c.mode);
		k.temp.className = 'temp ' + tempClass(c.temp, crit);
		clear(k.temp).append(fmtT(c.temp), h('small', null, unit() + (crit ? ` · crit ${fmtT(crit, 0)}` : '')));
		k.bar.style.width = pct(c.duty) + '%'; k.bar.className = { critical: 'crit', failsafe: 'crit', stall: 'stall', auto: 'auto' }[c.mode] || '';
		const slewing = c.target !== undefined && c.target !== c.duty && c.mode !== 'stall';
		k.tgt.hidden = !slewing; k.tgt.style.left = `calc(${pct(c.target)}% - 1px)`;
		clear(k.duty).append(`${pct(c.duty)} % `, h('span', { class: 'tg' }, `(${c.duty}${slewing ? ' → ' + c.target : ''})`));
		k.rpm.textContent = c.rpm < 0 ? 'no tach' : c.rpm.toLocaleString('en') + ' rpm';
	});
}
function renderSystem() {
	if (!snap || !signedIn()) return;
	const pr = profiles.find(p => p.name === snap.profile) || {}, d = cfg && cfg.daemon || {}, lg = cfg && cfg.log || {};
	kv($('#sys'), [['profile', `${snap.profile || '?'}${pr.title ? ' — ' + pr.title : ''}`], ['hwmon', snap.hwmon_path || '—'], ['verified', snap.verified ? 'yes, on hardware' : 'no (documentation)'],
		['notes', pr.notes || '—'], ['interval', d.interval || '—'], ['version', version ? 'v' + version.replace(/^v/, '') : '—'], ['log file', lg.file || 'journal only']]);
}
// alerts list (Overview card + Alerts tab)
const alertList = (el, recent, empty) => { clear(el); if (!recent.length) el.append(h('li', { class: 'empty' }, empty || 'no alerts'));
	for (const a of recent) el.append(h('li', null, h('span', { class: 'k ' + a.kind }, a.kind), h('span', { class: 'msg' }, a.msg || ''), tm(a.ts))); };
const alNotice = notice('#al-notice');
async function loadAlerts() { if (!signedIn()) return;
	try { alerts = (await api('/api/alerts')).body; alertList($('#alerts'), alerts.recent || []); alNotice(''); if (curTab === 'alerts') renderAlertsTab(); }
	catch (e) { if (e.status === 401) return; alerts = null; alertList($('#alerts'), [], 'alerts: unavailable'); alertList($('#al-recent'), []); alNotice('alerts: ' + e.message); } }
// sensors card, grouped by id prefix / hwmon chip
const GROUPS = [['CPU', /^(k10temp|coretemp)/], ['SSD · NVMe', /^nvme/], ['HDD', /^drivetemp/], ['GPU', /^(amdgpu|nouveau|i915|radeon)/], ['NIC', /^(nic|eth|mlx|igc|ixgbe|r8169|atlantic)/], ['EC · board', /^(ec$|minisforum|acpitz|spd5118)/]];
const groupOf = id => { const k = id.startsWith('hwmon:') ? id.slice(6) : id.split(':')[0], g = GROUPS.find(x => x[1].test(k)); return g ? g[0] : 'other'; };
const SN = { key: null, rows: {} };
const concrete = () => sensors.filter(s => !s.id.includes('<')); // id patterns (with <) are not selectable
function renderSensors() {
	const host = $('#sensors'), sensors = concrete(), key = sensors.map(s => s.id).join(',');
	if (key !== SN.key) { SN.key = key; SN.rows = {}; clear(host); const by = {};
		for (const s of sensors) (by[groupOf(s.id)] = by[groupOf(s.id)] || []).push(s);
		for (const g of [...GROUPS.map(x => x[0]), 'other']) { if (!by[g]) continue; const box = h('div', { class: 'sg' }, h('h3', null, g));
			for (const s of by[g]) { const r = SN.rows[s.id] = { v: h('b', { class: 'v' }), b: h('button', { class: 'btn sm' }, 'chart') };
				r.b.addEventListener('click', () => setDash(dash.includes(s.id) ? dash.filter(x => x !== s.id) : dash.concat(s.id)));
				box.append(h('div', { class: 'sn' }, h('span', { class: 'id mono' }, s.id), h('span', { class: 'd' }, (s.description || '').replace(/\s*\(now [^)]*\)\s*$/, '')), r.v, r.b)); }
			host.append(box); }
		if (!sensors.length) host.append(h('span', { class: 'empty' }, 'no readable sensors')); }
	for (const s of sensors) { const r = SN.rows[s.id], on = dash.includes(s.id), v = snap && snap.watched && snap.watched[s.id] !== undefined ? snap.watched[s.id] : s.temp;
		r.v.textContent = v === undefined || v === null ? '—' : fmtT(v) + ' ' + unit(); r.b.classList.toggle('on', on); r.b.setAttribute('aria-pressed', String(on)); r.b.disabled = !on && dash.length >= 8;
		}
}
async function pollSensors(force) { if (!signedIn() || !force && curTab !== 'overview') return;
	try { sensors = (await api('/api/sensors')).body || []; renderSensors(); fillSensorSelects(); } catch (e) {} }
async function loadDash() { try { dash = (await api('/api/dashboard')).body.sensors || []; renderSensors(); renderCharts(); } catch (e) {} }
async function setDash(ids) { const r = await act(() => api('/api/dashboard', { method: 'PUT', json: { sensors: ids } })); if (!r) return;
	dash = r.body.sensors || ids; const w = r.body.warnings || []; if (w.length) toast(w.join('\n'), 'warn'); renderSensors(); renderCharts(); resetHistory(); }
function renderCharts() {
	if (!snap) return;
	const names = snap.channels.map(c => c.name);
	const mk = (key, f) => names.map((n, i) => ({ name: n, color: seriesColor(i), data: hist.filter(p => p[key] && p[key][n] > -900).map(p => [p.ts, f ? f(p[key][n]) : p[key][n]]) }));
	const legend = (el, ss, rm) => { clear(el); for (const s of ss) { const i = h('i'); i.style.background = `var(${s.color})`;
		el.append(h('span', null, i, s.name, rm ? h('button', { class: 'x', 'aria-label': 'remove ' + s.name, onclick: () => rm(s.name) }, '×') : null)); } };
	const tf = { fmt: (v, ax) => v.toFixed(ax ? 0 : 1) + (ax ? '' : ' ' + unit()), minSpan: 15 };
	const ts = mk('temp', tC); legend($('#lg-temp'), ts);
	chart($('#ch-temp'), ts, tf);
	const fs = mk(fanMetric); legend($('#lg-fan'), fs);
	$('#ch-fan-title').textContent = (fanMetric === 'rpm' ? 'Fan speed' : 'Duty') + ' · last 2 h';
	chart($('#ch-fan'), fs, fanMetric === 'rpm' ? { fmt: (v, ax) => ax ? String(Math.round(v)) : Math.round(v) + ' rpm', yMin: 0, minSpan: 1000 } : { fmt: (v, ax) => ax ? String(Math.round(v)) : `${Math.round(v)} (${pct(v)} %)`, yMin: 0, yMax: 255 });
	// extra sensors chart, only while something is watched
	const on = signedIn() && dash.length > 0; $('#extra-card').hidden = !on; if (!on) return;
	const es = dash.map((id, i) => ({ name: id, color: seriesColor(names.length + i), data: hist.filter(p => p.extra && p.extra[id] > -900).map(p => [p.ts, tC(p.extra[id])]) }));
	legend($('#lg-extra'), es, id => setDash(dash.filter(x => x !== id))); chart($('#ch-extra'), es, tf);
}
$$('.seg button').forEach(b => b.addEventListener('click', () => { fanMetric = b.dataset.metric; $$('.seg button').forEach(x => { x.classList.toggle('on', x === b); x.setAttribute('aria-pressed', x === b); }); renderCharts(); }));

// curves
const ED = {}, drawEds = () => { for (const k in ED) ED[k].draw(); }; let edState = null;
const tomlChannel = c => `[[channel]]\nname = "${c.name}"\npwm = ${c.pwm}\nsensor = "${c.sensor}"\ncurve = [${c.curve.map(p => `[${p[0]}, ${p[1]}]`).join(', ')}]\ncritical = ${c.critical}\nstop = ${c.stop === 'auto' || c.stop === '' || c.stop === undefined ? '"auto"' : c.stop}\n`;
const stripChannels = raw => { const out = []; let skip = false;
	for (const ln of raw.split('\n')) { const t = ln.trim();
		if (/^\[\[channel\]\]/.test(t)) { skip = true; continue; }
		if (/^\[[^\[]/.test(t)) skip = false;
		if (!skip) out.push(ln); } return out.join('\n').replace(/\n{3,}/g, '\n\n').trimEnd() + '\n\n'; };
function loadEditor() {
	edState = chList().map(c => ({ name: c.name, pwm: c.pwm, sensor: c.sensor, curve: (c.curve || []).map(p => [+p[0], +p[1]]), critical: +c.critical, stop: c.stop === undefined || c.stop === 'auto' ? 'auto' : String(c.stop) }));
	const host = clear($('#editors')); for (const k in ED) delete ED[k];
	$('#cv-notice').hidden = true;
	edState.forEach((c, i) => {
		const ed = ED[c.name] = { c, i };
		const cv = h('canvas', { role: 'img', 'aria-label': `curve ${c.name}` });
		const sel = h('select', { onchange: () => { c.sensor = sel.value; } });
		const crit = h('input', { type: 'number', min: 30, max: 120, value: c.critical, oninput: () => { c.critical = +crit.value; ed.draw(); } });
		const stop = h('input', { type: 'text', value: c.stop, placeholder: 'auto', oninput: () => { c.stop = stop.value.trim(); } });
		const tbody = h('tbody');
		const ref = REF[c.name] && snap && snap.profile === 'n5pro' ? h('p', { class: 'ref' }, 'Duty → RPM (measured): ', ...REF[c.name].flatMap(([d, r], j) => [j ? ' · ' : '', h('b', null, `${d}→${r}`)])) : null;
		ed.sel = sel; ed.cv = cv; ed.tbody = tbody;
		const fillTable = () => {
			clear(tbody); c.curve.forEach((p, j) => tbody.append(h('tr', null,
				h('td', null, h('input', { type: 'number', min: 0, max: 120, value: p[0], 'aria-label': `point ${j + 1} temp`, oninput: ev => { p[0] = +ev.target.value; ed.draw(); } })),
				h('td', null, h('input', { type: 'number', min: 0, max: 255, value: p[1], 'aria-label': `point ${j + 1} duty`, oninput: ev => { p[1] = clamp(+ev.target.value, 0, 255); ed.draw(); } })),
				h('td', null, h('button', { class: 'btn sm', disabled: c.curve.length <= 2, onclick: () => { c.curve.splice(j, 1); fillTable(); ed.draw(); } }, 'remove')))));
			addBtn.disabled = c.curve.length >= 8;
		};
		// re-sort once focus leaves the table (a rebuild mid-click swallows the click)
		ed.sort = () => { const before = c.curve.slice(); c.curve.sort((a, b) => a[0] - b[0]); if (before.some((x, k) => x !== c.curve[k])) { fillTable(); ed.draw(); } };
		tbody.addEventListener('focusout', ev => { if (!tbody.contains(ev.relatedTarget)) ed.sort(); });
		// add point: middle of the widest gap, inserted sorted
		const at = h('input', { type: 'number', min: 0, max: 120, 'aria-label': 'new point temp' }), ad = h('input', { type: 'number', min: 0, max: 255, 'aria-label': 'new point duty' });
		const addRow = h('div', { class: 'addp' }, h('label', null, '°C', at), h('label', null, 'duty', ad),
			h('button', { class: 'btn sm primary', onclick: () => { const t = +at.value, d = clamp(Math.round(+ad.value), 0, 255); if (at.value === '') return at.focus();
				if (c.curve.some(p => p[0] === t)) return toast(`${c.name}: a point at ${t} °C exists`, 'warn');
				const np = [t, d]; c.curve.push(np); c.curve.sort((a, b) => a[0] - b[0]); addRow.hidden = true; fillTable(); ed.draw(); tbody.rows[c.curve.indexOf(np)].cells[0].firstChild.focus(); } }, 'Add'),
			h('button', { class: 'btn sm', onclick: () => { addRow.hidden = true; addBtn.focus(); } }, 'Cancel'));
		addRow.hidden = true;
		const addBtn = h('button', { class: 'btn sm', onclick: () => { const s = c.curve.slice().sort((a, b) => a[0] - b[0]); let bi = 1, bw = -1;
			for (let k = 1; k < s.length; k++) if (s[k][0] - s[k - 1][0] > bw) { bw = s[k][0] - s[k - 1][0]; bi = k; }
			const t = s.length > 1 ? Math.round((s[bi - 1][0] + s[bi][0]) / 2) : (s[0] ? s[0][0] : 40) + 5;
			at.value = t; ad.value = Math.round(interp(s, t)); addRow.hidden = false; at.focus(); at.select(); } }, '+ add point');
		host.append(h('div', { class: 'card ed' },
			h('div', { class: 'top' }, chanHead(c),
				h('label', null, 'sensor', sel), h('label', null, 'critical °C', crit), h('label', null, 'stop', stop)),
			h('div', { class: 'cvs' }, cv),
			h('div', { class: 'pts' }, h('table', null, h('thead', null, h('tr', null, h('th', null, '°C'), h('th', null, 'duty'), h('th'))), tbody), h('div', { class: 'addc' }, addBtn, addRow)),
			ref));
		fillTable();
		ed.draw = () => drawCurve(ed);
		bindCurveDrag(ed);
		new ResizeObserver(ed.draw).observe(cv.parentNode);
	});
	fillSensorSelects();
}
function fillSensorSelects() {
	for (const k in ED) { const { c, sel } = ED[k]; const ids = new Set(concrete().map(s => s.id)); ids.add(c.sensor); clear(sel);
		for (const id of ids) { const s = sensors.find(x => x.id === id); sel.append(h('option', { value: id, selected: id === c.sensor }, id + (s && s.temp !== undefined ? ` (${fmtT(s.temp)} ${unit()})` : ''))); } }
}
const curveGeom = ed => { const W = ed.cv.clientWidth, H = ed.cv.clientHeight, pad = { l: 34, r: 12, t: 10, b: 22 };
	const xmax = Math.max(100, (ed.c.critical || 0) + 10);
	return { W, H, pad, xmax, X: t => pad.l + t / xmax * (W - pad.l - pad.r), Y: d => pad.t + (1 - d / 255) * (H - pad.t - pad.b),
		T: x => (x - pad.l) / (W - pad.l - pad.r) * xmax, D: y => (1 - (y - pad.t) / (H - pad.t - pad.b)) * 255 }; };
function drawCurve(ed) {
	const { cv, c } = ed, dpr = window.devicePixelRatio || 1, g = curveGeom(ed); if (!g.W) return;
	cv.width = g.W * dpr; cv.height = g.H * dpr; const ctx = cv.getContext('2d'); ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
	ctx.fillStyle = cssVar('--bg'); ctx.fillRect(0, 0, g.W, g.H);
	ctx.font = '11px system-ui'; ctx.fillStyle = cssVar('--fg3'); ctx.strokeStyle = cssVar('--line'); ctx.textAlign = 'right'; ctx.textBaseline = 'middle';
	for (let d = 0; d <= 255; d += 51) { const y = Math.round(g.Y(d)) + .5; seg(ctx, g.pad.l, y, g.W - g.pad.r, y); ctx.fillText(pct(d) + '%', g.pad.l - 5, y); }
	ctx.textAlign = 'center'; ctx.textBaseline = 'top';
	for (let t = 0; t <= g.xmax; t += 20) { const x = Math.round(g.X(t)) + .5; seg(ctx, x, g.pad.t, x, g.H - g.pad.b); ctx.fillText(fmtT(t, 0) + '°', x, g.H - g.pad.b + 5); }
	if (c.critical) { const x = Math.round(g.X(c.critical)) + .5; ctx.strokeStyle = cssVar('--crit'); seg(ctx, x, g.pad.t, x, g.H - g.pad.b, [4, 3]);
		ctx.fillStyle = cssVar('--crit'); ctx.textAlign = 'right'; ctx.fillText('crit', x - 3, g.pad.t); }
	const col = cssVar(seriesColor(ed.i)), pts = c.curve.slice().sort((a, b) => a[0] - b[0]);
	ctx.beginPath(); ctx.moveTo(g.X(0), g.Y(pts[0][1]));
	for (const p of pts) ctx.lineTo(g.X(clamp(p[0], 0, g.xmax)), g.Y(clamp(p[1], 0, 255)));
	ctx.lineTo(g.X(g.xmax), g.Y(pts[pts.length - 1][1]));
	ctx.strokeStyle = col; ctx.lineWidth = 2; ctx.stroke();
	ctx.lineTo(g.X(g.xmax), g.Y(0)); ctx.lineTo(g.X(0), g.Y(0)); ctx.closePath(); ctx.globalAlpha = .1; ctx.fillStyle = col; ctx.fill(); ctx.globalAlpha = 1;
	for (const p of pts) dot(ctx, g.X(clamp(p[0], 0, g.xmax)), g.Y(clamp(p[1], 0, 255)), col);
	const live = snap && snap.channels.find(x => x.name === c.name);
	if (live && live.temp > -900) { const x = g.X(clamp(live.temp, 0, g.xmax)), y = g.Y(interp(pts, live.temp));
		ctx.strokeStyle = cssVar('--fg2'); seg(ctx, x, g.pad.t, x, g.H - g.pad.b, [2, 3]);
		dot(ctx, x, y, cssVar('--fg'));
		ctx.fillStyle = cssVar('--fg2'); ctx.textAlign = x > g.W / 2 ? 'right' : 'left'; ctx.textBaseline = 'bottom'; ctx.fillText(`now ${fmtT(live.temp)}${unit()} → ${Math.round(interp(pts, live.temp))}`, x + (x > g.W / 2 ? -8 : 8), y - 6); }
}
function bindCurveDrag(ed) {
	const { cv, c } = ed; let drag = -1;
	const pos = ev => { const r = cv.getBoundingClientRect(); return [ev.clientX - r.left, ev.clientY - r.top]; };
	cv.addEventListener('pointerdown', ev => { const a = document.activeElement; if (ed.tbody.contains(a)) a.blur(); ed.sort(); // sorted neighbours for the clamp
		const g = curveGeom(ed), [x, y] = pos(ev); let best = 14, bi = -1;
		c.curve.forEach((p, i) => { const d = Math.hypot(g.X(p[0]) - x, g.Y(p[1]) - y); if (d < best) { best = d; bi = i; } });
		if (bi >= 0) { drag = bi; cv.setPointerCapture(ev.pointerId); ev.preventDefault(); } });
	cv.addEventListener('pointermove', ev => { if (drag < 0) return; const g = curveGeom(ed), [x, y] = pos(ev);
		const lo = drag ? c.curve[drag - 1][0] + 1 : 0, hi = drag < c.curve.length - 1 ? c.curve[drag + 1][0] - 1 : g.xmax;
		c.curve[drag] = [clamp(Math.round(g.T(x)), lo, hi), clamp(Math.round(g.D(y)), 0, 255)];
		const row = ed.tbody.rows[drag]; if (row) { row.cells[0].firstChild.value = c.curve[drag][0]; row.cells[1].firstChild.value = c.curve[drag][1]; } ed.draw(); });
	const up = () => { drag = -1; }; cv.addEventListener('pointerup', up); cv.addEventListener('pointercancel', up);
}
const validateCurves = () => { const errs = [];
	for (const c of edState) { const n = c.curve.length; if (n < 2 || n > 8) errs.push(`${c.name}: ${n} points (need 2..8)`);
		for (let i = 1; i < n; i++) if (c.curve[i][0] <= c.curve[i - 1][0]) errs.push(`${c.name}: temps must ascend (point ${i + 1})`);
		for (const p of c.curve) if (!Number.isFinite(p[0]) || !Number.isFinite(p[1]) || p[1] < 0 || p[1] > 255) errs.push(`${c.name}: bad point [${p}]`);
		if (!(c.critical > 0)) errs.push(`${c.name}: critical missing`);
		if (c.stop !== 'auto' && !(/^\d+$/.test(c.stop) && +c.stop <= 255)) errs.push(`${c.name}: stop must be "auto" or 0..255`); }
	return errs; };
on('#cv-apply', 'click', async () => {
	for (const k in ED) ED[k].sort(); const n = $('#cv-notice'), errs = validateCurves();
	if (errs.length) { n.hidden = false; n.className = 'notice err'; n.textContent = errs.join('\n'); return; }
	const body = stripChannels(cfgRaw) + edState.map(tomlChannel).join('\n');
	try { const r = await api('/api/config', { method: 'PUT', body, headers: { 'Content-Type': 'application/toml' } });
		const warn = r.body && Array.isArray(r.body.warnings) && r.body.warnings.length ? 'Daemon replaced invalid values by defaults:\n' + r.body.warnings.join('\n') : '';
		n.hidden = r.status !== 202 && !warn; n.className = 'notice'; n.textContent = (r.status === 202 ? 'Saved — restart required (channel set or profile changed): systemctl restart n5-fangov\n' : '') + warn;
		toast(r.status === 202 ? 'Config written, restart required' : warn ? 'Applied with warnings' : 'Curves applied', warn ? 'warn' : 'ok'); await loadConfig(); loadEditor();
	} catch (e) { n.hidden = false; n.className = 'notice err'; n.textContent = e.message; toast('Rejected', 'err'); }
});
on('#cv-revert', 'click', () => { loadEditor(); toast('Reverted', ''); });

// manual
const MN = {};
function renderManual() {
	const host = $('#manual');
	for (const c of snap.channels) {
		let m = MN[c.name];
		if (!m) {
			const cc = chList().find(x => x.name === c.name) || c, min = minDuty(cc);
			m = MN[c.name] = {}; m.val = h('div', { class: 'val' }); m.mode = h('span', { class: 'mode' }); m.warn = h('div', { class: 'warn' });
			m.range = h('input', { type: 'range', min: 0, max: 255, value: c.duty, id: 'rg-' + c.name, oninput: () => { m.dirty = 1; m.show(+m.range.value); } });
			m.show = v => { clear(m.val).append(pct(v) + ' %', h('small', null, `${v}/255`)); m.warn.textContent = min && v < min ? `HDD-like channel: minimum ${min} (EC stops regulating it; the daemon refuses lower values).` : ''; };
			m.set = h('button', { class: 'btn primary', onclick: async () => { const v = +m.range.value; m.dirty = 0;
				if (min && v < min) return toast(`${c.name}: duty below ${min} refused`, 'warn');
				await act(() => api('/api/override/' + c.name, { method: 'PUT', json: { duty: v } }), `${c.name}: manual ${v} (${pct(v)} %)`); poll(); } }, 'Set');
			m.auto = h('button', { class: 'btn', onclick: async () => { m.dirty = 0; await act(() => api('/api/override/' + c.name, { method: 'DELETE' }), `${c.name}: back to auto`); poll(); } }, 'Back to auto');
			m.cur = h('span', { class: 'hint' });
			host.append(h('div', { class: 'card mn' }, h('div', { class: 'top' }, chanHead(c), m.mode),
				m.val, h('label', { for: 'rg-' + c.name, class: 'sr' }, `${c.name} duty`), m.range, m.warn, h('div', { class: 'act' }, m.set, m.auto, m.cur)));
			m.show(c.duty);
		}
		modeBadge(m.mode, c.mode);
		m.cur.textContent = `current ${c.duty} · ${c.rpm < 0 ? 'no tach' : c.rpm + ' rpm'}`;
		if (c.mode !== 'manual' && !m.dirty) { m.range.value = c.duty; m.show(c.duty); }
	}
}

// presets (built-ins: badge, no save-over, no delete)
const chanSummary = chs => (chs || []).map(c => c.name || c).join(' · ');
async function loadPresets() {
	const host = $('#presets'); try {
		const list = (await api('/api/presets')).body || []; clear(host);
		builtinNames = list.filter(p => p.builtin).map(p => p.name);
		if (!list.length) host.append(h('p', { class: 'empty' }, 'No presets yet.'));
		for (const p of list) host.append(h('div', { class: 'card ps' },
			h('span', { class: 'name' }, p.name, p.builtin ? h('span', { class: 'badge builtin' }, 'built-in') : null, /^recommended/i.test(p.description || '') || p.name === 'n5pro-balanced' ? h('span', { class: 'badge rec' }, 'recommended') : null),
			h('span', { class: 'sum' }, p.description ? h('span', { class: 'desc' }, p.description) : null, chanSummary(p.channels)),
			h('button', { class: 'btn', onclick: async () => { if (!confirm(`Apply preset “${p.name}”? Curves change immediately.`)) return;
				const r = await act(() => api(`/api/presets/${encodeURIComponent(p.name)}/apply`, { method: 'POST' }), `Preset ${p.name} applied`); if (!r) return;
				const n = $('#ps-notice'); n.hidden = r.status !== 202; n.textContent = 'Preset written — restart required: systemctl restart n5-fangov'; await loadConfig(); loadEditor(); } }, 'Apply'),
			p.builtin ? null : h('button', { class: 'btn danger', onclick: async () => { if (!confirm(`Delete preset “${p.name}”?`)) return;
				if (await act(() => api('/api/presets/' + encodeURIComponent(p.name), { method: 'DELETE' }), `Preset ${p.name} deleted`)) loadPresets(); } }, 'Delete')));
	} catch (e) { clear(host).append(h('p', { class: 'empty' }, 'presets: ' + e.message)); }
}
on('#ps-save', 'submit', async ev => { ev.preventDefault(); const n = $('#ps-name').value.trim();
	if (builtinNames.includes(n)) return toast(`“${n}” is a built-in preset — pick another name`, 'err');
	if (await act(() => api('/api/presets/' + encodeURIComponent(n), { method: 'PUT' }), `Saved current curves as “${n}”`)) { $('#ps-name').value = ''; loadPresets(); } });

// alerts tab; unsaved form edits survive the refresh
let alDirty = false; on('#al-form', 'input', () => { alDirty = true; });
function renderAlertsTab() {
	const a = alerts; if (!a) return; const t = a.template || {};
	if (!alDirty) { $('#al-transport').value = a.transport || 'auto'; $('#al-mailto').value = a.mail_to || ''; }
	const av = x => x ? 'available' : 'not available'; kv($('#al-status'), [['effective', a.effective || '—'], ['pve-notify', av(a.pve_available)], ['mail(1)', av(a.mail_available)], ['cooldown', a.cooldown || '—']]);
	$('#al-tpl-card').hidden = !a.pve_available;
	const yn = x => x ? 'yes' : 'no'; kv($('#al-tpl'), [['installed', yn(t.installed)], ['current', !t.installed ? '—' : t.current ? 'yes' : 'no — outdated'], ['writable', yn(t.writable)], ['path', h('dd', { class: 'mono' }, t.path || '—')]]);
	const b = $('#al-tpl-btn'), upToDate = t.installed && t.current; b.textContent = t.installed ? 'Update template' : 'Install template'; b.disabled = !t.writable || upToDate;
	$('#al-tpl-reason').textContent = !t.writable ? (t.reason || 'not writable') : upToDate ? 'up to date' : '';
	const tb = clear($('#al-kinds tbody')), last = a.last || {};
	for (const k of a.kinds || []) tb.append(h('tr', null, h('td', { class: 'mono' }, k.kind), h('td', null, k.description || ''), h('td', null, tm(last[k.kind] || 0))));
	alertList($('#al-recent'), a.recent || []);
}
on('#al-form', 'submit', async ev => { ev.preventDefault();
	const r = await act(() => api('/api/alerts', { method: 'PUT', json: { transport: $('#al-transport').value, mail_to: $('#al-mailto').value.trim() } })); if (!r) return;
	alDirty = false; toast('Transport saved — effective: ' + (r.body.status && r.body.status.effective), 'ok'); loadAlerts(); });
on('#al-test', 'click', async () => {
	try { const r = await api('/api/alerts/test', { method: 'POST' }); toast('Test alert sent via ' + r.body.transport, 'ok'); }
	catch (e) { if (e.status === 409) return toast('Test alert already running', 'warn'); toast('Test alert failed' + (e.body && e.body.transport ? ` (${e.body.transport})` : '') + ': ' + e.message, 'err'); }
	loadAlerts(); });
on('#al-tpl-btn', 'click', async () => { const r = await act(() => api('/api/alerts/template', { method: 'POST' })); if (r) { toast('Template written to ' + r.body.path, 'ok'); loadAlerts(); } });

// about (public)
function renderAbout(a) {
	$('#ab-name').textContent = a.name || 'n5-fangov'; $('#ab-version').textContent = 'v' + (a.version || version || '?').replace(/^v/, ''); preBadge($('#ab-beta'), a.prerelease);
	const link = (id, href, text) => { const e = $(id); e.href = href || '#'; e.textContent = text || href || '—'; };
	link('#ab-license', a.license_url, a.license); link('#ab-repo', a.repo, (a.repo || '').replace(/^https?:\/\//, '')); link('#ab-author', a.author_url, a.author);
	$('#ab-go').textContent = a.go || '—';
	const ul = clear($('#ab-credits')); for (const c of a.credits || []) ul.append(h('li', null, h('a', { href: c.url, rel: 'noopener', target: '_blank' }, c.name), c.note ? ' — ' + c.note : ''));
}

// log
let logLines = [];
const logLine = l => typeof l === 'string' ? l : JSON.stringify(l);
async function loadLog() {
	try { const r = await api('/api/log?lines=200'); const b = r.body;
		logLines = (b && b.lines || []).map(logLine); renderLog();
		const src = b && b.source || ''; $('#lg-src').textContent = src ? 'source: ' + src : ''; $('#lg-clear').disabled = src === 'journal';
		$('#lg-clear').title = src === 'journal' ? 'Log file disabled — the journal cannot be cleared' : 'Truncate the log file (rotated files and journal untouched)';
	} catch (e) { $('#log').textContent = 'log: ' + e.message; }
}
on('#lg-export', 'click', () => act(() => download('/api/log/export', 'n5-fangov.log')));
on('#lg-clear', 'click', async () => {
	if (!confirm('Clear the current log file? Rotated files and the systemd journal are untouched.')) return;
	const r = await act(() => api('/api/log', { method: 'DELETE' })); if (!r) return;
	toast('Log cleared — ' + (r.body && r.body.note || 'journal untouched'), 'ok'); loadLog();
});
function renderLog() {
	const q = $('#lg-filter').value.toLowerCase(), pre = clear($('#log'));
	for (const l of logLines) { if (q && !l.toLowerCase().includes(q)) continue;
		pre.append(h('span', { class: /error|fail/i.test(l) ? 'e' : /warn/i.test(l) ? 'w' : '' }, l + '\n')); }
	if ($('#lg-auto').checked) pre.scrollTop = pre.scrollHeight;
}
on('#lg-filter', 'input', renderLog); on('#lg-refresh', 'click', loadLog);

// compatibility
function renderProfiles() {
	const tb = clear($('#profiles tbody'));
	for (const p of profiles) { const act = snap && snap.profile === p.name;
		tb.append(h('tr', { class: act ? 'active' : '' }, h('td', { class: 'mono' }, p.name, act ? h('span', { class: 'act' }, 'ACTIVE') : null), h('td', null, p.title || ''),
			h('td', null, h('span', { class: 'badge ' + (p.verified ? 'ok' : 'warn') }, verTxt(p.verified))), h('td', null, p.notes || ''))); }
}

// loading & polling
async function loadConfig() {
	try { const r = await api('/api/config'); const b = r.body;
		cfgRaw = b.raw || ''; cfg = b.config || { channel: [] };
	} catch (e) { if (e.status !== 401) toast('config: ' + e.message, 'err'); }
}
// a reset bumps the generation; a late answer of the old load is dropped
let histGen = 0;
async function loadHistory() {
	const g = histGen;
	try { const r = await api('/api/history?minutes=120' + (lastTs ? '&since=' + lastTs : '')); if (g !== histGen) return;
		const pts = (r.body || []).filter(p => p.ts > lastTs).sort((a, b) => a.ts - b.ts);
		if (pts.length) { hist = hist.concat(pts); lastTs = hist[hist.length - 1].ts; }
		const cut = Date.now() / 1000 - 7200; while (hist.length && (hist[0].ts < cut || hist.length > 720)) hist.shift();
		renderCharts();
	} catch (e) {}
}
const resetHistory = () => { hist = []; lastTs = 0; histGen++; loadHistory(); };
let timer = null, histTimer = null, alTimer = null, polling = false, repoll = false;
async function poll() { // a call mid-poll queues one more round
	if (polling) { repoll = true; return; } polling = true;
	try { const r = await api('/api/state'); snap = r.body; renderHeader(); renderCards(); renderManual(); renderSystem();
		if (curTab === 'curves') drawEds(); }
	catch (e) {}
	await pollSensors();
	polling = false; if (repoll) { repoll = false; poll(); }
}
const liveState = () => { const l = $('#h-live'); const paused = document.hidden; l.classList.toggle('paused', paused); l.lastChild.textContent = paused ? 'paused' : 'live'; };
function schedule() {
	clearInterval(timer); clearInterval(histTimer); clearInterval(alTimer); liveState();
	if (document.hidden) return;
	poll(); loadHistory();
	timer = setInterval(poll, S.interval * 1000); histTimer = setInterval(loadHistory, 30000);
	alTimer = setInterval(() => { if (curTab === 'overview' || curTab === 'alerts') loadAlerts(); }, 60000);
}
document.addEventListener('visibilitychange', schedule);

// tabs
let curTab = 'overview';
const TABS = $$('#tabs [role=tab]');
function selectTab(id, focus) {
	if ($('#tab-' + id).hidden) return;
	curTab = id;
	TABS.forEach(t => { const on = t.id === 'tab-' + id; t.setAttribute('aria-selected', on); t.tabIndex = on ? 0 : -1; $('#' + t.getAttribute('aria-controls')).hidden = !on; if (on && focus) t.focus(); });
	({ overview: () => { renderCharts(); pollSensors(); loadAlerts(); }, presets: loadPresets, log: loadLog, compat: renderProfiles, alerts: () => { alDirty = false; renderAlertsTab(); loadAlerts(); }, about: () => {},
		curves: () => { if (!edState) loadEditor();
			pollSensors(1); drawEds(); } })[id]();
}
TABS.forEach(t => { t.addEventListener('click', () => selectTab(t.id.slice(4)));
	t.addEventListener('keydown', ev => { const vis = TABS.filter(x => !x.hidden), i = vis.indexOf(t), d = { ArrowRight: 1, ArrowLeft: -1, Home: -i, End: vis.length - 1 - i }[ev.key];
		if (d === undefined) return; ev.preventDefault(); selectTab(vis[(i + d + vis.length) % vis.length].id.slice(4), true); }); });

// settings UI
const sb = $('#h-settings'), sp = $('#settings'), showS = on => { sp.hidden = !on; sb.setAttribute('aria-expanded', on); };
sb.addEventListener('click', () => showS(sp.hidden));
document.addEventListener('click', ev => { if (!sp.hidden && !sp.contains(ev.target) && ev.target !== sb) showS(false); });
document.addEventListener('keydown', ev => { if (ev.key === 'Escape' && !sp.hidden) { showS(false); sb.focus(); } });
$('#s-unit').value = S.unit; $('#s-interval').value = String(S.interval); $('#s-theme').value = S.theme;
on('#s-unit', 'change', ev => { S.unit = ev.target.value; saveS(); if (snap) { renderCards(); renderCharts(); renderSensors(); fillSensorSelects(); redrawAll(); } });
on('#s-interval', 'change', ev => { S.interval = +ev.target.value; saveS(); schedule(); });
on('#s-theme', 'change', ev => { S.theme = ev.target.value; saveS(); document.documentElement.dataset.theme = S.theme; redrawAll(); });
matchMedia('(prefers-color-scheme: light)').addEventListener('change', () => { if (S.theme === 'system') redrawAll(); });
document.documentElement.dataset.theme = S.theme;
// settings bundle
const gNotice = notice('#g-notice');
on('#s-export', 'click', () => { showS(false); act(() => download('/api/config/export', 'n5-fangov-settings.json')); });
on('#s-import', 'click', () => $('#s-file').click());
on('#s-file', 'change', async () => {
	const inp = $('#s-file'), f = inp.files[0]; inp.value = ''; if (!f) return;
	if (f.size > 1 << 20) return toast('Settings file exceeds 1 MiB', 'err');
	const text = await f.text(); let j = null; try { j = JSON.parse(text); } catch (e) {}
	if (!j || typeof j !== 'object' || Array.isArray(j)) return toast(`${f.name}: not a JSON settings bundle`, 'err');
	if (!confirm(`Import settings from “${f.name}”?\nDaemon config and presets are replaced (after validation).`)) return;
	showS(false);
	try { const r = await api('/api/config/import', { method: 'POST', body: text, headers: { 'Content-Type': 'application/json' } });
		if (r.status === 202) { gNotice('Settings imported — restart required: systemctl restart n5-fangov'); toast('Imported, restart required', 'warn', 12000); }
		else { toast('Settings imported', 'ok'); }
		await loadConfig(); edState = null; if (curTab === 'curves') loadEditor(); if (curTab === 'presets') loadPresets(); poll();
	} catch (e) { toast(e.message, 'err', 15000); }
});

// account dialog (Settings → Account…)
const acd = $('#account'), acNotice = notice('#ac-notice');
const acReset = () => { for (const f of ['#ac-pw-f', '#ac-us-f']) { $(f).reset(); $(f).hidden = true; } };
async function loadAccount() {
	try { const a = (await api('/api/account')).body; $('#ac-user').textContent = a.user || sess.user || ''; const tb = clear($('#ac-sessions tbody')), ss = a.sessions || [];
		for (const s of ss) tb.append(h('tr', { class: s.current ? 'active' : '' }, h('td', { class: 'mono' }, s.id, s.current ? h('span', { class: 'act' }, 'THIS SESSION') : null),
			h('td', null, tm(s.created)), h('td', null, tm(s.last_seen)), h('td', null, tm(s.expires, true), s.remember ? ' · remembered' : null), h('td', { class: 'mono' }, s.ip || '')));
			} catch (e) { acNotice('account: ' + e.message, 'err'); }
}
on('#s-acc', 'click', () => { showS(false); acNotice(''); acReset(); $('#ac-user').textContent = sess.user || ''; if (!acd.open) acd.showModal(); $('#ac-pw').focus(); loadAccount(); });
on('#ac-close', 'click', () => acd.close()); backdrop(acd); acd.addEventListener('close', acReset);
$$('#account [data-cancel]').forEach(b => b.addEventListener('click', acReset));
for (const [b, f, i] of [['#ac-pw', '#ac-pw-f', '#ac-cur'], ['#ac-us', '#ac-us-f', '#ac-ucur']]) on(b, 'click', () => { acReset(); $(f).hidden = false; $(i).focus(); });
const acFail = (e, id) => { acNotice(e.message, 'err'); $(id).value = ''; $(id).focus(); };
on('#ac-pw-f', 'submit', async ev => { ev.preventDefault(); const cur = $('#ac-cur').value, n = $('#ac-new').value;
	if (n.length < 8 || n.length > 128) return acNotice('new password: 8–128 characters', 'err'); if (n !== $('#ac-new2').value) return acNotice('the two new passwords differ', 'err'); if (!cur) return acNotice('current password required', 'err');
	try { await api('/api/account/password', { method: 'POST', json: { current_password: cur, new_password: n } }); acReset(); acNotice(''); toast('Password changed', 'ok'); loadAccount(); }
	catch (e) { acFail(e, '#ac-cur'); } });
on('#ac-us-f', 'submit', async ev => { ev.preventDefault(); const cur = $('#ac-ucur').value, u = $('#ac-uname').value.trim();
	if (!/^[A-Za-z0-9_.-]{1,32}$/.test(u)) return acNotice('user name: letters, digits, _ . - (1–32)', 'err'); if (!cur) return acNotice('current password required', 'err');
	try { const r = await api('/api/account/user', { method: 'POST', json: { current_password: cur, user: u } }); sess.user = r.body.user || u; $('#h-user').textContent = sess.user; acReset(); acNotice(''); toast('User name changed to ' + sess.user, 'ok'); loadAccount(); }
	catch (e) { acFail(e, '#ac-ucur'); } });
on('#ac-revoke', 'click', async () => { if (!confirm('Sign out every other session? This one stays signed in.')) return;
	const r = await act(() => api('/api/account/sessions/revoke', { method: 'POST', json: { others: true } })); if (r) { toast(`${r.body.revoked || 0} other session(s) signed out`, 'ok'); loadAccount(); } });

// certificate panel (protected; POSTs need CSRF)
const dlg = $('#cert'), ctNotice = notice('#ct-notice');
const ctForm = id => { for (const f of ['#ct-regen-f', '#ct-upload-f']) $(f).hidden = f !== id || !$(f).hidden; };
async function loadCert() { if (!signedIn()) return; try { cert = (await api('/api/tls')).body; } catch (e) { cert = null; if (e.status !== 501 && e.status !== 401) toast('certificate: ' + e.message, 'err'); } secState(); }
function renderCert() {
	const c = cert || { mode: 'off' }, i = c.info, b = $('#ct-mode'), fb = !!c.fallback, isFile = c.mode === 'file', isAuto = c.mode === 'auto' || fb;
	b.textContent = fb ? 'automatic (fallback)' : isFile ? 'own certificate' : isAuto ? 'automatic' : 'TLS off'; b.className = 'badge ' + (fb ? 'warn' : isAuto ? 'ok' : c.mode);
	$('#ct-off').hidden = !!i; $('#ct-body').hidden = !i; $('#ct-reset').hidden = !isFile && !fb; $('#ct-regen').hidden = isFile;
	if (!i) return;
	const left = dLeft(i.not_after), until = h('dd', { class: left < 0 ? 'expired' : left < 30 ? 'soon' : '' }, i.not_after.slice(0, 10) + (left < 0 ? ' — expired' : left < 30 ? ` — in ${left} days` : ''));
	kv($('#ct-kv'), [['subject', i.subject], ['issuer', i.issuer], ['valid from', i.not_before.slice(0, 10)], ['valid until', until], ['key', i.key_algo + (i.is_ca ? ' · CA flag (trust anchor)' : '')], ['serial', h('dd', { class: 'mono' }, i.serial_hex)]]);
	const san = clear($('#ct-san')); for (const n of i.dns_names) san.append(h('span', null, n)); for (const n of i.ips) san.append(h('span', { class: 'ip' }, n));
	if (!i.dns_names.length && !i.ips.length) san.append(h('span', { class: 'empty' }, 'no SANs'));
	$('#ct-fp').textContent = i.fingerprint_sha256;
	const w = (c.warnings || []).map(x => /^SAN list lacks host/.test(x) ? x + ' — under HSTS the browser will refuse that name' : x);
	if (fb) w.unshift('The configured certificate files could not be loaded — the automatic certificate is served (see the daemon log). Upload the pair again or reset to auto.');
	if (w.length) ctNotice(w.join('\n'), 'warn');
}
const ctClearUpload = () => { for (const id of ['#ct-cpem', '#ct-kpem', '#ct-cfile', '#ct-kfile']) $(id).value = ''; $('#ct-force').checked = false; $('#ct-force-l').hidden = true; };
async function openCert() { showS(false); ctNotice(''); $('#ct-regen-f').hidden = $('#ct-upload-f').hidden = true; ctClearUpload(); await loadCert(); renderCert(); if (!dlg.open) dlg.showModal(); }
on('#h-sec', 'click', openCert); on('#s-cert', 'click', openCert);
on('#ct-close', 'click', () => dlg.close()); backdrop(dlg);
dlg.addEventListener('close', ctClearUpload);
$$('#cert [data-cancel]').forEach(b => b.addEventListener('click', () => { b.closest('form').hidden = true; ctClearUpload(); }));
for (const x of ['crt', 'cer']) on('#ct-' + x, 'click', () => act(() => download('/api/tls/cert.' + x, 'n5-fangov.' + x)));
on('#ct-copy', 'click', () => navigator.clipboard.writeText($('#ct-fp').textContent).then(() => toast('Fingerprint copied', 'ok'), () => toast('Clipboard blocked — select the text', 'warn')));
on('#ct-regen', 'click', () => { $('#ct-newkey').checked = false; ctForm('#ct-regen-f'); });
on('#ct-upload', 'click', () => ctForm('#ct-upload-f'));
// re-read /api/tls and the config (the [web] tls keys changed)
const ctDone = async (r, msg) => { const w = r.body.warning ? [r.body.warning] : r.body.warnings || [];
	toast(msg + (w.length ? ' (warnings)' : ''), w.length ? 'warn' : 'ok', 8000);
	await loadCert(); renderCert(); const have = new Set((cert && cert.warnings) || []), extra = w.filter(x => !have.has(x));
	if (extra.length) { const n = $('#ct-notice'); ctNotice((n.hidden ? '' : n.textContent + '\n') + extra.join('\n'), 'warn'); } loadConfig(); };
on('#ct-regen-f', 'submit', async ev => { ev.preventDefault(); const nk = $('#ct-newkey').checked;
	if (nk && !confirm('Generate a new private key?\nEvery browser and OS store trusting the current certificate must import the new one.')) return;
	const r = await act(() => api('/api/tls/regenerate', { method: 'POST', json: { keep_key: !nk } })); if (!r) return; $('#ct-regen-f').hidden = true; ctDone(r, 'Certificate regenerated'); });
for (const [f, ta] of [['#ct-cfile', '#ct-cpem'], ['#ct-kfile', '#ct-kpem']]) $(f).addEventListener('change', async ev => { const x = ev.target.files[0]; if (!x) return;
	if (x.size > 65536) return toast(x.name + ': larger than 64 KiB', 'err'); $(ta).value = await x.text(); });
// 400 + force_required: the leaf does not cover this session's host name
on('#ct-upload-f', 'submit', async ev => { ev.preventDefault(); const c = $('#ct-cpem').value.trim(), k = $('#ct-kpem').value.trim();
	if (!/BEGIN CERTIFICATE/.test(c) || !/PRIVATE KEY/.test(k)) return ctNotice('Need a PEM CERTIFICATE block and a PRIVATE KEY block.', 'err');
	let r; try { r = await api('/api/tls/upload', { method: 'POST', json: { cert: c, key: k, force: $('#ct-force').checked } }); }
	catch (e) { if (e.body && e.body.force_required) { $('#ct-force-l').hidden = false; $('#ct-force-h').textContent = e.body.host; ctNotice(e.message, 'err'); toast('Certificate does not cover ' + e.body.host, 'err'); } else toast(e.message, 'err'); return; }
	$('#ct-upload-f').hidden = true; ctClearUpload(); ctDone(r, 'Own certificate installed'); });
on('#ct-reset', 'click', async () => { if (!confirm('Back to the automatic certificate? The uploaded pair is deleted.')) return;
	const r = await act(() => api('/api/tls/reset', { method: 'POST' })); if (r) ctDone(r, 'Automatic certificate active'); });

// boot: session first, protected bits via applyAuth
(async () => {
	if (MOCK) toast('Mock mode', 'warn', 8000);
	api('/api/version').then(r => { version = r.body.version || ''; if (typeof r.body.tls === 'boolean') tls = r.body.tls; if (r.body.prerelease !== undefined) preBadge($('#h-beta'), r.body.prerelease); secState(); renderHeader(); }).catch(() => {});
	api('/api/about').then(r => renderAbout(r.body || {})).catch(() => {});
	try { sess = (await api('/api/session')).body || sess; } catch (e) { sess = anon(); }
	await applyAuth(); if (MOCK && Q.get('tab')) selectTab(Q.get('tab'));
	if (document.hidden) { poll(); loadHistory(); }
	schedule();
})();
})();
