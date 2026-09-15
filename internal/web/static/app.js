// n5-fangov dashboard — vanilla JS, CSP-safe
'use strict';
(() => {
const $ = (s, r) => (r || document).querySelector(s);
const $$ = s => [...document.querySelectorAll(s)];
const h = (tag, attrs, ...kids) => {
	const e = document.createElement(tag);
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
const clamp = (v, a, b) => Math.min(b, Math.max(a, v));
const SERIES = Array.from({ length: 8 }, (_, i) => '--s' + (i + 1));
const cssVar = n => getComputedStyle(document.documentElement).getPropertyValue(n).trim();
const MOCK = new URLSearchParams(location.search).get('mock') === '1';
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
const fmtT = (v, d) => v == null || !(v > -900) ? '—' : tC(v).toFixed(d === undefined ? 1 : d);
const pct = d => Math.round(d / 255 * 100);
const rel = ts => { const s = Math.max(0, Date.now() / 1000 - ts | 0);
	return s < 60 ? s + ' s ago' : s < 3600 ? (s / 60 | 0) + ' min ago' : s < 86400 ? `${s / 3600 | 0} h ${s % 3600 / 60 | 0} min ago` : `${s / 86400 | 0} d ${s % 86400 / 3600 | 0} h ago`; };
const fmtUp = s => { const d = s / 86400 | 0, hh = s % 86400 / 3600 | 0, m = s % 3600 / 60 | 0; return d ? `${d}d ${hh}h` : hh ? `${hh}h ${m}m` : `${m}m`; };
const hm = ts => { const d = new Date(ts * 1000); return String(d.getHours()).padStart(2, '0') + ':' + String(d.getMinutes()).padStart(2, '0'); };
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
		h('button', { type: 'button', 'aria-label': 'Dismiss', onclick: () => t.remove() }, '×'));
	$('#toasts').append(t);
	setTimeout(() => t.remove(), ms || (kind === 'err' ? 12000 : 5000));
};

// API
let auth = null, failures = 0, pendingLogin = null;
const api = async (path, opt) => {
	opt = opt || {};
	if (MOCK) return mock(path, opt);
	const headers = Object.assign({}, opt.headers || {});
	if (opt.method && opt.method !== 'GET') headers['X-N5-Fangov-Csrf'] = '1';
	if (opt.json !== undefined) { headers['Content-Type'] = 'application/json'; opt.body = JSON.stringify(opt.json); }
	for (;;) {
		const used = auth; if (used) headers.Authorization = used; else delete headers.Authorization;
		let r;
		try { r = await fetch(path, { method: opt.method || 'GET', headers, body: opt.body, cache: 'no-store' }); }
		catch (e) { failures++; connState(); throw new Error('network: ' + e.message); }
		// 401: first one is anonymous (silent on the daemon side) → login card; the
		// request is re-sent once credentials exist. A rejected credential is dropped.
		if (r.status === 401) { if (used && used === auth) { auth = null; toast('Sign-in failed — check user and password', 'err'); } await needLogin(); continue; }
		failures = r.status < 500 ? 0 : failures + 1; connState();
		const ct = r.headers.get('content-type') || '';
		const body = ct.includes('json') ? await r.json().catch(() => null) : await r.text();
		if (!r.ok) {
			const msg = body && typeof body === 'object'
				? (body.error || body.message || '') + (Array.isArray(body.errors) ? '\n' + body.errors.join('\n') : '')
				: String(body || r.statusText);
			const err = new Error(msg || `HTTP ${r.status}`); err.status = r.status; err.body = body; throw err;
		}
		return { status: r.status, body };
	}
};
const needLogin = () => {
	if (pendingLogin) return pendingLogin;
	const f = $('#login'); f.hidden = false; $('#l-user').focus();
	pendingLogin = new Promise(res => { f._resolve = res; });
	return pendingLogin;
};
$('#login').addEventListener('submit', ev => {
	ev.preventDefault();
	const u = $('#l-user').value, p = $('#l-pass').value;
	auth = 'Basic ' + btoa(unescape(encodeURIComponent(u + ':' + p)));
	$('#l-pass').value = ''; $('#login').hidden = true;
	const r = $('#login')._resolve; pendingLogin = null; if (r) r();
});
const connState = () => {
	$('#banner').hidden = failures < 2;
	$('#h-live').classList.toggle('err', failures >= 2);
};
// downloads go through fetch (the in-memory credential rides along) and a blob anchor — CSP-safe
const saveBlob = (b, name) => { const u = URL.createObjectURL(b), a = h('a', { href: u, download: name }); document.body.append(a); a.click(); a.remove(); setTimeout(() => URL.revokeObjectURL(u), 30000); };
const download = async (path, fallback) => {
	if (MOCK) { const r = await mock(path, {}); return saveBlob(new Blob([typeof r.body === 'string' ? r.body : JSON.stringify(r.body, null, 2)]), r.filename || fallback); }
	for (;;) {
		const used = auth, r = await fetch(path, { headers: used ? { Authorization: used } : {}, cache: 'no-store' });
		if (r.status === 401) { if (used && used === auth) auth = null; await needLogin(); continue; }
		if (!r.ok) { const b = await r.json().catch(() => null); throw new Error(b && b.error || `HTTP ${r.status}`); }
		const m = /filename="?([^";]+)/.exec(r.headers.get('content-disposition') || '');
		return saveBlob(await r.blob(), m ? m[1] : fallback);
	}
};

// mock backend
const mock = (() => {
	const t0 = Date.now() / 1000;
	const cfg = { daemon: { interval: '10s', step_up: 40, step_down: 15, stall_min_duty: 60, stall_cycles: 2, profile: 'auto' },
		web: { listen: '0.0.0.0:8010', auth: 'none' },
		channel: [
			{ name: 'cpu', pwm: 1, sensor: 'k10temp', curve: [[45, 85], [80, 255]], critical: 88, stop: 'auto' },
			{ name: 'ssd', pwm: 2, sensor: 'nvme:max', curve: [[40, 74], [70, 255]], critical: 75, stop: 'auto' },
			{ name: 'hdd', pwm: 3, sensor: 'drivetemp:max', curve: [[36, 105], [46, 255]], critical: 56, stop: 87 }] };
	const presets = { quiet: cfg.channel, summer: cfg.channel.map(c => Object.assign({}, c, { curve: c.curve.map(p => [p[0] - 4, p[1]]) })) };
	const overrides = {};
	const temp = (name, t) => ({ cpu: 38 + 9 * Math.sin(t / 900) + 3 * Math.sin(t / 130), ssd: 41 + 4 * Math.sin(t / 1400 + 1), hdd: 39 + 2.5 * Math.sin(t / 2600 + 2) })[name];
	const rpmOf = (name, d) => Math.round(interp(REF[name], d) + 20 * Math.sin(d));
	const point = t => { const p = { ts: Math.floor(t), temp: {}, duty: {}, rpm: {} };
		for (const c of cfg.channel) { const tv = temp(c.name, t); const d = c.name in overrides ? overrides[c.name] : Math.round(interp(c.curve, tv));
			p.temp[c.name] = +tv.toFixed(1); p.duty[c.name] = d; p.rpm[c.name] = rpmOf(c.name, d); } return p; };
	const sec = n => `[${n}]\n` + Object.entries(cfg[n]).map(([k, v]) => `${k} = ${typeof v === 'string' ? `"${v}"` : v}`).join('\n') + '\n\n';
	const raw = () => sec('daemon') + sec('web') + cfg.channel.map(tomlChannel).join('\n');
	const logs = [];
	for (let i = 0; i < 200; i++) { const t = t0 - (200 - i) * 300; const p = point(t);
		logs.push(`${new Date(t * 1000).toISOString().slice(0, 19)} ${i % 37 === 5 ? 'WARN stall: hdd rpm=0 at duty=105 → 255' : i % 53 === 7 ? 'ERROR sensor drivetemp:max: no devices' : 'INFO'} ` + cfg.channel.map(c => `${c.name} ${p.temp[c.name]}/${p.duty[c.name]}`).join(' ')); }
	const wait = v => new Promise(r => setTimeout(() => r(v), 120));
	// certificate mock: ?tls=off|file|soon sets the start state
	const q = new URLSearchParams(location.search).get('tls'), T = { mode: q === 'off' || q === 'file' || q === 'fallback' ? (q === 'fallback' ? 'file' : q) : 'auto', fb: q === 'fallback', n: 0 };
	const tlsInfo = () => { const up = T.mode === 'file' && !T.fb, cn = up ? 'CN=fans.example,O=Homelab' : 'CN=n5.lan,O=n5-fangov', d = new Date(t0 * 1000); d.setFullYear(d.getFullYear() + (up ? 1 : 10));
		return { subject: cn, issuer: up ? 'CN=Homelab CA' : cn, dns_names: up ? ['fans.example'] : ['n5.lan', 'n5host', 'localhost'], ips: up ? [] : ['192.0.2.20', '127.0.0.1', '::1'],
			not_before: new Date(t0 * 1000 - 36e5).toISOString(), not_after: q === 'soon' ? new Date(t0 * 1000 + 12 * 864e5).toISOString() : d.toISOString(), is_ca: !up, key_algo: up ? 'RSA 2048' : 'ECDSA P-256',
			serial_hex: '3F0' + T.n + 'A9C1', fingerprint_sha256: Array.from({ length: 32 }, (_, i) => ((i * 37 + T.n * 11) % 256 | 256).toString(16).slice(1).toUpperCase()).join(':') }; };
	const fail = (msg, status) => Promise.reject(Object.assign(new Error(msg), { status }));
	const mockTLS = (p, opt) => {
		if (p === '/api/tls') return wait({ status: 200, body: { mode: T.fb ? 'auto (fallback from file)' : T.mode, fallback: !!T.fb, info: T.mode === 'off' ? null : tlsInfo(), hosts: ['192.0.2.20', 'n5.lan', 'n5host', 'localhost'],
			warnings: T.mode === 'file' && !T.fb ? ['SAN list lacks host 192.0.2.20', 'SAN list lacks host n5.lan', 'SAN list lacks host n5host'] : q === 'soon' ? ['certificate expires in 12 days'] : [] } });
		if (T.mode === 'off') return fail('tls is off', 409);
		if (p === '/api/tls/cert.crt') return wait({ status: 200, body: '-----BEGIN CERTIFICATE-----\nMIIBmock\n-----END CERTIFICATE-----\n', filename: 'n5-fangov-n5host.crt' });
		if (p === '/api/tls/cert.cer') return wait({ status: 200, body: '0\u0082\u0001mock', filename: 'n5-fangov-n5host.cer' });
		if (p === '/api/tls/regenerate') { if (T.mode === 'file') return fail('custom certificate active; reset to auto first', 409);
			T.n++; const keep = !opt.json || opt.json.keep_key !== false; return wait({ status: 200, body: { ok: true, keep_key: keep, info: tlsInfo(), warning: keep ? undefined : 'new private key: re-download and trust the certificate' } }); }
		if (p === '/api/tls/upload') { const j = opt.json || {}; if (!/BEGIN CERTIFICATE/.test(j.cert || '') || !/PRIVATE KEY/.test(j.key || '')) return fail('certificate: no PEM CERTIFICATE block', 400);
			if (!j.force) { const e = fail('certificate does not cover "' + location.hostname + '", the name this browser session uses: under HSTS the browser would refuse the connection after the swap', 400); return e.catch(x => { x.body = { error: x.message, host: location.hostname, force_required: true }; throw x; }); }
			T.mode = 'file'; T.fb = false; T.n++; return wait({ status: 200, body: { ok: true, mode: 'file', info: tlsInfo(), warnings: ['SAN list lacks host 192.0.2.20', 'SAN list lacks host n5host'] } }); }
		if (p === '/api/tls/reset') { T.mode = 'auto'; T.fb = false; return wait({ status: 200, body: { ok: true, mode: 'auto', info: tlsInfo() } }); }
		return fail('mock: not found ' + p, 404);
	};
	return (path, opt) => {
		const m = opt.method || 'GET', u = new URL(path, location.origin), p = u.pathname;
		if (p === '/api/state') { const now = Date.now() / 1000, pt = point(now), stall = (now | 0) % 40 < 3;
			return wait({ status: 200, body: { ts: pt.ts, status: 'ok', profile: 'n5pro', verified: true, hwmon_path: '/sys/class/hwmon/hwmon14', dry_run: false, uptime_s: 435723,
				channels: cfg.channel.map(c => { const n = c.name, st = n === 'hdd' && stall; return { name: n, pwm: c.pwm, sensor: c.sensor, temp: pt.temp[n], duty: pt.duty[n], target: n === 'cpu' ? pt.duty.cpu + 22 : pt.duty[n], rpm: st ? 0 : pt.rpm[n],
					mode: n in overrides ? 'manual' : st ? 'stall' : 'auto' }; }),
				extra_temps: { 'ec:cpu': +(pt.temp.cpu + 1.5).toFixed(1), 'ec:system': 32.0, 'ec:ssd': 40.5, 'ec:hdd': 37.0 },
				alerts: { stall: Math.floor(now - 12 * 60), sensor: Math.floor(now - 3 * 3600 - 420) } } }); }
		if (p === '/api/history') { const since = +u.searchParams.get('since') || 0, now = Date.now() / 1000, out = [];
			for (let t = now - 7200; t <= now; t += 10) if (t > since) out.push(point(t)); return wait({ status: 200, body: out }); }
		if (p === '/api/config' && m === 'GET') return wait({ status: 200, body: { config: cfg, raw: raw() } });
		if (p === '/api/config' && m === 'PUT') { if (/critical = 9\d\d/.test(opt.body)) return fail('validation failed\nchannel cpu: critical out of range 30..110', 400);
			const n = (opt.body.match(/\[\[channel\]\]/g) || []).length; return wait({ status: n === cfg.channel.length ? 200 : 202, body: { ok: true } }); }
		if (p === '/api/sensors') return wait({ status: 200, body: [{ id: 'k10temp', temp: 38.2 }, { id: 'nvme:max', temp: 41 }, { id: 'drivetemp:max', temp: 39.5 }, { id: 'ec:cpu', temp: 39.7 }, { id: 'ec:system', temp: 32 }] });
		if (p.startsWith('/api/override/')) { const n = p.split('/')[3];
			if (m === 'DELETE') { delete overrides[n]; return wait({ status: 200, body: { ok: true } }); }
			if (n === 'hdd' && opt.json.duty < 60) return fail('duty 40 below stall_min_duty 60 for hdd', 400);
			overrides[n] = opt.json.duty; return wait({ status: 200, body: { ok: true } }); }
		if (p === '/api/presets') return wait({ status: 200, body: Object.keys(presets).map(k => ({ name: k, channels: presets[k] })) });
		if (p.startsWith('/api/presets/')) { const n = p.split('/')[3]; if (m === 'PUT') { presets[n] = cfg.channel; return wait({ status: 201, body: { ok: true } }); }
			return wait({ status: n === 'summer' ? 202 : 200, body: { ok: true } }); }
		if (p === '/api/log' && m === 'DELETE') { logs.length = 0; return wait({ status: 200, body: { cleared: true, note: 'journal untouched' } }); }
		if (p === '/api/log') return wait({ status: 200, body: { lines: logs.slice(-(+u.searchParams.get('lines') || 100)), source: 'file' } });
		if (p === '/api/log/export') return wait({ status: 200, body: logs.join('\n') + '\n', filename: 'n5-fangov-mock-20260915-120000.log' });
		if (p === '/api/config/export') return wait({ status: 200, body: { format: 1, version: '0.2.0-mock', exported: Math.floor(t0), config: raw().replace(/password_hash = "[^"]+"/, 'password_hash = "<unchanged>"'), presets: Object.fromEntries(Object.entries(presets).map(([k, v]) => [k, v.map(tomlChannel).join('\n')])) }, filename: 'n5-fangov-settings-20260915-120000.json' });
		if (p === '/api/config/import') { let j; try { j = JSON.parse(opt.body); } catch (e) { j = null; }
			if (!j || j.format !== 1) return fail('import rejected: bundle format missing\nexpected "format": 1', 400);
			return wait({ status: /restart/.test(opt.body) ? 202 : 200, body: { ok: true } }); }
		if (p === '/api/profiles') return wait({ status: 200, body: [
			{ name: 'n5pro', title: 'Minisforum N5 Pro (IT5571 EC)', verified: true, notes: 'EC does not resume HDD regulation after a write; stop = fixed duty.' },
			{ name: 'nct67xx', title: 'Nuvoton NCT67xx (SmartFan IV)', verified: false, notes: 'Auto = pwmN_enable 5; original restored on stop.' },
			{ name: 'it87xx', title: 'ITE IT86xx/IT87xx', verified: false, notes: 'Original pwmN_enable restored on stop.' },
			{ name: 'monitor', title: 'Monitoring only (no PWM)', verified: false, notes: 'Sensors only, never writes.' }] });
		if (p === '/api/version') return wait({ status: 200, body: { version: '0.2.0-mock', tls: T.mode !== 'off' } });
		if (p.startsWith('/api/tls')) return mockTLS(p, opt);
		return fail('mock: not found ' + p, 404);
	};
})();

// chart engine (line + area, ticks, last-value label, hover)
const charts = [];
function chart(wrap, series, opt) {
	// series: [{name, color, data: [[ts, value]...]}], opt: {fmt, yMin, yMax, minSpan}
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
		// grid + y ticks
		ctx.font = '11px system-ui'; ctx.textBaseline = 'middle'; ctx.textAlign = 'right';
		const step = niceStep((yMax - yMin) / 4);
		for (let v = Math.ceil(yMin / step) * step; v <= yMax + 1e-9; v += step) {
			const y = Math.round(Y(v)) + .5; ctx.strokeStyle = line; seg(ctx, pad.l, y, W - pad.r, y);
			ctx.fillStyle = fg3; ctx.fillText(opt.fmt(v, true), pad.l - 6, y);
		}
		// x ticks every 30 min
		ctx.textAlign = 'center'; ctx.textBaseline = 'top';
		for (let t = Math.ceil(x0 / 1800) * 1800; t <= x1; t += 1800) {
			const x = Math.round(X(t)) + .5; ctx.strokeStyle = line; seg(ctx, x, pad.t, x, pad.t + ph);
			ctx.fillStyle = fg3; ctx.fillText(hm(t), x, pad.t + ph + 6);
		}
		// series
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
		// last-value labels, spread to avoid overlap
		labels.sort((a, b) => a.y - b.y); for (let i = 1; i < labels.length; i++) if (labels[i].y - labels[i - 1].y < 13) labels[i].y = labels[i - 1].y + 13;
		ctx.textAlign = 'left'; ctx.textBaseline = 'middle'; ctx.font = '600 11px system-ui';
		for (const l of labels) { ctx.fillStyle = l.col; ctx.fillText(l.txt, W - pad.r + 6, l.y); }
		// hover crosshair
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
const redrawAll = () => { for (const w of charts) w._st && w._st.draw(); for (const e of Object.values(ED)) e.draw(); };

// state
let snap = null, cfg = null, cfgRaw = '', profiles = [], sensors = [], version = '', tls = null;
const LOOPBACK = /^(localhost|127\.\d+\.\d+\.\d+|\[::1\])$/i.test(location.hostname);
let cert = null;
const dLeft = iso => Math.ceil((new Date(iso) - Date.now()) / 86400e3);
const secState = () => { const e = $('#h-sec'), on = tls === null ? location.protocol === 'https:' : !!tls, i = cert && cert.info;
	e.textContent = on ? '🔒 TLS' : '🔓 HTTP'; e.className = 'meta sec ' + (on ? 'ok' : LOOPBACK ? '' : 'warn');
	e.title = (on ? 'TLS-encrypted connection' : LOOPBACK ? 'plain HTTP on loopback' : 'plain HTTP on a non-loopback address — credentials and settings travel unencrypted')
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

// header
function renderHeader() {
	if (!snap) return;
	const pr = profiles.find(p => p.name === snap.profile);
	$('#h-profile').textContent = pr ? pr.title : snap.profile || '—';
	const b = $('#h-verified'); b.hidden = false;
	b.className = 'badge ' + (snap.verified ? 'ok' : 'warn'); b.textContent = snap.verified ? 'verified on hardware' : 'from documentation · untested';
	const st = snap.dry_run ? 'dry-run' : snap.status || 'unknown';
	const c = $('#h-status'); c.className = 'chip ' + st; c.textContent = st;
	$('#h-uptime').textContent = 'up ' + fmtUp(snap.uptime_s || 0);
	if (version) $('#h-version').textContent = 'v' + version.replace(/^v/, '');
}

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
		k.bar.style.width = pct(c.duty) + '%'; k.bar.className = c.mode === 'critical' || c.mode === 'failsafe' ? 'crit' : c.mode === 'stall' ? 'stall' : c.mode === 'auto' ? 'auto' : '';
		const slewing = c.target !== undefined && c.target !== c.duty && c.mode !== 'stall';
		k.tgt.hidden = !slewing; k.tgt.style.left = `calc(${pct(c.target)}% - 1px)`;
		clear(k.duty).append(`${pct(c.duty)} % `, h('span', { class: 'tg' }, `(${c.duty}${slewing ? ' → ' + c.target : ''})`));
		k.rpm.textContent = c.rpm < 0 ? 'no tach' : c.rpm.toLocaleString('en') + ' rpm';
	});
	// extra sensors
	const ex = clear($('#extra')); const et = Object.entries(snap.extra_temps || {});
	if (!et.length) ex.append(h('span', { class: 'empty' }, 'none')); for (const [n, v] of et) ex.append(h('span', null, n, h('b', null, fmtT(v) + ' ' + unit())));
	// hardware
	const pr = profiles.find(p => p.name === snap.profile) || {};
	const hw = clear($('#hw'));
	for (const [k, v] of [['profile', `${snap.profile || '?'}${pr.title ? ' — ' + pr.title : ''}`], ['hwmon', snap.hwmon_path || '—'], ['verified', snap.verified ? 'yes, on real hardware' : 'no — from documentation'],
		['notes', pr.notes || '—']]) hw.append(h('dt', null, k), h('dd', null, v));
	// alerts
	const al = clear($('#alerts')); const alerts = Object.entries(snap.alerts || {}).sort((a, b) => b[1] - a[1]);
	if (!alerts.length) al.append(h('li', { class: 'empty' }, 'no alerts'));
	for (const [t, ts] of alerts) al.append(h('li', null, h('span', { class: 'k ' + t }, t), h('time', { datetime: new Date(ts * 1000).toISOString(), title: new Date(ts * 1000).toLocaleString() }, rel(ts))));
}
function renderCharts() {
	if (!snap) return;
	const names = snap.channels.map(c => c.name);
	const mk = (key, f) => names.map((n, i) => ({ name: n, color: seriesColor(i), data: hist.filter(p => p[key] && p[key][n] > -900).map(p => [p.ts, f ? f(p[key][n]) : p[key][n]]) }));
	const legend = (el, ss) => { clear(el); for (const s of ss) { const i = h('i'); i.style.background = `var(${s.color})`; el.append(h('span', null, i, s.name)); } };
	const ts = mk('temp', tC); legend($('#lg-temp'), ts);
	chart($('#ch-temp'), ts, { fmt: (v, ax) => v.toFixed(ax ? 0 : 1) + (ax ? '' : ' ' + unit()), minSpan: 15 });
	const fs = mk(fanMetric); legend($('#lg-fan'), fs);
	$('#ch-fan-title').textContent = (fanMetric === 'rpm' ? 'Fan speed' : 'Duty') + ' · last 2 h';
	chart($('#ch-fan'), fs, fanMetric === 'rpm' ? { fmt: (v, ax) => ax ? String(Math.round(v)) : Math.round(v) + ' rpm', yMin: 0, minSpan: 1000 } : { fmt: (v, ax) => ax ? String(Math.round(v)) : `${Math.round(v)} (${pct(v)} %)`, yMin: 0, yMax: 255 });
}
$$('.seg button').forEach(b => b.addEventListener('click', () => { fanMetric = b.dataset.metric; $$('.seg button').forEach(x => { x.classList.toggle('on', x === b); x.setAttribute('aria-pressed', x === b); }); renderCharts(); }));

// curves
const ED = {}; let edState = null, cvSensors = [];
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
				h('td', null, h('button', { type: 'button', class: 'btn sm', disabled: c.curve.length <= 2, onclick: () => { c.curve.splice(j, 1); fillTable(); ed.draw(); } }, 'remove')))));
			addBtn.disabled = c.curve.length >= 8;
		};
		const addBtn = h('button', { type: 'button', class: 'btn sm', onclick: () => { const l = c.curve[c.curve.length - 1]; c.curve.push([l[0] + 5, Math.min(255, l[1] + 20)]); fillTable(); ed.draw(); } }, '+ add point');
		host.append(h('div', { class: 'card ed' },
			h('div', { class: 'top' }, chanHead(c),
				h('label', null, 'sensor', sel), h('label', null, 'critical °C', crit), h('label', null, 'stop', stop)),
			h('div', { class: 'cvs' }, cv),
			h('div', { class: 'pts' }, h('table', null, h('thead', null, h('tr', null, h('th', null, '°C'), h('th', null, 'duty'), h('th'))), tbody), addBtn),
			ref));
		fillTable();
		ed.draw = () => drawCurve(ed);
		bindCurveDrag(ed);
		new ResizeObserver(ed.draw).observe(cv.parentNode);
	});
	fillSensorSelects();
}
function fillSensorSelects() {
	for (const k in ED) { const { c, sel } = ED[k]; const ids = new Set(cvSensors.map(s => s.id || s)); ids.add(c.sensor); clear(sel);
		for (const id of ids) { const s = cvSensors.find(x => (x.id || x) === id); sel.append(h('option', { value: id, selected: id === c.sensor }, id + (s && s.temp !== undefined ? ` (${fmtT(s.temp)} ${unit()})` : ''))); } }
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
	cv.addEventListener('pointerdown', ev => { const g = curveGeom(ed), [x, y] = pos(ev); let best = 14, bi = -1;
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
$('#cv-apply').addEventListener('click', async () => {
	const n = $('#cv-notice'), errs = validateCurves();
	if (errs.length) { n.hidden = false; n.className = 'notice err'; n.textContent = errs.join('\n'); return; }
	const body = stripChannels(cfgRaw) + edState.map(tomlChannel).join('\n');
	try { const r = await api('/api/config', { method: 'PUT', body, headers: { 'Content-Type': 'application/toml' } });
		const warn = r.body && Array.isArray(r.body.warnings) && r.body.warnings.length ? 'Daemon replaced invalid values by defaults:\n' + r.body.warnings.join('\n') : '';
		n.hidden = r.status !== 202 && !warn; n.className = 'notice'; n.textContent = (r.status === 202 ? 'Saved — restart required (channel set or profile changed): systemctl restart n5-fangov\n' : '') + warn;
		toast(r.status === 202 ? 'Config written, restart required' : warn ? 'Applied with warnings' : 'Curves applied', warn ? 'warn' : 'ok'); await loadConfig(); loadEditor();
	} catch (e) { n.hidden = false; n.className = 'notice err'; n.textContent = e.message; toast('Rejected', 'err'); }
});
$('#cv-revert').addEventListener('click', () => { loadEditor(); toast('Reverted', ''); });

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

// presets
const chanSummary = chs => Array.isArray(chs) ? chs.map(c => typeof c === 'string' ? c : `${c.name}: ${(c.curve || []).map(p => p.join('/')).join(' ')}${c.critical ? ' crit ' + c.critical : ''}`).join(' · ') : String(chs || '');
async function loadPresets() {
	const host = $('#presets'); try {
		const r = await api('/api/presets'); const list = Array.isArray(r.body) ? r.body : r.body.presets || []; clear(host);
		if (!list.length) host.append(h('p', { class: 'empty' }, 'No presets yet — save the current curves to create one.'));
		for (const p of list) host.append(h('div', { class: 'card ps' }, h('span', { class: 'name' }, p.name), h('span', { class: 'sum' }, chanSummary(p.channels)),
			h('button', { class: 'btn', onclick: async () => { if (!confirm(`Apply preset “${p.name}”? Curves change immediately.`)) return;
				const r = await act(() => api(`/api/presets/${encodeURIComponent(p.name)}/apply`, { method: 'POST' }), `Preset ${p.name} applied`); if (!r) return;
				const n = $('#ps-notice'); n.hidden = r.status !== 202; n.textContent = 'Preset written — restart required: systemctl restart n5-fangov'; await loadConfig(); loadEditor(); } }, 'Apply')));
	} catch (e) { clear(host).append(h('p', { class: 'empty' }, 'presets: ' + e.message)); }
}
$('#ps-save').addEventListener('submit', async ev => { ev.preventDefault(); const n = $('#ps-name').value.trim();
	if (await act(() => api('/api/presets/' + encodeURIComponent(n), { method: 'PUT' }), `Saved current curves as “${n}”`)) { $('#ps-name').value = ''; loadPresets(); } });

// log
let logLines = [];
const logLine = l => { if (typeof l === 'string') return l; const ts = l.ts || (l.__REALTIME_TIMESTAMP && +l.__REALTIME_TIMESTAMP / 1e6);
	return (ts ? new Date(ts * 1000).toISOString().slice(0, 19) + ' ' : '') + (l.level ? l.level + ' ' : '') + (l.msg || l.message || l.MESSAGE || JSON.stringify(l)); };
async function loadLog() {
	try { const r = await api('/api/log?lines=200'); const b = r.body;
		logLines = (Array.isArray(b) ? b : b && b.lines ? b.lines : String(b || '').split('\n').filter(Boolean)).map(logLine); renderLog();
		const src = b && b.source || ''; $('#lg-src').textContent = src ? 'source: ' + src : ''; $('#lg-clear').disabled = src === 'journal';
		$('#lg-clear').title = src === 'journal' ? 'Log file disabled — the journal cannot be cleared from here' : 'Truncate the current log file (rotated files and journal untouched)';
	} catch (e) { $('#log').textContent = 'log: ' + e.message; }
}
$('#lg-export').addEventListener('click', () => act(() => download('/api/log/export', 'n5-fangov.log')));
$('#lg-clear').addEventListener('click', async () => {
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
$('#lg-filter').addEventListener('input', renderLog); $('#lg-refresh').addEventListener('click', loadLog);

// compatibility
function renderProfiles() {
	const tb = clear($('#profiles tbody'));
	for (const p of profiles) { const act = snap && snap.profile === p.name;
		tb.append(h('tr', { class: act ? 'active' : '' }, h('td', { class: 'mono' }, p.name, act ? h('span', { class: 'act' }, 'ACTIVE') : null), h('td', null, p.title || ''),
			h('td', null, h('span', { class: 'badge ' + (p.verified ? 'ok' : 'warn') }, p.verified ? 'verified on hardware' : 'from documentation · untested')), h('td', null, p.notes || ''))); }
}

// loading & polling
async function loadConfig() {
	try { const r = await api('/api/config'); const b = r.body;
		cfgRaw = b.raw || ''; cfg = b.config || { channel: [] };
	} catch (e) { toast('config: ' + e.message, 'err'); }
}
async function loadHistory() {
	try { const r = await api('/api/history?minutes=120' + (lastTs ? '&since=' + lastTs : ''));
		const pts = (Array.isArray(r.body) ? r.body : r.body.points || []).filter(p => p.ts > lastTs).sort((a, b) => a.ts - b.ts);
		if (pts.length) { hist = hist.concat(pts); lastTs = hist[hist.length - 1].ts; }
		const cut = Date.now() / 1000 - 7200; while (hist.length && (hist[0].ts < cut || hist.length > 720)) hist.shift();
		renderCharts();
	} catch (e) {}
}
let timer = null, histTimer = null, polling = false;
async function poll() {
	if (polling) return; polling = true;
	try { const r = await api('/api/state'); snap = r.body; renderHeader(); renderCards(); renderManual();
		if (curTab === 'curves') for (const k in ED) ED[k].draw(); }
	catch (e) {}
	polling = false;
}
const liveState = () => { const l = $('#h-live'); const paused = document.hidden; l.classList.toggle('paused', paused); l.lastChild.textContent = paused ? 'paused' : 'live'; };
function schedule() {
	clearInterval(timer); clearInterval(histTimer); liveState();
	if (document.hidden) return;
	poll(); loadHistory();
	timer = setInterval(poll, S.interval * 1000); histTimer = setInterval(loadHistory, 30000);
}
document.addEventListener('visibilitychange', schedule);

// tabs
let curTab = 'overview';
const TABS = $$('#tabs [role=tab]');
function selectTab(id, focus) {
	curTab = id;
	TABS.forEach(t => { const on = t.id === 'tab-' + id; t.setAttribute('aria-selected', on); t.tabIndex = on ? 0 : -1; $('#' + t.getAttribute('aria-controls')).hidden = !on; if (on && focus) t.focus(); });
	({ overview: renderCharts, presets: loadPresets, log: loadLog, compat: renderProfiles, curves: () => { if (!edState) loadEditor();
		api('/api/sensors').then(r => { cvSensors = Array.isArray(r.body) ? r.body : r.body.sensors || []; fillSensorSelects(); }).catch(() => {}); for (const k in ED) ED[k].draw(); } })[id]();
}
TABS.forEach((t, i) => { t.addEventListener('click', () => selectTab(t.id.slice(4)));
	t.addEventListener('keydown', ev => { const d = { ArrowRight: 1, ArrowLeft: -1, Home: -i, End: TABS.length - 1 - i }[ev.key]; if (d === undefined) return; ev.preventDefault(); selectTab(TABS[(i + d + TABS.length) % TABS.length].id.slice(4), true); }); });

// settings UI
const sb = $('#h-settings'), sp = $('#settings'), showS = on => { sp.hidden = !on; sb.setAttribute('aria-expanded', on); };
sb.addEventListener('click', () => showS(sp.hidden));
document.addEventListener('click', ev => { if (!sp.hidden && !sp.contains(ev.target) && ev.target !== sb) showS(false); });
document.addEventListener('keydown', ev => { if (ev.key === 'Escape' && !sp.hidden) { showS(false); sb.focus(); } });
$('#s-unit').value = S.unit; $('#s-interval').value = String(S.interval); $('#s-theme').value = S.theme;
$('#s-unit').addEventListener('change', ev => { S.unit = ev.target.value; saveS(); if (snap) { renderCards(); renderCharts(); fillSensorSelects(); redrawAll(); } });
$('#s-interval').addEventListener('change', ev => { S.interval = +ev.target.value; saveS(); schedule(); });
$('#s-theme').addEventListener('change', ev => { S.theme = ev.target.value; saveS(); document.documentElement.dataset.theme = S.theme; redrawAll(); });
matchMedia('(prefers-color-scheme: light)').addEventListener('change', () => { if (S.theme === 'system') redrawAll(); });
document.documentElement.dataset.theme = S.theme;
// settings bundle: export = download, import = file → POST (config + presets replaced after server-side validation)
const gNotice = msg => { const n = $('#g-notice'); n.hidden = !msg; n.textContent = msg || ''; };
$('#s-export').addEventListener('click', () => { showS(false); act(() => download('/api/config/export', 'n5-fangov-settings.json')); });
$('#s-import').addEventListener('click', () => $('#s-file').click());
$('#s-file').addEventListener('change', async () => {
	const inp = $('#s-file'), f = inp.files[0]; inp.value = ''; if (!f) return;
	if (f.size > 1 << 20) return toast('Settings file exceeds 1 MiB', 'err');
	const text = await f.text(); let j = null; try { j = JSON.parse(text); } catch (e) {}
	if (!j || typeof j !== 'object' || Array.isArray(j)) return toast(`${f.name}: not a JSON settings bundle`, 'err');
	if (!confirm(`Import settings from “${f.name}”?\nConfig and presets on the daemon are replaced (after validation).`)) return;
	showS(false);
	try { const r = await api('/api/config/import', { method: 'POST', body: text, headers: { 'Content-Type': 'application/json' } });
		if (r.status === 202) { gNotice('Settings imported — restart required: systemctl restart n5-fangov'); toast('Imported, restart required', 'warn', 12000); }
		else { toast('Settings imported', 'ok'); }
		await loadConfig(); edState = null; if (curTab === 'curves') loadEditor(); if (curTab === 'presets') loadPresets(); poll();
	} catch (e) { toast(e.message, 'err', 15000); }
});

// certificate panel (GET /api/tls is public, POSTs need CSRF + auth)
const dlg = $('#cert'), ctNotice = (msg, kind) => { const n = $('#ct-notice'); n.hidden = !msg; n.className = 'notice ' + (kind || ''); n.textContent = msg || ''; };
const ctForm = id => { for (const f of ['#ct-regen-f', '#ct-upload-f']) $(f).hidden = f !== id || !$(f).hidden; };
async function loadCert() { try { cert = (await api('/api/tls')).body; } catch (e) { cert = null; if (e.status !== 501) toast('certificate: ' + e.message, 'err'); } secState(); }
// mode "auto (fallback from file)" + fallback:true: the configured file pair
// could not be loaded, the automatic certificate stands in (M3).
function renderCert() {
	const c = cert || { mode: 'off' }, i = c.info, b = $('#ct-mode'), fb = !!c.fallback, isFile = c.mode === 'file', isAuto = c.mode === 'auto' || fb;
	b.textContent = fb ? 'automatic (fallback)' : isFile ? 'own certificate' : isAuto ? 'automatic' : 'TLS off'; b.className = 'badge ' + (fb ? 'warn' : isAuto ? 'ok' : c.mode);
	$('#ct-off').hidden = !!i; $('#ct-body').hidden = !i; $('#ct-reset').hidden = !isFile && !fb; $('#ct-regen').hidden = isFile;
	if (!i) return;
	const kv = clear($('#ct-kv')), left = dLeft(i.not_after), until = h('dd', { class: left < 0 ? 'expired' : left < 30 ? 'soon' : '' }, i.not_after.slice(0, 10) + (left < 0 ? ' — expired' : left < 30 ? ` — in ${left} days` : ''));
	for (const [k, v] of [['subject', i.subject], ['issuer', i.issuer], ['valid from', i.not_before.slice(0, 10)], ['valid until', until], ['key', i.key_algo + (i.is_ca ? ' · CA flag (trust anchor)' : '')], ['serial', h('dd', { class: 'mono' }, i.serial_hex)]])
		kv.append(h('dt', null, k), v.nodeType ? v : h('dd', null, v));
	const san = clear($('#ct-san')); for (const n of i.dns_names) san.append(h('span', null, n)); for (const n of i.ips) san.append(h('span', { class: 'ip' }, n));
	if (!i.dns_names.length && !i.ips.length) san.append(h('span', { class: 'empty' }, 'no SANs'));
	$('#ct-fp').textContent = i.fingerprint_sha256;
	// warnings come from the server (same rules as the upload validation)
	const w = (c.warnings || []).map(x => /^SAN list lacks host/.test(x) ? x + ' — under HSTS the browser will refuse that name' : x);
	if (fb) w.unshift('The configured certificate files could not be loaded — the automatic certificate is served instead (see the daemon log). Upload the pair again or go back to auto.');
	if (w.length) ctNotice(w.join('\n'), 'warn');
}
const ctClearUpload = () => { for (const id of ['#ct-cpem', '#ct-kpem', '#ct-cfile', '#ct-kfile']) $(id).value = ''; $('#ct-force').checked = false; $('#ct-force-l').hidden = true; };
async function openCert() { showS(false); ctNotice(''); $('#ct-regen-f').hidden = $('#ct-upload-f').hidden = true; ctClearUpload(); await loadCert(); renderCert(); if (!dlg.open) dlg.showModal(); }
$('#h-sec').addEventListener('click', openCert); $('#s-cert').addEventListener('click', openCert);
$('#ct-close').addEventListener('click', () => dlg.close());
dlg.addEventListener('click', ev => { if (ev.target === dlg) dlg.close(); });
dlg.addEventListener('close', ctClearUpload);
$$('#cert [data-cancel]').forEach(b => b.addEventListener('click', () => { b.closest('form').hidden = true; ctClearUpload(); }));
$('#ct-crt').addEventListener('click', () => act(() => download('/api/tls/cert.crt', 'n5-fangov.crt')));
$('#ct-cer').addEventListener('click', () => act(() => download('/api/tls/cert.cer', 'n5-fangov.cer')));
$('#ct-copy').addEventListener('click', () => navigator.clipboard.writeText($('#ct-fp').textContent).then(() => toast('Fingerprint copied', 'ok'), () => toast('Clipboard blocked — select the text', 'warn')));
$('#ct-regen').addEventListener('click', () => { $('#ct-newkey').checked = false; ctForm('#ct-regen-f'); });
$('#ct-upload').addEventListener('click', () => ctForm('#ct-upload-f'));
// after a change: re-read /api/tls (mode, server-side warnings, fallback) and
// the config — the manager rewrote [web] tls/cert_file/key_file, and a stale
// cfgRaw in the curve editor would otherwise carry the old values (M2).
const ctDone = async (r, msg) => { const w = r.body.warning ? [r.body.warning] : r.body.warnings || [];
	toast(msg + (w.length ? ' (warnings)' : ''), w.length ? 'warn' : 'ok', 8000);
	await loadCert(); renderCert(); const have = new Set((cert && cert.warnings) || []), extra = w.filter(x => !have.has(x));
	if (extra.length) { const n = $('#ct-notice'); ctNotice((n.hidden ? '' : n.textContent + '\n') + extra.join('\n'), 'warn'); } loadConfig(); };
$('#ct-regen-f').addEventListener('submit', async ev => { ev.preventDefault(); const nk = $('#ct-newkey').checked;
	if (nk && !confirm('Generate a new private key?\nEvery browser and OS store that trusts the current certificate must import the new one.')) return;
	const r = await act(() => api('/api/tls/regenerate', { method: 'POST', json: { keep_key: !nk } })); if (!r) return; $('#ct-regen-f').hidden = true; ctDone(r, 'Certificate regenerated'); });
for (const [f, ta] of [['#ct-cfile', '#ct-cpem'], ['#ct-kfile', '#ct-kpem']]) $(f).addEventListener('change', async ev => { const x = ev.target.files[0]; if (!x) return;
	if (x.size > 65536) return toast(x.name + ': larger than 64 KiB', 'err'); $(ta).value = await x.text(); });
// M4: a 400 with force_required means the leaf does not cover the name this
// session uses; the server refuses until the operator ticks "force".
$('#ct-upload-f').addEventListener('submit', async ev => { ev.preventDefault(); const c = $('#ct-cpem').value.trim(), k = $('#ct-kpem').value.trim();
	if (!/BEGIN CERTIFICATE/.test(c) || !/PRIVATE KEY/.test(k)) return ctNotice('Need a PEM CERTIFICATE block and a PRIVATE KEY block.', 'err');
	let r; try { r = await api('/api/tls/upload', { method: 'POST', json: { cert: c, key: k, force: $('#ct-force').checked } }); }
	catch (e) { if (e.body && e.body.force_required) { $('#ct-force-l').hidden = false; $('#ct-force-h').textContent = e.body.host; ctNotice(e.message, 'err'); toast('Certificate does not cover ' + e.body.host, 'err'); } else toast(e.message, 'err'); return; }
	$('#ct-upload-f').hidden = true; ctClearUpload(); ctDone(r, 'Own certificate installed'); });
$('#ct-reset').addEventListener('click', async () => { if (!confirm('Back to the automatic certificate? The uploaded pair is deleted.')) return;
	const r = await act(() => api('/api/tls/reset', { method: 'POST' })); if (r) ctDone(r, 'Automatic certificate active'); });

// boot
(async () => {
	if (MOCK) toast('Mock mode', 'warn', 8000);
	api('/api/profiles').then(r => { profiles = Array.isArray(r.body) ? r.body : r.body.profiles || []; renderHeader(); if (snap) renderCards(); }).catch(() => {});
	secState(); loadCert();
	api('/api/version').then(r => { version = typeof r.body === 'string' ? r.body.trim() : r.body.version || ''; if (typeof r.body.tls === 'boolean') tls = r.body.tls; secState(); renderHeader(); }).catch(() => {});
	await loadConfig();
	if (document.hidden) { poll(); loadHistory(); }
	schedule();
})();
})();
