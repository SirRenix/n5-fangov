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
// icons: the sprite at the top of index.html; icons next to text are aria-hidden, icon-only controls carry aria-label
const NS = 'http://www.w3.org/2000/svg';
const ico = name => { const s = document.createElementNS(NS, 'svg'); s.setAttribute('class', 'ic'); s.setAttribute('aria-hidden', 'true'); const u = document.createElementNS(NS, 'use'); u.setAttribute('href', '#i-' + name); s.append(u); return s; };
// UI constants: bp mirror app.css, timings ms, geometry px; limits are defaults until GET /api/version merges into LIM
const UI = Object.freeze({
	bp: { xs: 480, sm: 700, md: 900, lg: 1100 },
	timing: { toast: 5000, toastErr: 12000, toastLong: 15000, toastNotice: 8000, alerts: 60000, system: 30000, log: 10000, schedules: 60000, fans: 30000, blobRevoke: 30000, subLock: 1000 },
	toastMax: 3,
	chart: { yMargin: .15, yRound: 5, minSpanTemp: 15, minSpanRpm: 1000, minSpan: 10, pad: { l: 40, r: 58, t: 8, b: 22 },
		lineW: 2, fillAlpha: .08, dotR: 4.5, dotStroke: 2, labelH: 13, labelGap: 6, tipGap: 12, dash: { hover: [3, 3], crit: [4, 3], now: [2, 3] }, ceilAlpha: .55,
		sparkPad: 2, sparkW: 1.5, sparkFill: .12, sparkDot: 2.5 },
	curve: { pad: { l: 34, r: 12, t: 10, b: 22 }, xMin: 100, xPad: 10, xStep: 20, yStep: 51, hitR: 14, hitRTouch: 22, addStep: 5, addDefault: 40, fillAlpha: .1, labelGap: 8, keyT: 1, keyD: 5, keyShift: 5 },
	limits: { min_hdd_override: 60, critical_min: 30, critical_max: 150, curve_points_min: 2, curve_points_max: 8, dashboard_sensors_max: 8, password_min: 8, password_max: 128, hysteresis_max: 10, min_on_max_s: 3600,
		temp: [-20, 120], duty: 255, name: /^[a-z0-9_-]{1,64}$/, user: /^[A-Za-z0-9_.-]{1,32}$/, token: /^[A-Za-z0-9][A-Za-z0-9 ._-]{0,31}$/, importBytes: 1 << 20, pemBytes: 65536, logLines: 200 },
	thresh: { warm: .8, hot: .92, certSoonDays: 30 },
	font: (size, weight) => `${weight ? weight + ' ' : ''}${cssVar(size)} ${cssVar('--font-sans')}`,
});
const LIM = Object.assign({}, UI.limits);
const TIP = { duty: 'duty = PWM value 0..255 written to the channel (100 % = 255)', stop: 'stop = duty left on the channel when the daemon stops; auto = driver/EC takes over',
	critical: 'critical = temperature that forces 100 % at once', sensor: 'temperature source of this curve', slew: 'current → target: moved in step_up/step_down steps per cycle',
	ceiling: 'ceiling = built-in floor below critical (HDD 65, SSD 85, CPU 100 °C, or lower if configured): 255 at once, whatever critical says',
	hyst: 'hysteresis = the curve follows the reading only when it moved by at least this many °C (0 = off)', minOn: 'min_on = a rise of the curve target is held at least this long (off = no hold)',
	held: 'the hysteresis-held temperature the curve is evaluated at', hold: 'min_on: the curve target is held for the remaining time' };
const Q = new URLSearchParams(location.search), MOCK = Q.get('mock') === '1';
const REF = { // N5 Pro duty→RPM (measured)
	cpu: [[85, 2000], [140, 3120], [179, 3830], [217, 4445], [255, 5073]],
	ssd: [[74, 2130], [140, 3280], [179, 3790], [217, 4230], [255, 4687]],
	hdd: [[87, 1237], [105, 1650], [140, 2250], [179, 2725], [217, 3160], [255, 3540]] };

// history ranges: minutes for the API, window/grid/label format for the charts, polling (2 h: since every 30 s; 24 h / 7 d: full reload every 60 s)
const hm = ts => new Date(ts * 1000).toTimeString().slice(0, 5);
const ddmm = ts => { const d = new Date(ts * 1000); return `${String(d.getDate()).padStart(2, '0')}.${String(d.getMonth() + 1).padStart(2, '0')} ${hm(ts)}`; };
// maxPoints 0 = raw tier: the cap follows the daemon's interval once the config is known (maxPts), otherwise only the window trims
const RANGES = { '2h': { label: '2 h', minutes: 120, windowS: 7200, gridS: 1800, maxPoints: 0, poll: 30000, since: true, fmt: hm },
	'24h': { label: '24 h', minutes: 1440, windowS: 86400, gridS: 4 * 3600, maxPoints: 1500, poll: 60000, fmt: ts => new Date(ts * 1000).toLocaleDateString('en', { weekday: 'short' }) + ' ' + hm(ts) },
	'7d': { label: '7 d', minutes: 10080, windowS: 7 * 86400, gridS: 86400, maxPoints: 2100, poll: 60000, fmt: ddmm } };
// settings (whitelisted values; anything else falls back to the default); nav = sidebar expanded or icon rail; sensors = Overview sensor groups the user opened / closed
const S = { unit: 'C', interval: 5, theme: 'system', range: '2h', nav: 'side', sensors: {} }, S_OK = { unit: ['C', 'F'], interval: [5, 10, 30], theme: ['system', 'dark', 'light'], range: Object.keys(RANGES), nav: ['side', 'rail'] };
try { const j = JSON.parse(localStorage.getItem('n5-fangov') || '{}'); for (const k in S_OK) if (S_OK[k].includes(j[k])) S[k] = j[k]; for (const g in j.sensors || {}) if (typeof j.sensors[g] === 'boolean') S.sensors[g] = j.sensors[g]; } catch (e) {}
const R = () => RANGES[S.range];
const saveS = () => { try { localStorage.setItem('n5-fangov', JSON.stringify(S)); } catch (e) {} };
const tC = v => S.unit === 'F' ? v * 9 / 5 + 32 : v;
const unit = () => S.unit === 'F' ? '°F' : '°C';
const fmtT = (v, d = 1) => !(v > -900) ? '—' : tC(v).toFixed(d);
const pct = d => Math.round(d / 255 * 100);
// "6 d 3 h" / "2 h 5 min" / "4 min", zero parts dropped
const elapsed = s => { const d = s / 86400 | 0, hh = s % 86400 / 3600 | 0, m = s % 3600 / 60 | 0;
	return d ? `${d} d` + (hh ? ` ${hh} h` : '') : hh ? `${hh} h` + (m ? ` ${m} min` : '') : `${m} min`; };
const rel = ts => { const s = Math.max(0, Date.now() / 1000 - ts | 0); return (s < 60 ? s + ' s' : elapsed(s)) + ' ago'; };
const fmtUp = s => { const d = s / 86400 | 0, hh = s % 86400 / 3600 | 0, m = s % 3600 / 60 | 0; return d ? `${d}d` + (hh ? ` ${hh}h` : '') : hh ? `${hh}h` + (m ? ` ${m}m` : '') : `${m}m`; };
// Go duration ("30m0s", "1h0m0s") → "30 min" / "1 h"
const fmtDur = s => { const m = /^(?:(\d+)h)?(?:(\d+)m)?(?:(\d+(?:\.\d+)?)s)?$/.exec(s || ''); if (!m || !m[0]) return s || '—';
	const t = (+m[1] || 0) * 3600 + (+m[2] || 0) * 60 + (+m[3] || 0); return t && t % 3600 === 0 ? t / 3600 + ' h' : t && t % 60 === 0 ? t / 60 + ' min' : t + ' s'; };
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

// toasts: errors go to the assertive live region
const toast = (msg, kind, ms) => {
	const t = h('div', { class: 'toast ' + (kind || '') }, msg,
		h('button', { 'aria-label': 'Dismiss', onclick: () => t.remove() }, ico('close')));
	const host = $(kind === 'err' ? '#toasts-a' : '#toasts'); while (host.children.length >= UI.toastMax) host.firstChild.remove(); host.append(t); // at most 3 per region, oldest dropped
	setTimeout(() => t.remove(), ms || (kind === 'err' ? UI.timing.toastErr : UI.timing.toast));
};
// confirm dialog (replaces window.confirm): ask(title, text, {ok, danger}) → Promise<boolean>
const cfd = $('#confirm'); let cfRes = null;
const ask = (title, text, opt) => new Promise(res => { opt = opt || {}; if (cfRes) cfRes(false); cfRes = res; // a second ask answers the open one with "no"
	$('#cf-title').textContent = title; $('#cf-text').textContent = text; const ok = $('#cf-ok');
	ok.textContent = opt.ok || 'Confirm'; ok.className = 'btn primary' + (opt.danger ? ' danger' : '');
	if (!cfd.open) cfd.showModal(); (opt.danger ? $('#cf-cancel') : ok).focus(); });
const cfEnd = v => { const r = cfRes; cfRes = null; if (cfd.open) cfd.close(); if (r) r(v); };
on('#cf-ok', 'click', () => cfEnd(true)); on('#cf-cancel', 'click', () => cfEnd(false)); on('#cf-close', 'click', () => cfEnd(false));
backdrop(cfd); cfd.addEventListener('close', () => cfEnd(false));
// Escape is answered here, not by the browser: a confirm opened without a user activation (e.g. from an Escape in the preset editor) would otherwise
// share one close watcher group with the dialog below it and one Escape would close both
cfd.addEventListener('keydown', ev => { if (ev.key === 'Escape') { ev.preventDefault(); cfEnd(false); } });

// API: cookie session; a 401 while signed in = session gone (unsaved curve and schedule edits are stashed and restored after the next sign-in)
const anon = mode => ({ authenticated: false, mode: mode || 'basic', user: '' });
let failures = 0, sess = anon();
const signedIn = () => !!sess.authenticated;
const sessionLost = () => { if (!signedIn()) return; sess = anon(sess.mode); if (edDirty) edStash = edState; if (scDirty) scStash = scState;
	toast('Session expired — sign in again' + (edDirty ? ' — curve edits kept' : '') + (scDirty ? ' — schedule edits kept' : ''), 'warn'); applyAuth(); };
// consecutive network / 5xx answers (mock &down=1: status 0); two show the banner
const tally = status => { failures = status > 0 && status < 500 ? 0 : failures + 1; connState(); };
const api = async (path, opt) => {
	opt = opt || {};
	if (MOCK) return window.n5mock(path, opt).then(r => { tally(r.status); return r; }, e => { if (e.status === 401) sessionLost(); tally(e.status); throw e; }); // mock.js, loaded at boot
	const headers = Object.assign({}, opt.headers || {});
	if (opt.method && opt.method !== 'GET') headers['X-N5-Fangov-Csrf'] = '1';
	if (opt.json !== undefined) { headers['Content-Type'] = 'application/json'; opt.body = JSON.stringify(opt.json); }
	let r;
	try { r = await fetch(path, { method: opt.method || 'GET', headers, body: opt.body, cache: 'no-store', credentials: 'same-origin' }); }
	catch (e) { tally(0); throw new Error('network: ' + e.message); }
	tally(r.status);
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
// downloads: fetch (cookie rides along) + blob anchor, CSP-safe; resolves with the file name
const saveBlob = (b, name) => { const u = URL.createObjectURL(b), a = h('a', { href: u, download: name }); document.body.append(a); a.click(); a.remove(); setTimeout(() => URL.revokeObjectURL(u), UI.timing.blobRevoke); return name; };
const download = async (path, fallback) => {
	if (MOCK) { const r = await api(path); return saveBlob(new Blob([typeof r.body === 'string' ? r.body : JSON.stringify(r.body)]), r.filename || fallback); }
	const r = await fetch(path, { cache: 'no-store', credentials: 'same-origin' });
	if (r.status === 401) { sessionLost(); throw new Error('sign in first'); }
	if (!r.ok) { const b = await r.json().catch(() => null); throw new Error(b && b.error || `HTTP ${r.status}`); }
	const m = /filename="?([^";]+)/.exec(r.headers.get('content-disposition') || '');
	return saveBlob(await r.blob(), m ? m[1] : fallback);
};

// chart engine
const charts = [], C = UI.chart;
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
		const pad = C.pad, pw = W - pad.l - pad.r, ph = H - pad.t - pad.b;
		const all = series.flatMap(s => s.data);
		if (!all.length) { ctx.fillStyle = fg3; ctx.font = UI.font('--fs-12'); ctx.fillText('no history — points arrive with every daemon cycle', pad.l, H / 2); return; }
		const now = Date.now() / 1000, rg = R(), x0 = Math.min(now - rg.windowS, all[0][0]), x1 = now;
		let yMin = opt.yMin, yMax = opt.yMax;
		if (yMin === undefined || yMax === undefined) {
			let lo = Infinity, hi = -Infinity; for (const [, v] of all) { if (v < lo) lo = v; if (v > hi) hi = v; }
			if (!isFinite(lo)) { lo = 0; hi = 1; }
			const span = Math.max(hi - lo, opt.minSpan || C.minSpan), m = span * C.yMargin, r = C.yRound;
			if (yMin === undefined) yMin = Math.max(Math.floor((lo - m) / r) * r, opt.floor === undefined ? -Infinity : opt.floor); if (yMax === undefined) yMax = Math.ceil((hi + m) / r) * r;
		}
		const X = t => pad.l + (t - x0) / (x1 - x0) * pw, Y = v => pad.t + (1 - (v - yMin) / (yMax - yMin)) * ph;
		st.X = X; st.pad = pad; st.pw = pw;
		ctx.font = UI.font('--fs-11'); ctx.textBaseline = 'middle'; ctx.textAlign = 'right';
		const step = niceStep((yMax - yMin) / 4);
		for (let v = Math.ceil(yMin / step) * step; v <= yMax + 1e-9; v += step) {
			const y = Math.round(Y(v)) + .5; ctx.strokeStyle = line; seg(ctx, pad.l, y, W - pad.r, y);
			ctx.fillStyle = fg3; ctx.fillText(opt.fmt(v, true), pad.l - C.labelGap, y);
		}
		ctx.textAlign = 'center'; ctx.textBaseline = 'top';
		// grid lines on local-time multiples of the range's step (midnight for 7 d, not UTC midnight)
		const g = rg.gridS, tz = new Date(x0 * 1000).getTimezoneOffset() * 60, minGap = ctx.measureText(rg.fmt(x0)).width + C.labelGap; let lastX = -Infinity;
		for (let t = Math.ceil((x0 - tz) / g) * g + tz; t <= x1; t += g) {
			const x = Math.round(X(t)) + .5; ctx.strokeStyle = line; seg(ctx, x, pad.t, x, pad.t + ph);
			if (x - lastX < minGap) continue; lastX = x; ctx.fillStyle = fg3; ctx.fillText(rg.fmt(t), x, pad.t + ph + C.labelGap);
		}
		const labels = [];
		for (const s of series) {
			if (!s.data.length) continue;
			const col = cssVar(s.color);
			ctx.beginPath(); s.data.forEach(([t, v], i) => { const x = X(t), y = Y(clamp(v, yMin, yMax)); i ? ctx.lineTo(x, y) : ctx.moveTo(x, y); });
			ctx.strokeStyle = col; ctx.lineWidth = C.lineW; ctx.lineJoin = 'round'; ctx.stroke();
			const last = s.data[s.data.length - 1];
			ctx.lineTo(X(last[0]), pad.t + ph); ctx.lineTo(X(s.data[0][0]), pad.t + ph); ctx.closePath();
			ctx.globalAlpha = C.fillAlpha; ctx.fillStyle = col; ctx.fill(); ctx.globalAlpha = 1;
			labels.push({ y: Y(clamp(last[1], yMin, yMax)), col, txt: opt.fmt(last[1]) });
		}
		labels.sort((a, b) => a.y - b.y); for (let i = 1; i < labels.length; i++) if (labels[i].y - labels[i - 1].y < C.labelH) labels[i].y = labels[i - 1].y + C.labelH;
		ctx.textAlign = 'left'; ctx.textBaseline = 'middle'; ctx.font = UI.font('--fs-11', cssVar('--fw-semibold'));
		for (const l of labels) { ctx.fillStyle = l.col; ctx.fillText(l.txt, W - pad.r + C.labelGap, l.y); }
		if (st.hover !== null) {
			const t = st.hover; const x = Math.round(X(t)) + .5; ctx.strokeStyle = fg2; seg(ctx, x, pad.t, x, pad.t + ph, C.dash.hover);
			for (const s of series) { const p = nearest(s.data, t); if (p) dot(ctx, X(p[0]), Y(clamp(p[1], yMin, yMax)), cssVar(s.color)); }
		}
	};
	st.draw = draw;
	if (!wrap._bound) {
		wrap._bound = true; charts.push(wrap);
		const move = ev => {
			const r = cv.getBoundingClientRect(), x = ev.clientX - r.left, s = wrap._st;
			if (!s.X || x < s.pad.l || x > s.pad.l + s.pw) return leave();
			const now = Date.now() / 1000, ws = R().windowS, x0 = now - ws; s.hover = x0 + (x - s.pad.l) / s.pw * ws; s.draw();
			clear(tip); tip.append(h('div', { class: 't' }, R().fmt(s.hover)));
			for (const sr of s.series) { const p = nearest(sr.data, s.hover); if (!p) continue;
				const key = h('i'); key.style.background = cssVar(sr.color);
				tip.append(h('div', null, h('span', null, key, sr.name), h('b', null, s.opt.fmt(p[1])))); }
			tip.hidden = false; const tw = tip.offsetWidth, g = C.tipGap; tip.style.left = (x + g + tw > r.width ? x - tw - g : x + g) + 'px'; tip.style.top = Math.min(ev.clientY - r.top + g, r.height - tip.offsetHeight - 4) + 'px';
		};
		const leave = () => { const s = wrap._st; if (s.hover === null) return; s.hover = null; tip.hidden = true; s.draw(); };
		cv.addEventListener('pointermove', move); cv.addEventListener('pointerleave', leave);
		new ResizeObserver(() => wrap._st.draw()).observe(wrap);
	}
	draw();
}
const seg = (ctx, x0, y0, x1, y1, dash) => { ctx.setLineDash(dash || []); ctx.beginPath(); ctx.moveTo(x0, y0); ctx.lineTo(x1, y1); ctx.stroke(); ctx.setLineDash([]); };
const niceStep = r => { const p = Math.pow(10, Math.floor(Math.log10(r || 1))), f = r / p; return (f < 1.5 ? 1 : f < 3.5 ? 2 : f < 7.5 ? 5 : 10) * p; };
const dot = (ctx, x, y, col) => { ctx.beginPath(); ctx.arc(x, y, C.dotR, 0, 7); ctx.fillStyle = col; ctx.fill(); ctx.strokeStyle = cssVar('--bg2'); ctx.lineWidth = C.dotStroke; ctx.stroke(); };
const nearest = (data, t) => { if (!data.length) return null; let lo = 0, hi = data.length - 1;
	while (hi - lo > 1) { const m = (lo + hi) >> 1; data[m][0] < t ? lo = m : hi = m; }
	return Math.abs(data[lo][0] - t) < Math.abs(data[hi][0] - t) ? data[lo] : data[hi]; };
const redrawAll = () => { for (const w of charts) w._st && w._st.draw(); drawSparks(); drawEds(); };

// state
let snap = null, cfg = null, cfgRaw = '', profiles = [], sensors = [], version = '', tls = null, dash = [], alerts = null, builtinNames = [];
const LOOPBACK = /^(localhost|127\.\d+\.\d+\.\d+|\[::1\])$/i.test(location.hostname);
let cert = null;
const dLeft = iso => Math.ceil((new Date(iso) - Date.now()) / 86400e3);
// certificate warning chip in the page header: shown only while the certificate is in fallback, expires within certSoonDays,
// is expired, or the listener is plain HTTP off loopback; click → Settings → Certificate
const secState = () => { const e = $('#h-cert'), on = tls === null ? location.protocol === 'https:' : !!tls, i = cert && cert.info, fb = !!(cert && cert.fallback), left = i ? dLeft(i.not_after) : null, soon = left !== null && left < UI.thresh.certSoonDays;
	const warn = on ? fb || soon : !LOOPBACK; e.hidden = !warn || !signedIn(); if (e.hidden) return;
	const txt = !on ? 'plain HTTP' : fb ? 'certificate fallback' : left < 0 ? 'certificate expired' : `certificate expires in ${left} d`;
	$('#h-cert-t').textContent = txt; e.setAttribute('aria-label', txt); // < 700 px: icon-only
	e.title = (on ? 'TLS connection' : 'plain HTTP off loopback — credentials travel unencrypted')
		+ (cert ? `\ncertificate: ${cert.mode}` + (i ? ` · expires ${i.not_after.slice(0, 10)}${soon ? ' (soon!)' : ''}` : '') : '') + '\nclick for the certificate settings'; };
let hist = [], lastTs = 0, fanMetric = 'rpm';
// the tile and live colours follow min(critical, ceiling): the daemon acts on whichever is lower (0.4.1)
const ceilOf = name => { const c = snap && snap.channels.find(x => x.name === name); return c && c.ceiling > 0 ? c.ceiling : null; };
const cfgCrit = name => { const c = cfg && chList().find(x => x.name === name); return c && +c.critical > 0 ? +c.critical : null; };
const critOf = name => { const k = cfgCrit(name), l = ceilOf(name); return k && l ? Math.min(k, l) : k; };
// tooltip of a temperature: the configured critical and the daemon's ceiling (with a hint when the ceiling is the lower one)
const limitTip = name => { const k = cfgCrit(name), l = ceilOf(name); if (!k && !l) return null;
	return (k ? `${TIP.critical}: ${fmtT(k, 0)} ${unit()}` : '') + (l ? `${k ? '\n' : ''}ceiling ${fmtT(l, 0)} ${unit()} (built-in floor below critical${k > l ? ' — acts first here' : ''})` : ''); };
const chList = () => (cfg && (cfg.channel || cfg.channels)) || [];
// colour by share of critical; without one (anonymous) neutral
const tempClass = (t, crit) => { if (t === null || t <= -900) return 'na'; if (!crit) return '';
	const r = t / crit; return r < UI.thresh.warm ? 't-ok' : r < UI.thresh.hot ? 't-warm' : r < 1 ? 't-hot' : 't-crit'; };
// the daemon's hddLike rule (control/controller.go): fixed stop duty or pwm3 on the N5 Pro → manual duty ≥ min_hdd_override
const profName = () => snap ? snap.profile : (profiles.find(p => p.active) || {}).name || '';
const stopN = s => s === undefined || s === null || /^\s*(auto)?\s*$/i.test(String(s)) ? 'auto' : String(s).trim();
const minDuty = c => stopN(c.stop) !== 'auto' || profName() === 'n5pro' && +c.pwm === 3 ? LIM.min_hdd_override : 0;
const seriesColor = i => SERIES[i % SERIES.length];
const modeBadge = (el, m, ceil) => { el.className = 'mode m-' + (m || 'unknown'); el.textContent = m || 'unknown'; el.title = m === 'stall' ? 'stall: the fan reports 0 rpm, the channel is raised until it spins' : m === 'critical' ? (ceil ? 'ceiling reached: ' + TIP.ceiling : TIP.critical) : ''; };
const chanHead = (c, ...pre) => h('span', null, ...pre, h('span', { class: 'name' }, c.name), h('span', { class: 'sub' }, `pwm${c.pwm} · ${c.sensor || '?'}`));
const act = async (fn, ok) => { try { const r = await fn(); if (ok) toast(typeof ok === 'function' ? ok(r) : ok, 'ok'); return r; } catch (e) { toast(e.message, 'err'); } };
const verTxt = v => v ? 'verified on hardware' : 'from documentation · untested';
const preBadge = (els, pre) => { for (const el of els) { el.hidden = !pre; el.textContent = (pre || '').split('.')[0]; } };
const errText = e => /lspci.*(not found|no such file)/i.test(e) ? 'lspci not installed — apt install pciutils for PCI device names (ids only until then)' : e;

// page header: title from PAGES, profile title, status chip, uptime, version + beta badge, certificate chip (secState), user
function renderHeader() {
	const p = PAGES.find(x => x.id === cur); if (p) { $('#ph-title').textContent = p.t; document.title = `${p.t} · n5-fangov`; }
	if (version) { for (const v of $$('.version')) v.textContent = 'v' + version.replace(/^v/, ''); const nv = $('#nav-ver'); if (nv) nv.textContent = 'v' + version.replace(/^v/, ''); }
	if (!snap) return;
	const pr = profiles.find(x => x.name === snap.profile), title = pr ? pr.title : snap.profile || '—', pf = $('#h-profile');
	pf.textContent = title; pf.title = title + (snap.verified === undefined ? '' : ' · ' + verTxt(snap.verified));
	const st = snap.dry_run ? 'dry-run' : snap.status || 'unknown';
	const c = $('#h-status'); c.className = 'chip ' + st; if (c.textContent !== st) c.textContent = st; // role=status: no mutation without change
	for (const u of $$('.uptime')) u.textContent = 'up ' + fmtUp(snap.uptime_s || 0);
}
// the page header's height (it wraps) feeds the sticky settings sub-navigation and the section scroll margin; the sticky action bar's (0 while hidden) lifts the toasts
const ph = $('#ph'); new ResizeObserver(() => document.documentElement.style.setProperty('--hdr-h', ph.offsetHeight + 'px')).observe(ph);
const ab = $('#actbar'); new ResizeObserver(() => document.documentElement.style.setProperty('--actbar-h', ab.offsetHeight + 'px')).observe(ab);
// daemon limits → hints, field bounds, slider minimums
const applyLimits = () => { const L = LIM;
	$('#cv-hint').textContent = `${L.curve_points_min}–${L.curve_points_max}`;
	$('#ac-pw-hint').firstChild.textContent = `${L.password_min}–${L.password_max} characters. Every other session is signed out; this one stays.`;
	const pw = $('#ac-new'); pw.minLength = L.password_min; pw.maxLength = L.password_max;
	for (const k in ED) { const { lv, c } = ED[k]; lv.min = minDuty(c); lv.range.min = lv.num.min = lv.min; lv.show(+lv.range.value, 1); } if (snap) renderSensors(); };

// pages: the nav (sidebar / icon rail / phone bottom bar + More sheet) is built from PAGES; hash routing #<page>[/<section>]
const PAGES = [
	{ g: 'Monitor', id: 'overview', t: 'Overview', ic: 'gauge', anon: true }, { g: 'Monitor', id: 'system', t: 'System', ic: 'chip' },
	{ g: 'Control', id: 'fans', t: 'Fans', ic: 'fan' }, { g: 'Control', id: 'schedules', t: 'Schedules', ic: 'clock' },
	{ g: 'Operate', id: 'alerts', t: 'Alerts', ic: 'bell' }, { g: 'Operate', id: 'log', t: 'Log', ic: 'list' },
	{ g: 'Settings', id: 'settings', t: 'Settings', ic: 'gear' },
	{ g: 'Info', id: 'about', t: 'About', ic: 'info', anon: true }];
const BOTTOM = ['overview', 'fans', 'alerts', 'settings']; // the phone bar; the rest goes into the More sheet
let cur = 'overview', lastHash = '';
const visible = () => PAGES.filter(p => signedIn() || p.anon);
const navBtn = (p, extra) => h('button', { 'aria-current': p.id === cur ? 'page' : null, title: p.t, 'data-page': p.id, onclick: () => goUser(p.id) }, ico(p.ic), h('span', { class: 'lbl' }, p.t), extra);
const dirtyDot = () => h('i', { class: 'dirty', hidden: !edDirty, title: 'unsaved changes', role: 'img', 'aria-label': 'unsaved changes' });
// 700–1099 px: the icon rail is forced (the 220 px sidebar leaves the header no room); the stored preference applies from 1100 px
const RAIL_MQ = matchMedia(`(max-width:${UI.bp.lg - .02}px)`); RAIL_MQ.addEventListener('change', () => buildNav());
// the sidebar toggle (panel-left icon): at the right end of the brand row while expanded, in the rail directly under the logo — one
// trigger, always in the sidebar (Proxmox / UniFi / Portainer pattern; operator decision rc4). The [ key toggles (toggleNav) when the
// focus is not in a field or a dialog. 700–1099 px: the rail is forced and has no toggle; the logo's tooltip says why.
const navForced = () => RAIL_MQ.matches, navRail = () => navForced() || S.nav === 'rail';
const toggleNav = () => { if (navForced()) return; S.nav = navRail() ? 'side' : 'rail'; saveS(); $('#s-nav').value = S.nav; buildNav(); $('#nav .tog').focus(); };
document.addEventListener('keydown', ev => { if (ev.key !== '[' || ev.ctrlKey || ev.metaKey || ev.altKey || ev.repeat) return;
	const t = ev.target; if (t.closest && t.closest('input, textarea, select, [contenteditable], dialog') || $('dialog[open]')) return; ev.preventDefault(); toggleNav(); });
function buildNav() {
	const nav = clear($('#nav')), forced = navForced(), rail = navRail(); $('#shell').classList.toggle('rail', rail);
	const tg = (rail ? 'Expand sidebar' : 'Collapse sidebar'), tip = tg + ' · [', forcedTip = 'Sidebar collapses below 1100 px';
	const tog = () => h('button', { class: 'btn icon link tog', title: tip, 'aria-label': tg, 'aria-expanded': String(!rail), 'aria-keyshortcuts': '[', onclick: toggleNav }, ico('panel'));
	// brand row: logo, name, toggle at the right; in the rail the same toggle sits directly under the logo — one trigger, always in the
	// sidebar (operator decision rc4: nothing in the page header). 700–1099 px: no toggle, the logo's tooltip says why.
	nav.append(h('div', { class: 'brand-row' }, h('span', { class: 'logo', title: forced ? forcedTip : null }, ico('fan')), h('span', { class: 'brand' }, 'n5-fangov'), rail ? null : tog()));
	if (rail) nav.append(forced ? null : tog(), h('div', { class: 'grp-sep' }));
	let lastG = null;
	for (const p of visible()) {
		if (p.g !== lastG) { const gid = 'grp-' + p.g.toLowerCase(); if (lastG) nav.append(h('div', { class: 'grp-sep' })); nav.append(h('div', { class: 'grp', id: gid }, p.g), h('ul', { 'aria-labelledby': gid })); lastG = p.g; }
		nav.lastChild.append(h('li', null, navBtn(p, p.id === 'fans' ? Object.assign(dirtyDot(), { id: 'nav-dirty' }) : null)));
	}
	nav.append(h('div', { class: 'foot' }, h('span', { class: 'vtxt', id: 'nav-ver' }, version ? 'v' + version.replace(/^v/, '') : '')));
	// phone: bottom bar + More sheet
	const b = clear($('#bnav')), vis = visible(), main = vis.filter(p => BOTTOM.includes(p.id)), rest = vis.filter(p => !BOTTOM.includes(p.id));
	for (const p of main) b.append(navBtn(p, p.id === 'fans' ? dirtyDot() : null));
	if (rest.length) b.append(h('button', { class: 'more', 'aria-current': rest.some(p => p.id === cur) ? 'page' : null, 'aria-haspopup': 'dialog', 'aria-controls': 'more', onclick: () => openMore(rest) }, ico('more'), h('span', { class: 'lbl' }, 'More')));
}
const openMore = rest => { const ul = clear($('#more-list'));
	for (const p of rest) ul.append(h('li', null, h('button', { 'aria-current': p.id === cur ? 'page' : null, onclick: () => { $('#more').close(); goUser(p.id); } }, ico(p.ic), p.t, h('span', { class: 'grp-t', 'aria-hidden': 'true' }, p.g))));
	$('#more').showModal(); ($('#more-list [aria-current]') || $('#more-list button')).focus(); };
// go: switch the page (auth-gated), mark the nav entries, keep the hash, optionally scroll to a section; false for an unknown/forbidden page.
// No hash yet (boot) or a fallback page: replaceState, not a push (a push re-triggers itself on Back). goUser (nav, links, Back/Forward) also moves the focus to the heading.
let byUser = false; const goUser = (id, sec) => { byUser = true; try { return go(id, sec); } finally { byUser = false; } };
function go(id, sec, replace) {
	const p = PAGES.find(x => x.id === id && (signedIn() || x.anon)); if (!p) return false;
	if (scGuard(p.id, sec)) return true; // unsaved schedule edits: the switch waits for the dialog
	const changed = cur !== p.id, user = byUser; cur = p.id;
	lastHash = '#' + cur + (sec ? '/' + sec : '');
	if (location.hash !== lastHash) { if (replace || !location.hash) history.replaceState(null, '', lastHash); else location.hash = lastHash; }
	for (const s of $$('.pg')) s.hidden = s.id !== 'p-' + cur;
	for (const b of $$('[data-page]')) b.dataset.page === cur ? b.setAttribute('aria-current', 'page') : b.removeAttribute('aria-current');
	const more = $('#bnav .more'); if (more) BOTTOM.includes(cur) ? more.removeAttribute('aria-current') : more.setAttribute('aria-current', 'page');
	renderHeader();
	if (changed) window.scrollTo(0, 0);
	// every page needs an entry (TestNavHasPages); a missing one would throw after the section switch
	(({ overview: () => { renderCharts(); pollSensors(); loadAlerts(); }, system: () => { renderSystemTab(); loadSystem(); },
		fans: () => { if (!edState && cfg) loadEditor(); renderLive(); pollSensors(1); drawEds(); loadPresets(); },
		schedules: loadSchedules, alerts: () => { alDirty = false; renderAlertsTab(); loadAlerts(); }, log: loadLog,
		settings: () => { alDirty = false; renderAlertsTab(); renderSettings(); }, about: () => { renderProfiles(); } })[id] || (() => {}))();
	const el = sec && $('#' + sec);
	if (sec) { if (el) el.scrollIntoView({ block: 'start' }); if (cur === 'settings') markSub(sec, true); }
	if (user && changed) { const f = el || $('#ph-title'); f.tabIndex = -1; f.focus({ preventScroll: true }); }
	return true;
}
// route from the hash (Overview when the page is unknown or needs auth); the 0.3 mock parameter &tab= is consumed once and maps
// curves|manual|presets → fans and compat → about (screenshot script)
let tabOnce = MOCK && Q.get('tab');
const route = () => { const tab = tabOnce, map = { curves: 'fans', manual: 'fans', presets: 'fans', compat: 'about' }, hp = location.hash.slice(1).split('/'); tabOnce = null;
	const id = tab ? map[tab] || tab : hp[0] || 'overview', sec = tab === 'compat' ? 'compat' : hp[1]; if (!go(id, sec)) go('overview', null, true); };
addEventListener('hashchange', () => { if (location.hash !== lastHash) { byUser = true; try { route(); } finally { byUser = false; } } });
document.addEventListener('click', ev => { const b = ev.target.closest('[data-go]'); if (b) goUser(b.dataset.go, b.dataset.sec); });
on('#skip', 'click', ev => { ev.preventDefault(); $('#main').focus(); }); // skip link: a hash href would route
for (const b of $$('[data-close]')) b.addEventListener('click', () => b.closest('dialog').close());
backdrop($('#more')); // the preset editor closes through peClose

// auth: mirrors the server's visibility split — nav entries, bottom bar and every [data-auth] element are hidden while anonymous
async function applyAuth() {
	const on = signedIn(), basic = sess.mode !== 'none';
	for (const el of $$('[data-auth]:not(.pg)')) el.hidden = !on;
	for (const [id, show] of [['#h-signin', !on && basic], ['#h-signout', on && basic], ['#h-user', on && basic]]) $(id).hidden = !show;
	$('#h-user-n').textContent = sess.user || ''; buildNav();
	if (!on) psReset();
	if (!on) { cert = null; cfg = null; edState = null; dirty(false); scReset(); alerts = null; sysinfo = null; dash = []; profiles = []; SN.key = null; for (const k in ED) delete ED[k]; for (const id of ['#fan-cards', '#ch-sel', '#preset-row', '#log', '#sc-tbl tbody', '#sc-kv', '#ac-tok tbody']) clear($(id));
		// protected content leaves the DOM, not only the view
		for (const el of $$('#p-system dl, #p-system tbody, #p-alerts dl, #p-alerts tbody, #p-alerts ul, #profiles tbody, #sensors, #sys, #alerts, #ac-sessions tbody, #ct-kv, #ct-san, #al-tpl')) clear(el);
		for (const id of ['#ct-fp', '#ac-user', '#ac-tk-nn', '#sy-when']) $(id).textContent = ''; ctClearUpload(); acReset(); $('#al-form').reset(); alDirty = false; if (ped.open) ped.close();
		route(); // a page that needs auth falls back to the Overview
		secState(); renderCharts(); loadAbout(); return; }
	hist = []; lastTs = 0; // history is re-read with the extra series
	await loadConfig(); loadCert(); loadDash(); loadAlerts(); loadSystem(); resetHistory(); loadAbout();
	api('/api/profiles').then(r => { profiles = r.body || []; renderHeader(); renderSystem(); renderProfiles(); }).catch(() => {});
	if (edStash) { edState = edStash; edStash = null; buildEditors(); dirty(true); cvNotice('Unsaved curve edits from before the session expired are restored — apply or revert.', ''); if (cur !== 'fans') toast('Unsaved curve edits restored (Fans page)', 'warn'); }
	if (scStash) { scState = scStash; scStash = null; scSetDirty(true); scErr('Unsaved schedule edits from before the session expired are restored — save or revert.', ''); if (cur !== 'schedules') toast('Unsaved schedule edits restored (Schedules page)', 'warn'); }
	route(); // the page's loader runs with the config known
	poll();
}
const ld = $('#login'), lf = $('#login-f'), lErr = notice('#l-err');
on('#h-signin', 'click', () => { lErr(''); ld.showModal(); $('#l-user').focus(); });
on('#l-close', 'click', () => ld.close()); backdrop(ld); ld.addEventListener('close', () => lf.reset());
lf.addEventListener('submit', async ev => { ev.preventDefault(); const u = $('#l-user').value.trim(), p = $('#l-pass').value; if (!u || !p) return lErr('user and password needed');
	const bt = $('#l-submit'); bt.disabled = true; lErr('');
	try { const r = await api('/api/login', { method: 'POST', json: { user: u, password: p, remember: $('#l-remember').checked } });
		sess = { authenticated: true, mode: 'basic', user: r.body.user || u, via: 'cookie' }; ld.close(); toast('Signed in', 'ok'); applyAuth();
		// focus: the page's sidebar entry, below 700 px (sidebar display:none) its bottom-bar entry, else the heading
		$$(`[data-page="${cur}"]`).concat($('#ph-title')).find(e => e.getClientRects().length).focus(); }
	catch (e) { lErr(e.status === 401 ? 'invalid user or password' : e.status === 429 ? 'too many attempts' : e.message); $('#l-pass').value = ''; $('#l-pass').focus(); }
	bt.disabled = false; });
on('#h-signout', 'click', async () => { const un = [edDirty && 'curve', scDirty && 'schedule'].filter(Boolean);
	if (un.length && !await ask('Sign out', `Unsaved ${un.join(' and ')} changes are discarded.`, { ok: 'Sign out', danger: true })) return;
	try { await api('/api/logout', { method: 'POST' }); } catch (e) {}
	sess = anon(sess.mode); toast('Signed out', 'ok'); applyAuth(); $('#h-signin').focus(); });

// overview
// tiles (§11a): one .tile per channel, built once and updated in place; the sparkline is the last 2 h of `hist` in the channel's series colour
const cards = {}, SPARK_S = RANGES['2h'].windowS;
function spark(cv, data, color, padR) { // padR: room for the range label at the right edge, so the line end never runs under it
	const dpr = window.devicePixelRatio || 1, W = cv.clientWidth, H = cv.clientHeight; if (!W || !H) return;
	cv.width = W * dpr; cv.height = H * dpr; const ctx = cv.getContext('2d'); ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
	if (data.length < 2) return;
	let lo = Infinity, hi = -Infinity; for (const [, v] of data) { if (v < lo) lo = v; if (v > hi) hi = v; } lo -= 1; hi += 1;
	const t1 = Date.now() / 1000, t0 = t1 - SPARK_S, col = cssVar(color), P = C.sparkPad;
	const X = t => P + (t - t0) / SPARK_S * (W - 2 * P - (padR || 0)), Y = v => P + (1 - (v - lo) / (hi - lo)) * (H - 2 * P);
	ctx.beginPath(); data.forEach(([t, v], i) => i ? ctx.lineTo(X(t), Y(v)) : ctx.moveTo(X(t), Y(v)));
	ctx.strokeStyle = col; ctx.lineWidth = C.sparkW; ctx.lineJoin = 'round'; ctx.stroke();
	const last = data[data.length - 1]; ctx.lineTo(X(last[0]), H); ctx.lineTo(X(data[0][0]), H); ctx.closePath(); ctx.globalAlpha = C.sparkFill; ctx.fillStyle = col; ctx.fill(); ctx.globalAlpha = 1;
	ctx.beginPath(); ctx.arc(X(last[0]), Y(last[1]), C.sparkDot, 0, 7); ctx.fillStyle = col; ctx.fill();
}
const sparkData = n => { const cut = Date.now() / 1000 - SPARK_S; return hist.filter(p => p.ts >= cut && p.temp && p.temp[n] > -900).map(p => [p.ts, tC(p.temp[n])]); };
const drawSpark = k => spark(k.cv, sparkData(k.name), k.color, k.rng.offsetWidth + C.sparkPad * 2);
const drawSparks = () => { for (const n in cards) drawSpark(cards[n]); };
function renderCards() {
	const host = $('#cards');
	const names = snap.channels.map(c => c.name);
	for (const k of Object.keys(cards)) if (!names.includes(k)) { cards[k].el.remove(); delete cards[k]; }
	snap.channels.forEach((c, i) => {
		let k = cards[c.name];
		if (!k) {
			k = cards[c.name] = { el: h('div', { class: 'card tile' }), name: c.name, cv: h('canvas', { role: 'img', 'aria-label': `${c.name} temperature, last 2 h` }), rng: h('span', { class: 'rng', 'aria-hidden': 'true' }, '2 h') };
			k.mode = h('span', { class: 'mode' }); k.hold = h('span', { class: 'badge hold', title: TIP.hold, hidden: true });
			k.temp = h('div', { class: 'temp' }); k.duty = h('span', { class: 'v' }); k.bar = h('i'); k.tgt = h('b', { hidden: true }); k.rpm = h('span', { class: 'v' });
			k.el.append(h('div', { class: 'top' }, chanHead(c), h('span', { class: 'badges' }, k.hold, k.mode)), k.temp,
				h('div', { class: 'spark', title: 'temperature, last 2 h' }, k.cv, k.rng),
				h('div', { class: 'row' }, h('span', { class: 'k', title: TIP.duty }, 'duty'), h('div', { class: 'bar-h' }, k.bar, k.tgt), k.duty),
				h('div', { class: 'row' }, h('span', { class: 'k' }, 'rpm'), h('span'), k.rpm));
			host.append(k.el);
			new ResizeObserver(() => drawSpark(k)).observe(k.cv);
		}
		k.color = seriesColor(i);
		const crit = critOf(c.name);
		modeBadge(k.mode, c.mode, c.ceiling_hit);
		k.temp.className = 'temp ' + tempClass(c.temp, crit);
		const held = c.held_temp !== undefined && c.held_temp !== null && c.held_temp !== c.temp;
		clear(k.temp).append(fmtT(c.temp), h('small', { title: limitTip(c.name) }, unit()));
		if (held) k.temp.append(h('small', { class: 'held', title: TIP.held }, `held ${fmtT(c.held_temp)}`));
		const left = c.hold_until > 0 ? Math.max(0, Math.round(c.hold_until - Date.now() / 1000)) : -1; k.hold.hidden = left < 0;
		if (left >= 0) k.hold.textContent = 'hold ' + (left >= 60 ? `${Math.ceil(left / 60)} min` : `${left} s`);
		k.bar.style.width = pct(c.duty) + '%'; k.bar.className = { critical: 'crit', failsafe: 'crit', stall: 'stall', auto: 'auto' }[c.mode] || '';
		const slewing = c.target !== undefined && c.target !== c.duty && c.mode !== 'stall';
		k.tgt.hidden = !slewing; k.tgt.style.left = `calc(${pct(c.target)}% - 1px)`;
		clear(k.duty).append(`${pct(c.duty)} % `, h('span', { class: 'tg', title: slewing ? TIP.slew : TIP.duty }, `(${c.duty}${slewing ? ' → ' + c.target : ''})`));
		clear(k.rpm).append(c.rpm < 0 ? 'no tach' : c.rpm === 0 ? h('span', { class: 't-crit' }, '0 rpm') : c.rpm.toLocaleString('en') + ' rpm');
		drawSpark(k);
	});
}
// system inventory: Overview card (at a glance) + System page (full tables)
let sysinfo = null;
const GiB = 1 << 30, fmtB = b => !(b > 0) ? '0 B' : b >= 1024 * GiB ? (b / 1024 / GiB).toFixed(1) + ' TiB' : b >= GiB ? (b / GiB).toFixed(1) + ' GiB' : Math.round(b / (1 << 20)) + ' MiB';
const fmtMb = m => m >= 1000 ? +(m / 1000).toFixed(1) + ' Gbit/s' : m + ' Mbit/s';
const na = v => v === undefined || v === null || v === '' ? '—' : v;
const dimm = m => `${fmtB(m.size_bytes)} ${m.type}${m.speed_mts ? '-' + m.speed_mts : ''}${m.ecc ? ' ECC' : ''}`;
const diskSum = ds => { const t = ds.reduce((a, x) => a + x.size_bytes, 0), hdd = ds.filter(x => x.rotational).length; return `${ds.length} disk${ds.length === 1 ? '' : 's'} · ${fmtB(t)}` + (ds.length ? ` (${ds.length - hdd} SSD, ${hdd} HDD)` : ''); };
const syNotice = notice('#sy-notice'), npuVer = v => (v || '').split(',')[0];
function renderSystem() {
	if (!snap || !signedIn()) return;
	const s = sysinfo, pr = profiles.find(p => p.name === snap.profile) || {}, d = cfg && cfg.daemon || {}, lg = cfg && cfg.log || {}, rows = [];
	if (s) { const m = s.machine, c = s.cpu, me = s.memory, fc = s.fan_controller, used = me.total_bytes - me.available_bytes;
		rows.push(['machine', `${na((m.vendor + ' ' + m.product).trim())} · ${na(m.board)} · BIOS ${na(m.bios_version)} (${na(m.bios_date)})`],
			['cpu', `${na(c.model)} · ${c.cores}c/${c.threads}t` + (c.max_mhz ? ` · ${(c.max_mhz / 1000).toFixed(1)} GHz` : '')],
			['memory', `${fmtB(used)} used of ${fmtB(me.total_bytes)}` + (me.installed_bytes ? ` · ${fmtB(me.installed_bytes)} installed (${me.modules.length} × ${dimm(me.modules[0])})` : '')],
			['gpu', s.gpus.map(g => `${g.name} · ${na(g.driver)}`).join(', ') || 'none'],
			['npu', s.npus.map(n => `${n.name} · ${na(n.driver)} ${npuVer(n.driver_version)}`).join(', ') || 'none'],
			['network', s.nics.map(n => `${n.name} ${n.state}${n.speed_mbit > 0 ? ' ' + fmtMb(n.speed_mbit) : ''}`).join(' · ') || 'no physical NIC'],
			['storage', diskSum(s.storage.disks)], ['os', `${na(s.host.os)} · ${na(s.host.kernel)}`],
			['fan control', `${snap.profile || '?'}${pr.title ? ' — ' + pr.title : ''} · ${na(snap.hwmon_path || fc.hwmon)}` + (fc.module ? ` · ${fc.module} ${fc.module_version}` : '')]);
	} else rows.push(['profile', `${snap.profile || '?'}${pr.title ? ' — ' + pr.title : ''}`], ['hwmon', na(snap.hwmon_path)], ['inventory', 'not available — see the System page']);
	rows.push(['daemon', `${version ? 'v' + version.replace(/^v/, '') : '—'} · interval ${na(d.interval)} · ${lg.file || 'journal only'}`]);
	if (s && s.errors.length) rows.push(['notes', s.errors.map(errText).join('; ')]);
	kv($('#sys'), rows);
}
const trow = (tb, ...cells) => tb.append(h('tr', null, ...cells.map(c => h('td', c && c.mono ? { class: 'mono', title: c.title } : null, c && c.mono ? c.mono : na(c))))), mono = (v, title) => v ? { mono: v, title } : null;
function renderSystemTab() {
	const s = sysinfo; if (!s) return;
	const hst = s.host, m = s.machine, c = s.cpu, me = s.memory, fc = s.fan_controller, used = me.total_bytes - me.available_bytes;
	$('#sy-when').textContent = `live ${hm(s.collected)} · static ${rel(s.static_at)}`;
	syNotice(s.errors.length ? 'Some sources could not be read:\n' + s.errors.map(errText).join('\n') : '', 'warn');
	kv($('#sy-host'), [['hostname', na(hst.hostname)], ['OS', na(hst.os)], ['kernel', na(hst.kernel)], ['uptime', fmtUp(hst.uptime_s)], ['load', `${hst.load1.toFixed(2)} · ${hst.load5.toFixed(2)} · ${hst.load15.toFixed(2)}`]]);
	kv($('#sy-machine'), [['vendor', na(m.vendor)], ['product', na(m.product)], ['board', na(m.board) + (m.board_vendor ? ` (${m.board_vendor})` : '')], ['BIOS', `${na(m.bios_version)} · ${na(m.bios_date)}`]]);
	kv($('#sy-cpu'), [['model', na(c.model)], ['sockets', c.sockets], ['cores / threads', `${c.cores} / ${c.threads}`], ['max clock', c.max_mhz ? `${c.max_mhz} MHz` : '—']]);
	kv($('#sy-fan'), [['profile', na(fc.profile)], ['hwmon', h('dd', { class: 'mono' }, na(fc.hwmon))], ['module', h('dd', { class: 'mono' }, na(fc.module))], ['module version', na(fc.module_version)]]);
	kv($('#sy-mem'), [['total', fmtB(me.total_bytes)], ['used', `${fmtB(used)} (${me.total_bytes ? Math.round(used / me.total_bytes * 100) : 0} %)`], ['available', fmtB(me.available_bytes)], ['swap', `${fmtB(me.swap_total_bytes - me.swap_free_bytes)} used of ${fmtB(me.swap_total_bytes)}`],
		['installed', me.installed_bytes ? `${fmtB(me.installed_bytes)} in ${me.modules.length} module${me.modules.length === 1 ? '' : 's'} (SMBIOS ${na(me.smbios)})` : 'unknown (SMBIOS table not readable)']]);
	let tb = clear($('#sy-dimms tbody')); for (const x of me.modules) trow(tb, x.slot, x.bank, fmtB(x.size_bytes), `${x.type} ${x.form_factor || ''}`, x.speed_mts ? x.speed_mts + ' MT/s' : '—', x.ecc ? 'yes' : 'no', x.manufacturer, mono(x.part));
	tb = clear($('#sy-acc tbody')); for (const g of s.gpus) trow(tb, 'GPU', g.name, g.vendor, mono(g.pci), mono(g.driver), '—', '—');
	for (const n of s.npus) trow(tb, 'NPU', n.name, '—', mono(n.pci), mono(n.driver), mono(npuVer(n.driver_version), n.driver_version), mono(n.accel));
	if (!s.gpus.length && !s.npus.length) trow(tb, 'none found');
	tb = clear($('#sy-nics tbody')); for (const n of s.nics) trow(tb, mono(n.name), n.state, n.speed_mbit > 0 ? fmtMb(n.speed_mbit) : '—', n.duplex, n.model, mono(n.driver), mono(n.mac), n.mtu, mono(n.pci));
	if (!s.nics.length) trow(tb, 'no physical interfaces');
	tb = clear($('#sy-ctl tbody')); for (const x of s.storage.controllers) trow(tb, x.kind, x.name, mono(x.driver), mono(x.pci));
	tb = clear($('#sy-disks tbody')); for (const x of s.storage.disks) trow(tb, mono(x.name), fmtB(x.size_bytes), x.rotational ? 'HDD' : 'SSD', x.transport, x.model, x.temp_c === null || x.temp_c === undefined ? '—' : `${fmtT(x.temp_c)} ${unit()}`);
	trow(tb, { mono: diskSum(s.storage.disks) });
}
async function loadSystem() { if (!signedIn()) return;
	try { sysinfo = (await api('/api/system')).body; renderSystem(); if (cur === 'system') renderSystemTab(); }
	catch (e) { if (e.status === 401) return; sysinfo = null; renderSystem(); if (cur === 'system') syNotice(e.status === 501 ? 'This daemon has no system inventory (older version?).' : 'system: ' + e.message, 'err'); } }
on('#sy-refresh', 'click', loadSystem);
// alerts list (Overview card + Alerts page); `error` = raised but not delivered
const alertList = (el, recent, empty) => { clear(el); if (!recent.length) el.append(h('li', { class: 'empty' }, empty || 'no alerts'));
	for (const a of recent) el.append(h('li', null, h('span', { class: 'k ' + a.kind }, a.kind), h('span', { class: 'msg' }, a.msg || '', a.error ? h('span', { class: 'de' }, 'not delivered: ' + a.error) : null), tm(a.ts))); };
const alNotice = notice('#al-notice');
async function loadAlerts() { if (!signedIn()) return;
	try { alerts = (await api('/api/alerts')).body; alertList($('#alerts'), alerts.recent || [], 'no alerts — Send test alert on the Alerts page checks the transport'); alNotice(''); if (cur === 'alerts' || cur === 'settings') renderAlertsTab(); }
	catch (e) { if (e.status === 401) return; alerts = null; alertList($('#alerts'), [], 'alerts: unavailable'); alertList($('#al-recent'), []); alNotice('alerts: ' + e.message); } }
// sensors card, grouped by `kind` first (disk:* → SSD·NVMe or HDD), then id prefix / hwmon chip
const GROUPS = [['CPU', /^(k10temp|coretemp)/], ['SSD · NVMe', /^nvme/], ['HDD', /^drivetemp/], ['GPU', /^(amdgpu|nouveau|i915|radeon)/], ['NIC', /^(nic|eth|mlx|igc|ixgbe|r8169|atlantic)/], ['EC · board', /^(ec$|minisforum|acpitz|spd5118)/]];
const groupOf = s => { if (s.kind === 'ssd') return 'SSD · NVMe'; if (s.kind === 'hdd') return 'HDD';
	const id = s.id, k = id.startsWith('hwmon:') ? id.slice(6) : id.split(':')[0], g = GROUPS.find(x => x[1].test(k)); return g ? g[0] : 'other'; };
const SN = { key: null, rows: {}, grp: {} };
const concrete = () => sensors.filter(s => !s.id.includes('<')); // id patterns (with <) are not selectable
const snDesc = id => { const s = sensors.find(x => x.id === id); return s && s.description ? s.description.replace(/\s*\(now [^)]*\)\s*$/, '') : id; }; // catalogue description for legends and rows, the id as fallback
// one <details> per group (summary: name · count · live max); open by default when the group holds a channel sensor (composite parts included) or a
// charted one, a group the user toggles keeps its state in S.sensors (localStorage)
function renderSensors() {
	const host = $('#sensors'), sensors = concrete(), key = sensors.map(s => s.id).join(','), max = LIM.dashboard_sensors_max;
	$('#sn-hint').textContent = `chart = record in the history (${dash.length}/${max})`;
	if (key !== SN.key) { SN.key = key; SN.rows = {}; SN.grp = {}; clear(host); const by = {}, chS = chList().flatMap(c => parts(c.sensor));
		for (const s of sensors) (by[groupOf(s)] = by[groupOf(s)] || []).push(s);
		for (const g of [...GROUPS.map(x => x[0]), 'other']) { if (!by[g]) continue; const mx = SN.grp[g] = h('span', { class: 'gm' }), open = typeof S.sensors[g] === 'boolean' ? S.sensors[g] : by[g].some(s => chS.includes(s.id) || dash.includes(s.id));
			const det = h('details', { class: 'sg', open }, h('summary', { onclick: () => { S.sensors[g] = !det.open; saveS(); } }, g, ' · ', by[g].length, ' · ', mx)); host.append(det);
			for (const s of by[g]) { const r = SN.rows[s.id] = { v: h('span', { class: 'val' }), b: h('button', { class: 'btn sm', 'aria-label': 'chart ' + s.id }, ico('chart'), 'chart') };
				r.b.addEventListener('click', () => { const off = dash.includes(s.id); setDash(off ? dash.filter(x => x !== s.id) : dash.concat(s.id), n => off ? `${s.id} removed from the chart (${n}/${max})` : `${s.id} added to the chart (${n}/${max})`); });
				det.append(h('div', { class: 'sn' }, h('span', null, h('span', { class: 'd' }, snDesc(s.id)), h('span', { class: 'id mono' }, s.id)), r.v, r.b)); } }
		if (!sensors.length) host.append(h('div', { class: 'empty' }, 'no readable sensors — the Log page shows why the catalogue is empty')); }
	const gmax = {};
	for (const s of sensors) { const r = SN.rows[s.id], on = dash.includes(s.id), v = snap && snap.watched && snap.watched[s.id] !== undefined ? snap.watched[s.id] : s.temp, g = groupOf(s);
		if (v > -900) gmax[g] = Math.max(v, gmax[g] === undefined ? -Infinity : gmax[g]);
		r.v.textContent = v === undefined || v === null ? '—' : fmtT(v) + ' ' + unit(); r.b.classList.toggle('on', on); r.b.setAttribute('aria-pressed', String(on)); r.b.disabled = !on && dash.length >= max;
		r.b.title = r.b.disabled ? `at most ${max} sensors — remove one from the chart first` : on ? 'remove from the chart' : 'add to the chart'; }
	for (const g in SN.grp) SN.grp[g].textContent = gmax[g] === undefined ? 'max —' : `max ${fmtT(gmax[g])} ${unit()}`;
}
async function pollSensors(force) { if (!signedIn() || !force && cur !== 'overview') return;
	try { const first = !sensors.length; sensors = (await api('/api/sensors')).body || []; renderSensors(); fillSensorSelects(); if (first && sensors.length) renderCharts(); } catch (e) {} } // first catalogue: the extra-chart legend switches from ids to descriptions
async function loadDash() { try { const was = dash.join(); dash = (await api('/api/dashboard')).body.sensors || []; if (dash.join() !== was) SN.key = null; renderSensors(); renderCharts(); } catch (e) {} } // a changed watch list re-evaluates the groups' open-by-default rule (R01)
async function setDash(ids, msg) { const r = await act(() => api('/api/dashboard', { method: 'PUT', json: { sensors: ids } }), r => msg((r.body.sensors || ids).length)); if (!r) return;
	dash = r.body.sensors || ids; const w = r.body.warnings || []; if (w.length) toast(w.join('\n'), 'warn'); renderSensors(); renderCharts(); resetHistory(); loadConfig(); } // [dashboard] changed in the file: cfgRaw follows
function renderCharts() {
	if (!snap) return;
	const names = snap.channels.map(c => c.name);
	const mk = (key, f) => names.map((n, i) => ({ name: n, color: seriesColor(i), data: hist.filter(p => p[key] && p[key][n] > -900).map(p => [p.ts, f ? f(p[key][n]) : p[key][n]]) }));
	const legend = (el, ss, rm) => { clear(el); for (const s of ss) { const i = h('i'); i.style.background = `var(${s.color})`;
		el.append(h('span', { title: s.id || null }, i, s.name, rm ? h('button', { class: 'x', 'aria-label': 'remove ' + (s.id || s.name), title: 'remove from the chart', onclick: () => rm(s.id || s.name) }, ico('close')) : null)); } };
	const tf = { fmt: (v, ax) => v.toFixed(ax ? 0 : 1) + (ax ? '' : ' ' + unit()), minSpan: C.minSpanTemp };
	const ts = mk('temp', tC), last = ' · last ' + R().label; legend($('#lg-temp'), ts);
	$('#ch-temp-title').textContent = 'Temperature' + last; $('#ch-extra-title').textContent = 'Extra sensors' + last;
	chart($('#ch-temp'), ts, tf);
	const fs = mk(fanMetric); legend($('#lg-fan'), fs);
	$('#ch-fan-title').textContent = (fanMetric === 'rpm' ? 'Fan speed' : 'Duty') + last;
	// rpm autoscales like the temperature (a 0-based axis squeezes 2000–3300 rpm into the top third), duty keeps 0..255
	chart($('#ch-fan'), fs, fanMetric === 'rpm' ? { fmt: (v, ax) => ax ? String(Math.round(v)) : Math.round(v) + ' rpm', minSpan: C.minSpanRpm, floor: 0 } : { fmt: (v, ax) => ax ? String(Math.round(v)) : `${Math.round(v)} (${pct(v)} %)`, yMin: 0, yMax: LIM.duty });
	// extra sensors chart, only while something is watched
	const on = signedIn() && dash.length > 0; $('#extra-card').hidden = !on; if (!on) return;
	const es = dash.map((id, i) => ({ id, name: snDesc(id), color: seriesColor(names.length + i), data: hist.filter(p => p.extra && p.extra[id] > -900).map(p => [p.ts, tC(p.extra[id])]) }));
	legend($('#lg-extra'), es, id => setDash(dash.filter(x => x !== id), n => `${id} removed from the chart (${n}/${LIM.dashboard_sensors_max})`)); chart($('#ch-extra'), es, tf);
}
const segOn = (sel, b) => $$(sel).forEach(x => { x.classList.toggle('on', x === b); x.setAttribute('aria-pressed', x === b); });
$$('#fan-seg button').forEach(b => b.addEventListener('click', () => { fanMetric = b.dataset.metric; segOn('#fan-seg button', b); renderCharts(); }));
// range selector: the tier changes with the range, so the history is reloaded and the poll timer re-armed
const setRange = r => { if (!RANGES[r]) return; S.range = r; saveS(); segOn('#rg-seg button', $(`#rg-seg button[data-range="${r}"]`)); $('#ov-csv').title = `Download the last ${RANGES[r].label} as CSV`; };
$$('#rg-seg button').forEach(b => b.addEventListener('click', () => { if (b.dataset.range === S.range) return; setRange(b.dataset.range); resetHistory(); schedule(); }));
setRange(S.range);
on('#ov-csv', 'click', () => act(() => download('/api/history.csv?minutes=' + R().minutes, 'n5-fangov-history.csv'), n => `History exported as ${n}`));

// Fans page (§11a): one card per channel — the curve editor (edited in °C whatever the display unit; edState = working copy,
// edDirty = unsaved) on the left, "Live & override" on the right; below 700 px the channel selector shows one card at a time
const ED = {}, drawEds = () => { for (const k in ED) ED[k].draw(); }; let edState = null, edStash = null, edDirty = false;
const cvNotice = notice('#cv-notice'), K = UI.curve;
const chDirty = name => { const e = ED[name], o = chList().find(x => x.name === name); return !!(e && o) && chKey([e.c]) !== chKey([o]); };
const dirty = v => { edDirty = !!v; $('#cv-dirty').hidden = !v; $('#cv-clean').hidden = !!v; $('#cv-saveas').hidden = !v; for (const d of $$('#nav-dirty, #bnav .dirty')) d.hidden = !v;
	for (const b of $$('#ch-sel button')) b.lastChild.hidden = !v || !chDirty(b.dataset.ch); };
addEventListener('beforeunload', ev => { if (edDirty || scDirty) ev.preventDefault(); });
// Go durations ("1m0s") ↔ seconds; min_on is kept in the daemon's canonical form, the select lists the common values
const durS = s => { const m = /^(?:(\d+)h)?(?:(\d+)m)?(?:(\d+(?:\.\d+)?)s)?$/.exec(s || ''); return m && m[0] ? (+m[1] || 0) * 3600 + (+m[2] || 0) * 60 + (+m[3] || 0) : 0; };
const MIN_ON = [['0s', 'off'], ['30s', '30 s'], ['1m0s', '1 min'], ['2m0s', '2 min'], ['5m0s', '5 min'], ['10m0s', '10 min'], ['30m0s', '30 min'], ['1h0m0s', '1 h']];
const normDur = s => { const t = durS(s), o = MIN_ON.find(x => durS(x[0]) === t); return o ? o[0] : t ? `${t / 3600 | 0}h${t % 3600 / 60 | 0}m${t % 60}s`.replace(/^0h/, '').replace(/^0m/, '') : '0s'; };
const parts = s => String(s || '').split(',').map(x => x.trim()).filter(Boolean);
const minOnSel = (c, onchange) => { const mo = h('select', { title: TIP.minOn, onchange: () => { c.min_on = mo.value; onchange(); } });
	for (const [v, t] of MIN_ON.filter(x => durS(x[0]) <= LIM.min_on_max_s).concat(MIN_ON.some(x => x[0] === c.min_on) ? [] : [[c.min_on, fmtDur(c.min_on)]])) mo.append(h('option', { value: v, selected: v === c.min_on }, t)); return mo; };
// [[channel]] tables: sensor as an array for a composite, hysteresis / min_on omitted at their defaults
const tomlChannel = c => { const ps = parts(c.sensor);
	return `[[channel]]\nname = "${c.name}"\npwm = ${c.pwm}\nsensor = ${ps.length > 1 ? `[${ps.map(x => `"${x}"`).join(', ')}]` : `"${ps[0] || ''}"`}\ncurve = [${c.curve.map(p => `[${p[0]}, ${p[1]}]`).join(', ')}]\ncritical = ${c.critical}\nstop = ${stopN(c.stop) === 'auto' ? '"auto"' : stopN(c.stop)}\n`
		+ (c.hysteresis > 0 ? `hysteresis = ${c.hysteresis}\n` : '') + (durS(c.min_on) ? `min_on = "${c.min_on}"\n` : '') + (c.ceiling > 0 ? `ceiling = ${c.ceiling}\n` : ''); }; // ceiling: kept as configured (no editor field), the daemon can only lower it
// every [[channel]] table is dropped as a block; the block ends at the next table header of ANY kind — [section] or [[other]], e.g. [[schedule]] —
// so the other array tables survive the rewrite. A header carries a bare/quoted key path only: an array element line ("[45, 85],") has a comma and is no header.
const TOML_HDR = /^\[\[?\s*[\w.\-"' ]+\s*\]\]?\s*(#.*)?$/, CH_HDR = /^\[\[\s*channel\s*\]\]/;
const stripChannels = raw => { const out = []; let skip = false;
	for (const ln of raw.split('\n')) { const t = ln.trim();
		if (CH_HDR.test(t)) { skip = true; continue; }
		if (TOML_HDR.test(t)) skip = false;
		if (!skip) out.push(ln); } return out.join('\n').replace(/\n{3,}/g, '\n\n').trimEnd() + '\n\n'; };
// a channel as the editors hold it (copied: the preset editor and the stash never share arrays with the config or a preset detail)
const chCopy = c => ({ name: c.name, pwm: +c.pwm, sensor: parts(c.sensor).join(','), curve: (c.curve || []).map(p => [+p[0], +p[1]]), critical: +c.critical, stop: stopN(c.stop),
	hysteresis: +c.hysteresis || 0, min_on: normDur(c.min_on), ceiling: +c.ceiling || 0 });
const fromCfg = () => chList().map(chCopy);
function loadEditor(keepNotice) { edState = fromCfg(); dirty(false); buildEditors(keepNotice); }
// the channel selector below 700 px: one card at a time, the selected channel keeps across rebuilds; a resize across the breakpoint switches modes
const SEL = { mq: matchMedia(`(max-width:${UI.bp.sm - .02}px)`), ch: null };
const selMode = () => { const on = SEL.mq.matches, sel = $('#ch-sel'); sel.hidden = !on;
	if (on && !(SEL.ch in ED)) SEL.ch = Object.keys(ED)[0] || null;
	for (const k in ED) ED[k].card.hidden = on && k !== SEL.ch;
	for (const b of $$('#ch-sel button')) { const cur = on && b.dataset.ch === SEL.ch; b.classList.toggle('on', cur); b.setAttribute('aria-pressed', cur); }
	drawEds(); };
SEL.mq.addEventListener('change', selMode);
function buildEditors(keepNotice) { // keepNotice: the 202 / warnings notice of an apply survives the reload of the editors (cleared by Revert, page switch, next apply)
	const host = clear($('#fan-cards')), sel = clear($('#ch-sel')); for (const k in ED) delete ED[k];
	if (!keepNotice) cvNotice('');
	edState.forEach((c, i) => {
		const ed = ED[c.name] = { c, i }, L = LIM, dt = () => dirty(1);
		const cv = h('canvas', { role: 'img', 'aria-label': `curve ${c.name}` });
		const sl = h('select', { title: TIP.sensor, 'aria-label': `${c.name} sensor`, onchange: () => { c.sensor = sl.value; dt(); } });
		const crit = h('input', { type: 'number', min: L.critical_min, max: L.critical_max, value: c.critical, title: TIP.critical, oninput: () => { c.critical = +crit.value; dt(); ed.draw(); } });
		const stop = h('input', { type: 'text', value: c.stop, placeholder: 'auto', title: TIP.stop, oninput: () => { c.stop = stop.value.trim(); dt(); } });
		const hyst = h('input', { type: 'number', min: 0, max: L.hysteresis_max, step: 1, value: c.hysteresis, title: TIP.hyst, oninput: () => { c.hysteresis = +hyst.value; dt(); } });
		const mo = minOnSel(c, dt);
		const tbody = h('tbody'), ptsEl = h('div', { class: 'ptl' });
		// duty→RPM reference: filled by renderLive once the profile is known (the editors are built before the first /api/state)
		const ref = REF[c.name] ? h('p', { class: 'ref', hidden: true }) : null;
		ed.ref = () => { if (!ref || ref.childNodes.length || profName() !== 'n5pro') return; ref.hidden = false;
			ref.append('Duty → RPM (measured): ', ...REF[c.name].flatMap(([d, r], j) => [j ? ' · ' : '', h('b', null, `${d}→${r}`)])); };
		ed.sel = sl; ed.cv = cv; ed.tbody = tbody; ed.ptsEl = ptsEl;
		const fillTable = () => {
			clear(tbody); c.curve.forEach((p, j) => tbody.append(h('tr', null,
				h('td', null, h('input', { type: 'number', min: L.temp[0], max: L.temp[1], value: p[0], 'aria-label': `point ${j + 1} temp`, oninput: ev => { p[0] = +ev.target.value; dt(); ed.draw(); } })),
				h('td', null, h('input', { type: 'number', min: 0, max: L.duty, value: p[1], 'aria-label': `point ${j + 1} duty`, oninput: ev => { p[1] = clamp(+ev.target.value, 0, L.duty); dt(); ed.draw(); } })),
				h('td', null, h('button', { class: 'btn sm', disabled: c.curve.length <= L.curve_points_min, title: c.curve.length <= L.curve_points_min ? `at least ${L.curve_points_min} points` : null, onclick: () => { c.curve.splice(j, 1); dt(); fillTable(); ed.draw(); } }, 'remove')))));
			addBtn.disabled = c.curve.length >= L.curve_points_max; addBtn.title = addBtn.disabled ? `at most ${L.curve_points_max} points` : '';
		};
		// re-sort once focus leaves the table (a rebuild mid-click swallows the click)
		ed.sort = () => { const before = c.curve.slice(); c.curve.sort((a, b) => a[0] - b[0]); if (before.some((x, k) => x !== c.curve[k])) { fillTable(); ed.draw(); } };
		tbody.addEventListener('focusout', ev => { if (!tbody.contains(ev.relatedTarget)) ed.sort(); });
		// add point: middle of the widest gap, inserted sorted
		const at = h('input', { type: 'number', min: L.temp[0], max: L.temp[1], 'aria-label': 'new point temp' }), ad = h('input', { type: 'number', min: 0, max: L.duty, 'aria-label': 'new point duty' });
		const addRow = h('div', { class: 'addp' }, h('label', null, '°C', at), h('label', { title: TIP.duty }, 'duty', ad),
			h('button', { class: 'btn sm primary', onclick: () => { const t = +at.value, d = clamp(Math.round(+ad.value), 0, L.duty); if (at.value === '') return at.focus();
				if (c.curve.some(p => p[0] === t)) return toast(`${c.name}: a point at ${t} °C exists`, 'warn');
				const np = [t, d]; c.curve.push(np); c.curve.sort((a, b) => a[0] - b[0]); addRow.hidden = true; dt(); fillTable(); ed.draw(); tbody.rows[c.curve.indexOf(np)].cells[0].firstChild.focus(); } }, 'Add'),
			h('button', { class: 'btn sm', onclick: () => { addRow.hidden = true; addBtn.focus(); } }, 'Cancel'));
		addRow.hidden = true;
		const addBtn = h('button', { class: 'btn sm', onclick: () => { const [t, d] = gapPoint(c.curve); at.value = t; ad.value = d; addRow.hidden = false; at.focus(); at.select(); } }, '+ add point');
		// live & override: the values refresh with every state poll (renderLive), the switch is the override
		const lv = ed.lv = { temp: h('span', { class: 't' }), duty: h('span', { class: 'd' }), rpm: h('span', { class: 'hint sm' }), tgt: h('span'), mode: h('span', { class: 'mode' }), hmode: h('span', { class: 'mode' }),
			pre: h('span', { class: 'badge preset', hidden: true }), warn: h('p', { class: 'warn' }), min: minDuty(c) };
		// the manual block is disabled (not only dimmed) while the switch is off: keyboard and screen reader see the gate
		lv.range = h('input', { type: 'range', min: lv.min, max: L.duty, value: 0, disabled: true, 'aria-label': `${c.name} manual duty`, oninput: () => { lv.dirty = 1; lv.show(+lv.range.value); } });
		lv.num = h('input', { type: 'number', min: lv.min, max: L.duty, value: 0, disabled: true, 'aria-label': `${c.name} manual duty value`, title: TIP.duty, oninput: () => { lv.dirty = 1; lv.show(clamp(+lv.num.value || 0, 0, L.duty), 1); } });
		lv.set = h('button', { class: 'btn primary sm', disabled: true, onclick: async () => { const v = +lv.range.value; lv.dirty = 0;
			if (await act(() => api('/api/override/' + c.name, { method: 'PUT', json: { duty: v } }), `${c.name}: manual ${v} (${pct(v)} %)`)) lv.pend(true, v); poll(); } }, 'Set');
		lv.pctEl = h('span', { class: 'hint sm' });
		lv.show = (v, keep) => { lv.range.value = v; if (!keep) lv.num.value = v; lv.pctEl.textContent = `${pct(v)} %`;
			lv.low = lv.min > 0 && v < lv.min; lv.set.disabled = lv.low || lv.box.dataset.off === 'true';
			lv.warn.textContent = lv.min ? `HDD-like channel: minimum ${lv.min} (the EC stops regulating it; lower values are refused)` : 'Critical-temperature and stall guards still apply and override any manual value.'; lv.warn.classList.toggle('on', lv.low); };
		// the switch: Manual holds the duty the channel runs now (PUT /api/override with the snapshot duty), Auto is the DELETE. The snapshot mode follows
		// with the next cycle, so the switch shows the client flag lv.on, held through a pending window (lv.pend) — see renderLive. aria-busy, not disabled: the focus stays
		lv.sw = h('button', { class: 'switch', role: 'switch', 'aria-checked': 'false', 'aria-label': `${c.name}: manual override`, onclick: async () => {
			if (lv.busy) return; const on = !lv.on, live = snap && snap.channels.find(x => x.name === c.name) || {}; lv.busy = 1; lv.sw.setAttribute('aria-busy', 'true');
			const d = Math.max(lv.min, live.duty >= 0 ? live.duty : live.target >= 0 ? live.target : lv.min); // duty -1 = write failed: hold the target, never 0
			const r = on ? await act(() => api('/api/override/' + c.name, { method: 'PUT', json: { duty: d } }), `${c.name}: manual — holding ${d} (${pct(d)} %)`)
				: await act(() => api('/api/override/' + c.name, { method: 'DELETE' }), `${c.name}: back to auto`);
			lv.busy = 0; lv.sw.removeAttribute('aria-busy'); if (r) { lv.dirty = 0; lv.pend(on, d); lv.state(on ? 'manual' : 'auto'); if (on) lv.show(d); } poll(); } },
			h('span', { class: 'track', 'aria-hidden': 'true' }), h('span', null, h('b', null, 'Manual'), ' override'));
		lv.on = false; lv.pend = (on, v) => { lv.on = on; lv.want = v; lv.until = Date.now() + 2 * Math.max(2, cfg && cfg.daemon ? durS(cfg.daemon.interval) : 10) * 1000; };
		lv.state = m => { const on = lv.on; lv.sw.setAttribute('aria-checked', on); lv.box.dataset.off = !on; lv.range.disabled = lv.num.disabled = !on; lv.set.disabled = !on || !!lv.low;
			modeBadge(lv.mode, m); modeBadge(lv.hmode, m); };
		lv.box = h('div', { class: 'live', 'data-off': 'true' }, h('h3', null, `Live & override — ${c.name}`),
			h('div', { class: 'now' }, lv.temp, h('span', { class: 'arrow', 'aria-hidden': 'true' }, '→'), lv.duty, lv.rpm),
			h('div', { class: 'tgt' }, lv.tgt, lv.mode), lv.sw,
			h('div', { class: 'man' }, h('div', { class: 'rg' }, lv.range, lv.num, lv.pctEl), h('div', { class: 'actions' }, lv.set)), lv.warn);
		lv.show(0);
		ed.card = h('div', { class: 'card fan' },
			h('div', { class: 'fh' }, h('h2', { class: 'name' }, c.name), ed.sub = h('span', { class: 'sub' }, `pwm${c.pwm} · ${c.sensor || '?'}`), h('span', { class: 'badges' }, lv.pre, lv.hmode)),
			h('div', { class: 'ed' },
				h('div', { class: 'fields' }, h('label', null, 'sensor', sl), h('label', { title: TIP.critical }, 'critical °C', crit), h('label', { title: TIP.stop }, 'stop', stop),
					h('label', { title: TIP.hyst }, 'hysteresis °C', hyst), h('label', { title: TIP.minOn }, 'min on', mo)),
				h('div', { class: 'cvs' }, cv, ptsEl),
				h('div', { class: 'pts' }, h('table', null, h('thead', null, h('tr', null, h('th', null, '°C'), h('th', { title: TIP.duty }, 'duty'), h('th'))), tbody), h('div', { class: 'addc' }, addBtn, addRow)),
				ref),
			lv.box);
		host.append(ed.card);
		sel.append(h('button', { 'data-ch': c.name, 'aria-pressed': 'false', onclick: () => { SEL.ch = c.name; selMode(); } }, c.name, h('span', { class: 'sub' }, `pwm${c.pwm}`), dirtyDot()));
		fillTable();
		ed.draw = () => drawCurve(ed);
		bindCurveDrag(ed);
		new ResizeObserver(ed.draw).observe(cv.parentNode);
	});
	fillSensorSelects(); selMode(); if (snap) renderLive(); presetBadges(); dirty(edDirty);
}
// a new point: the middle of the widest gap, duty interpolated (shared by the curve editor and the preset editor)
const gapPoint = curve => { const s = curve.slice().sort((a, b) => a[0] - b[0]); let bi = 1, bw = -1;
	for (let k = 1; k < s.length; k++) if (s[k][0] - s[k - 1][0] > bw) { bw = s[k][0] - s[k - 1][0]; bi = k; }
	const t = s.length > 1 ? Math.round((s[bi - 1][0] + s[bi][0]) / 2) : (s[0] ? s[0][0] : K.addDefault) + K.addStep; return [t, Math.round(interp(s, t))]; };
// live block: every poll (no editor rebuild); the slider follows the running duty until the operator touches it
function renderLive() { if (!snap) return;
	for (const c of snap.channels) { const ed = ED[c.name]; if (!ed) continue; const { lv } = ed, crit = critOf(c.name);
		lv.temp.className = 't ' + tempClass(c.temp, crit); clear(lv.temp).append(fmtT(c.temp), h('small', null, ' ' + unit()));
		clear(lv.duty).append(String(c.duty), h('small', null, ` duty · ${pct(c.duty)} %`)); lv.rpm.textContent = c.rpm < 0 ? 'no tach' : `${c.rpm.toLocaleString('en')} rpm`;
		// lag: the snapshot trails a PUT/DELETE by a cycle — flag and slider wait for the daemon to agree or the window to pass; manual → on, auto → off,
		// critical/stall leave the flag alone (the override persists underneath, the switch still turns it off)
		const man = c.mode === 'manual', lag = lv.until > Date.now() && (man !== lv.on || man && c.target !== lv.want);
		if (!lv.busy && !lag) { lv.until = 0; if (man) lv.on = true; else if (c.mode === 'auto') lv.on = false; }
		lv.state(c.mode); if (c.ceiling_hit) modeBadge(lv.mode, c.mode, true), modeBadge(lv.hmode, c.mode, true);
		lv.tgt.textContent = (lv.on ? `held ${man && !lag ? c.target : lv.range.value}` : `curve target ${c.target !== undefined ? c.target : '—'}`) + ' · mode';
		if (!lv.dirty && !lv.busy && !lag) { if (!lv.on) lv.show(c.duty); else if (man) lv.show(c.target); }
		const mn = minDuty(chList().find(x => x.name === c.name) || ed.c); if (mn !== lv.min) { lv.min = mn; lv.range.min = lv.num.min = mn; lv.show(+lv.range.value, 1); } // profile known now
		// the daemon's ceiling arrives with the snapshot: header tooltip, and the editor redraws its line once it is known
		if (c.ceiling > 0 && ed.ceil !== c.ceiling) { ed.ceil = c.ceiling; ed.sub.title = `ceiling ${fmtT(c.ceiling, 0)} ${unit()} (built-in floor below critical)`; ed.draw(); }
		ed.ref(); }
}
// a critical above the daemon's ceiling is accepted, the ceiling just acts first: one warning line per channel for the notice (never blocks)
const ceilingWarnings = (chs = edState) => chs.flatMap(c => { const l = ceilOf(c.name); return l && +c.critical > l ? [`${c.name}: critical ${c.critical} above the built-in ceiling ${l} — the ceiling acts first`] : []; });
function fillSensorSelects() {
	// a composite id "a,b" is one option (never dropped by the editor); the catalogue's single ids follow
	for (const k in ED) { const { c, sel } = ED[k]; const ids = new Set([c.sensor, ...concrete().map(s => s.id)]); clear(sel);
		for (const id of ids) { const s = sensors.find(x => x.id === id), n = parts(id).length;
			sel.append(h('option', { value: id, selected: id === c.sensor }, id + (n > 1 ? ` (max of ${n})` : s && s.temp !== undefined ? ` (${s.temp.toFixed(1)} °C)` : ''))); } }
}
const curveGeom = ed => { const W = ed.cv.clientWidth, H = ed.cv.clientHeight, pad = K.pad;
	const xmax = clamp((ed.c.critical || 0) + K.xPad, K.xMin, LIM.critical_max + K.xPad);
	return { W, H, pad, xmax, X: t => pad.l + t / xmax * (W - pad.l - pad.r), Y: d => pad.t + (1 - d / LIM.duty) * (H - pad.t - pad.b),
		T: x => (x - pad.l) / (W - pad.l - pad.r) * xmax, D: y => (1 - (y - pad.t) / (H - pad.t - pad.b)) * LIM.duty }; };
function drawCurve(ed) {
	const { cv, c } = ed, dpr = window.devicePixelRatio || 1, g = curveGeom(ed), D = LIM.duty; if (!g.W) return;
	cv.width = g.W * dpr; cv.height = g.H * dpr; const ctx = cv.getContext('2d'); ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
	ctx.fillStyle = cssVar('--bg'); ctx.fillRect(0, 0, g.W, g.H);
	ctx.font = UI.font('--fs-11'); ctx.fillStyle = cssVar('--fg3'); ctx.strokeStyle = cssVar('--line'); ctx.textAlign = 'right'; ctx.textBaseline = 'middle';
	for (let d = 0; d <= D; d += K.yStep) { const y = Math.round(g.Y(d)) + .5; seg(ctx, g.pad.l, y, g.W - g.pad.r, y); ctx.fillText(pct(d) + '%', g.pad.l - 5, y); }
	ctx.textAlign = 'center'; ctx.textBaseline = 'top';
	for (let t = 0; t <= g.xmax; t += K.xStep) { const x = Math.round(g.X(t)) + .5; seg(ctx, x, g.pad.t, x, g.H - g.pad.b); ctx.fillText(t + '°', x, g.H - g.pad.b + 5); }
	if (c.critical) { const x = Math.round(g.X(c.critical)) + .5; ctx.strokeStyle = cssVar('--crit'); seg(ctx, x, g.pad.t, x, g.H - g.pad.b, C.dash.crit);
		ctx.fillStyle = cssVar('--crit'); ctx.textAlign = 'right'; ctx.fillText('crit', x - 3, g.pad.t); }
	// the daemon's ceiling (0.4.1): a second dashed line, dimmed, its label a row below crit; only when it is in range
	const ceil = ceilOf(c.name);
	if (ceil && ceil <= g.xmax) { const x = Math.round(g.X(ceil)) + .5; ctx.globalAlpha = C.ceilAlpha; ctx.strokeStyle = cssVar('--crit'); seg(ctx, x, g.pad.t, x, g.H - g.pad.b, C.dash.crit);
		ctx.fillStyle = cssVar('--crit'); ctx.textAlign = 'right'; ctx.fillText('ceiling', x - 3, g.pad.t + C.labelH); ctx.globalAlpha = 1; } // label on the second row, left of the line (the line may sit on the right edge)
	const col = cssVar(seriesColor(ed.i)), pts = c.curve.slice().sort((a, b) => a[0] - b[0]);
	ctx.beginPath(); ctx.moveTo(g.X(0), g.Y(pts[0][1]));
	for (const p of pts) ctx.lineTo(g.X(clamp(p[0], 0, g.xmax)), g.Y(clamp(p[1], 0, D)));
	ctx.lineTo(g.X(g.xmax), g.Y(pts[pts.length - 1][1]));
	ctx.strokeStyle = col; ctx.lineWidth = C.lineW; ctx.stroke();
	ctx.lineTo(g.X(g.xmax), g.Y(0)); ctx.lineTo(g.X(0), g.Y(0)); ctx.closePath(); ctx.globalAlpha = K.fillAlpha; ctx.fillStyle = col; ctx.fill(); ctx.globalAlpha = 1;
	for (const p of pts) dot(ctx, g.X(clamp(p[0], 0, g.xmax)), g.Y(clamp(p[1], 0, D)), col);
	const live = snap && snap.channels.find(x => x.name === c.name);
	if (live && live.temp > -900) { const x = g.X(clamp(live.temp, 0, g.xmax)), y = g.Y(interp(pts, live.temp)), r = x > g.W / 2;
		ctx.strokeStyle = cssVar('--fg2'); seg(ctx, x, g.pad.t, x, g.H - g.pad.b, C.dash.now);
		dot(ctx, x, y, cssVar('--fg'));
		ctx.fillStyle = cssVar('--fg2'); ctx.textAlign = r ? 'right' : 'left'; ctx.textBaseline = 'bottom'; ctx.fillText(`now ${live.temp.toFixed(1)} °C → ${Math.round(interp(pts, live.temp))}`, x + (r ? -K.labelGap : K.labelGap), y - C.labelGap); }
	syncPts(ed, g);
}
// keyboard: one focusable overlay per point (Tab reaches it, arrows move it by 1 °C / 5 duty, Shift × 5); pointer events fall through to the canvas drag
function syncPts(ed, g) {
	const { c, ptsEl } = ed, L = LIM;
	while (ptsEl.children.length > c.curve.length) ptsEl.lastChild.remove();
	while (ptsEl.children.length < c.curve.length) { const i = ptsEl.children.length; ptsEl.append(h('span', { class: 'pt', tabindex: '0', role: 'slider', onkeydown: ev => keyPt(ed, i, ev) })); }
	c.curve.forEach((p, i) => { const el = ptsEl.children[i]; el.style.left = g.X(clamp(p[0], 0, g.xmax)) + 'px'; el.style.top = g.Y(clamp(p[1], 0, L.duty)) + 'px';
		for (const [k, v] of [['aria-label', `${c.name} point ${i + 1}`], ['aria-valuemin', L.temp[0]], ['aria-valuemax', L.temp[1]], ['aria-valuenow', p[0]], ['aria-valuetext', `${p[0]} °C → duty ${p[1]} (${pct(p[1])} %)`]]) el.setAttribute(k, v); });
}
const keyPt = (ed, i, ev) => { const { c } = ed, d = { ArrowLeft: [-1, 0], ArrowRight: [1, 0], ArrowUp: [0, 1], ArrowDown: [0, -1] }[ev.key]; if (!d) return; ev.preventDefault();
	const m = ev.shiftKey ? K.keyShift : 1, lo = i ? c.curve[i - 1][0] + 1 : LIM.temp[0], hi = i < c.curve.length - 1 ? c.curve[i + 1][0] - 1 : LIM.temp[1];
	c.curve[i] = [clamp(c.curve[i][0] + d[0] * K.keyT * m, lo, hi), clamp(c.curve[i][1] + d[1] * K.keyD * m, 0, LIM.duty)]; dirty(1);
	const row = ed.tbody.rows[i]; if (row) { row.cells[0].firstChild.value = c.curve[i][0]; row.cells[1].firstChild.value = c.curve[i][1]; } ed.draw(); };
function bindCurveDrag(ed) {
	const { cv, c } = ed; let drag = -1;
	const pos = ev => { const r = cv.getBoundingClientRect(); return [ev.clientX - r.left, ev.clientY - r.top]; };
	cv.addEventListener('pointerdown', ev => { const a = document.activeElement; if (ed.tbody.contains(a)) a.blur(); ed.sort(); // sorted neighbours for the clamp
		const g = curveGeom(ed), [x, y] = pos(ev); let best = ev.pointerType === 'touch' ? K.hitRTouch : K.hitR, bi = -1;
		c.curve.forEach((p, i) => { const d = Math.hypot(g.X(p[0]) - x, g.Y(p[1]) - y); if (d < best) { best = d; bi = i; } });
		if (bi >= 0) { drag = bi; cv.setPointerCapture(ev.pointerId); ev.preventDefault(); } });
	cv.addEventListener('pointermove', ev => { if (drag < 0) return; const g = curveGeom(ed), [x, y] = pos(ev);
		const lo = drag ? c.curve[drag - 1][0] + 1 : 0, hi = drag < c.curve.length - 1 ? c.curve[drag + 1][0] - 1 : g.xmax;
		c.curve[drag] = [clamp(Math.round(g.T(x)), lo, hi), clamp(Math.round(g.D(y)), 0, LIM.duty)]; dirty(1);
		const row = ed.tbody.rows[drag]; if (row) { row.cells[0].firstChild.value = c.curve[drag][0]; row.cells[1].firstChild.value = c.curve[drag][1]; } ed.draw(); });
	const up = () => { drag = -1; }; cv.addEventListener('pointerup', up); cv.addEventListener('pointercancel', up);
}
// the daemon's rules (rule 8 would otherwise replace the curve by its default); one channel at a time, shared with the preset editor
// integers only (the daemon decodes int64: 45.5 is a 400, not a rounding); critical ≥ last point + 1; stop "" = auto
const validateChannel = (c, errs) => { const L = LIM, n = c.curve.length, nm = c.name;
	if (n < L.curve_points_min || n > L.curve_points_max) errs.push(`${nm}: ${n} points (need ${L.curve_points_min}..${L.curve_points_max})`);
	c.curve.forEach((p, i) => { const q = `${nm} point ${i + 1}`;
		if (!Number.isInteger(p[0]) || p[0] < L.temp[0] || p[0] > L.temp[1]) errs.push(`${q}: temp ${p[0]} must be a whole number ${L.temp[0]}..${L.temp[1]} °C`);
		if (!Number.isInteger(p[1]) || p[1] < 0 || p[1] > L.duty) errs.push(`${q}: duty ${p[1]} must be a whole number 0..${L.duty}`);
		if (i && p[0] <= c.curve[i - 1][0]) errs.push(`${q}: temp ${p[0]} °C not above point ${i} (${c.curve[i - 1][0]} °C)`);
		if (i && p[1] < c.curve[i - 1][1]) errs.push(`${q}: duty ${p[1]} below point ${i} (${c.curve[i - 1][1]}) — duty must not fall`); });
	const last = n ? c.curve[n - 1][0] : L.temp[0], cmin = last + 1;
	if (!(Number.isInteger(c.critical) && c.critical >= cmin && c.critical <= L.critical_max)) errs.push(`${nm}: critical ${c.critical || '—'} must be a whole number ${cmin}..${L.critical_max} °C (above the last point)`);
	const st = stopN(c.stop); if (st !== 'auto' && !(/^\d+$/.test(st) && +st >= L.min_hdd_override && +st <= L.duty)) errs.push(`${nm}: stop must be "auto" (or empty) or a fixed duty ${L.min_hdd_override}..${L.duty}`);
	if (!(Number.isInteger(c.hysteresis) && c.hysteresis >= 0 && c.hysteresis <= L.hysteresis_max)) errs.push(`${nm}: hysteresis must be 0..${L.hysteresis_max} °C`);
	if (durS(c.min_on) > L.min_on_max_s) errs.push(`${nm}: min_on above ${fmtDur(normDur(L.min_on_max_s + 's'))}`); return errs; };
const validateCurves = (chs = edState) => { const errs = []; for (const c of chs) validateChannel(c, errs); return errs; };
on('#cv-apply', 'click', async () => {
	for (const k in ED) ED[k].sort(); const errs = validateCurves();
	if (errs.length) return cvNotice('Not applied — fix these first:\n' + errs.join('\n'), 'err');
	await loadConfig(); // fresh raw: [dashboard], [alert], [[schedule]] may have changed since the last read
	const body = stripChannels(cfgRaw) + edState.map(tomlChannel).join('\n');
	try { const r = await api('/api/config?strict=1', { method: 'PUT', body, headers: { 'Content-Type': 'application/toml' } });
		const cw = ceilingWarnings(), warn = (r.body && Array.isArray(r.body.warnings) && r.body.warnings.length ? 'Config warnings outside the channel tables:\n' + r.body.warnings.join('\n') + '\n' : '') + cw.join('\n');
		cvNotice((r.status === 202 ? 'Written — restart required (channel set or profile changed): systemctl restart n5-fangov\n' : '') + warn, '');
		toast(r.status === 202 ? 'Curves written — restart required' : warn ? 'Applied with warnings' : 'Curves applied', r.status === 202 || warn ? 'warn' : 'ok'); await loadConfig(); loadEditor(true); loadPresets();
	} catch (e) { cvNotice(e.message, 'err'); } // notice is role=alert: no toast on top
});
on('#cv-revert', 'click', () => { loadEditor(); toast('Reverted', ''); });

// presets: chips under the channel cards (built-ins: badge, no save-over, no delete); active = every channel equals the daemon config,
// the badge on a channel names the first preset whose channel matches (chKey); details are cached per name (PD)
const chKey = chs => JSON.stringify((chs || []).map(c => [c.name, +c.pwm, parts(c.sensor).join(','), (c.curve || []).map(p => [+p[0], +p[1]]), +c.critical, stopN(c.stop), +c.hysteresis || 0, durS(c.min_on)]).sort());
// what Apply makes of a preset here (cmd mergeChannelsByPWM): a preset channel replaces the config channel with the same pwm and takes its name; a table
// without hysteresis and min_on (both 0 — the file omits them) keeps the config channel's post-processing; other config channels stay, new pwms are
// appended. "active" and the channel badge compare against this merge, not the raw preset.
const mergePre = (pc, hc) => Object.assign({}, pc, { name: hc.name }, +pc.hysteresis || durS(pc.min_on) ? null : { hysteresis: hc.hysteresis, min_on: hc.min_on });
const applied = (chs, host) => { const pcs = chs || []; return host.map(hc => { const pc = pcs.find(x => +x.pwm === +hc.pwm); return pc ? mergePre(pc, hc) : hc; }).concat(pcs.filter(pc => !host.some(hc => +hc.pwm === +pc.pwm))); };
const psNotice = notice('#ps-notice'), PD = {}, psGet = name => PD[name] ? Promise.resolve(PD[name]) : api('/api/presets/' + encodeURIComponent(name)).then(r => (PD[name] = r.body));
let psList = [], psSig = '';
const psReset = () => { for (const k in PD) delete PD[k]; psList = []; builtinNames = []; psSig = ''; $('#ps-active').hidden = true; }; // sign-out
const recommended = p => /^recommended/i.test(p.description || '') || p.name === 'n5pro-balanced';
// a channel can belong to several presets (a user preset that shares a channel with a built-in): the badge lists every match, user presets first, in
// the row order; the active set is every preset whose merge equals the whole running config
const userFirst = ps => ps.filter(p => !p.builtin).concat(ps.filter(p => p.builtin));
const presetsOf = c => { const k = chKey([c]); return userFirst(psList.filter(p => { const d = PD[p.name], pc = d && (d.channels || []).find(x => +x.pwm === +c.pwm); return pc && chKey([mergePre(pc, c)]) === k; })).map(p => p.name); };
const activeSets = () => { const host = chList(), k = chKey(host); return userFirst(psList.filter(p => PD[p.name] && chKey(applied(PD[p.name].channels, host)) === k)).map(p => p.name); };
const presetBadges = () => { for (const c of chList()) { const ed = ED[c.name]; if (!ed) continue; const ns = presetsOf(c), b = ed.lv.pre;
	clear(b); b.hidden = false; if (ns.length) { b.className = 'badge preset'; b.title = (ns.length > 1 ? 'the presets these values match:\n' : 'the preset these values match:\n') + ns.join('\n'); b.append(ico('check'), ns.join(' · ')); } else { b.className = 'badge builtin'; b.title = 'no preset matches these values'; b.append('custom'); } }
	const as = activeSets(), el = clear($('#ps-active')); el.hidden = !cfg; el.classList.toggle('on', as.length > 0);
	el.append(h('i', { class: 'dot', 'aria-hidden': 'true' }), 'Active set: ', as.length ? h('b', null, as.join(' · ')) : h('b', null, 'custom'), as.length ? (as.length > 1 ? ' — the daemon runs these values (several presets match)' : ' — the daemon runs exactly these values') : ' (matches no preset)'); };
const nameOk = n => LIM.name.test(n) ? builtinNames.includes(n) ? `${n} is a built-in preset — pick another name` : '' : 'Name: a-z, 0-9, _ and -, at most 64 characters';
const applyPreset = async p => { if (!await ask('Apply preset', `Apply preset ${p.name}? The curves change immediately.` + (edDirty ? '\nUnsaved curve edits are discarded.' : ''), { ok: 'Apply' })) return;
	let r; try { r = await api(`/api/presets/${encodeURIComponent(p.name)}/apply`, { method: 'POST' }); } catch (e) { return toast(e.message, 'err'); }
	const rs = r.status === 202; psNotice(rs ? 'Preset written — restart required: systemctl restart n5-fangov' : '');
	toast(rs ? `Preset ${p.name} written — restart required` : `Preset ${p.name} applied`, rs ? 'warn' : 'ok'); await loadConfig(); loadEditor(); loadPresets(); };
async function loadPresets() {
	const host = $('#preset-row'); try {
		psList = (await api('/api/presets')).body || []; builtinNames = psList.filter(p => p.builtin).map(p => p.name);
		for (const k in PD) delete PD[k]; // every detail is re-read (CLI, second browser, import)
		await Promise.all(psList.map(p => psGet(p.name).catch(() => null)));
		const host_ = chList(), cur = chKey(host_), sig = cur + JSON.stringify([psList, PD]); presetBadges();
		if (sig === psSig && host.childNodes.length) return; psSig = sig; clear(host); // the periodic refresh leaves an unchanged row alone (focus, scroll)
		if (!psList.length) host.append(h('div', { class: 'empty-cta' }, 'No presets yet — New preset… saves the running curves as the first one.'));
		for (const p of psList) { const d = PD[p.name], on = !!d && chKey(applied(d.channels, host_)) === cur;
			host.append(h('div', { class: 'pchip' + (on ? ' active' : '') }, h('i', { class: 'dot', role: 'img', 'aria-label': on ? 'active — the daemon runs these values' : 'not active', title: on ? 'active — the daemon runs these values' : '' }),
				recommended(p) ? h('span', { class: 'star', role: 'img', 'aria-label': 'recommended', title: 'recommended' }, ico('star')) : null,
				h('span', { class: 'pn' }, p.name), p.builtin ? h('span', { class: 'badge builtin' }, 'built-in') : null, p.description ? h('span', { class: 'pd', title: p.description }, p.description) : null,
				h('button', { class: 'btn sm', disabled: on, title: on ? 'already active' : `apply ${p.name}`, onclick: () => applyPreset(p) }, on ? 'active' : 'Apply'),
				h('button', { class: 'btn icon link', 'aria-label': `${p.name}: ${p.builtin ? 'details' : 'edit'}`, title: p.builtin ? 'Details' : 'Edit', onclick: () => openPresetEditor(p.name) }, ico(p.builtin ? 'external' : 'edit')),
				p.builtin ? null : h('button', { class: 'btn icon link danger', 'aria-label': `${p.name}: delete`, title: 'Delete', onclick: async () => { if (!await ask('Delete preset', `Delete preset ${p.name}?`, { ok: 'Delete', danger: true })) return;
					if (await act(() => api('/api/presets/' + encodeURIComponent(p.name), { method: 'DELETE' }), `Preset ${p.name} deleted`)) loadPresets(); } }, ico('trash')))); }
	} catch (e) { psSig = ''; clear(host).append(h('p', { class: 'empty' }, 'presets: ' + e.message)); }
}
on('#ps-new', 'click', () => openPresetEditor(null));
// Save as preset… (action bar, while dirty): the editor opens with Start from = the editor, locked; the curve editor keeps its unsaved changes
on('#cv-saveas', 'click', () => { for (const k in ED) ED[k].sort(); openPresetEditor(null, 'editor'); });
// the Fans page re-reads config and presets every 30 s while current (a preset applied from another browser or the CLI, a preset saved elsewhere):
// a clean editor follows the daemon, a dirty one keeps its edits — the badges and the active set always show what the daemon runs
async function refreshFans() { if (cur !== 'fans' || !signedIn() || !cfg) return; const before = chKey(chList()); await loadConfig();
	if (chKey(chList()) !== before && !edDirty && cur === 'fans') loadEditor(true); if (cur === 'fans') loadPresets(); }
// preset editor dialog (New preset…): "Start from" (the daemon's running curves by default, the editor's unsaved values or any preset), name, one block per channel with the
// curve table; Save = PUT /api/presets/{name} with the composed channels (nothing is applied); built-in presets open read-only
const ped = $('#preset-ed'), peNotice = notice('#pe-notice');
// unsaved edits (peDirty) ask before the dialog goes: Cancel, Escape (keydown, before the dialog's own cancel) and the backdrop share peClose
let peDirty = false;
const peClose = async () => { if (peDirty && !await ask('Discard changes', 'Unsaved preset edits are discarded.', { ok: 'Discard', danger: true })) return; peDirty = false; ped.close(); };
ped.addEventListener('keydown', ev => { if (ev.key === 'Escape') { ev.preventDefault(); peClose(); } });
ped.addEventListener('cancel', ev => { if (peDirty) { ev.preventDefault(); peClose(); } });
ped.addEventListener('click', ev => { if (ev.target === ped) peClose(); });
on('#pe-close', 'click', peClose);
function openPresetEditor(name, from) { // from = 'editor': Save as preset… — Start from is the editor's unsaved values and locked to it
	const body = clear($('#pe-body')), d = name ? PD[name] : null, ro = !!(d && d.builtin), pe = { chans: [] }; peDirty = false;
	$('#pe-title').textContent = name ? (ro ? `Preset ${name}` : `Edit preset ${name}`) : from === 'editor' ? 'Save as preset' : 'New preset';
	const srcs = [['editor', 'the editor (unsaved values)'], ['daemon', 'the daemon (running curves)'], ...psList.map(p => ['p:' + p.name, `preset ${p.name}`])];
	const src = h('select', { 'aria-label': 'start from', disabled: !!name || !!from, onchange: () => fill(src.value) });
	for (const [v, t] of srcs) src.append(h('option', { value: v, selected: v === (name ? 'p:' + name : from || 'daemon') }, t));
	const nm = h('input', { type: 'text', value: name || '', placeholder: 'summer', maxlength: 64, readonly: ro, autocapitalize: 'off', spellcheck: 'false', 'aria-describedby': 'pe-hint', oninput: () => { peDirty = true; } });
	const chBox = h('div', { class: 'pe-chs', oninput: () => { peDirty = true; }, onclick: ev => { if (ev.target.closest('button')) peDirty = true; } }), noticeEl = h('div', { class: 'notice err', id: 'pe-notice', role: 'alert', hidden: true });
	body.append(h('div', { class: 'src' }, 'Start from ', src, h('span', { class: 'hint sm' }, from === 'editor' ? 'from the edited curves — saving stores them as a preset; the editor keeps its unsaved changes.' : 'Saving stores the values below — nothing is applied to the daemon.')),
		h('div', { class: 'frow' }, h('label', null, 'Name ', nm), h('span', { class: 'hint sm', id: 'pe-hint' }, ro ? 'built-in presets are read-only' : 'a–z, 0–9, _ and -, at most 64 characters')),
		chBox, noticeEl,
		h('div', { class: 'act' }, h('button', { class: 'btn', onclick: peClose }, ro ? 'Close' : 'Cancel'), ro ? null : h('button', { class: 'btn primary', onclick: save }, name ? 'Save changes' : 'Save preset')));
	const fill = v => { const from = v === 'editor' ? edState || fromCfg() : v === 'daemon' ? chList() : (PD[v.slice(2)] || {}).channels || [];
		pe.chans = from.map(chCopy); clear(chBox); peNotice(''); for (const c of pe.chans) chBox.append(peChannel(c, ro)); };
	// Save: PUT under the current name first, the rename last — either failure leaves a consistent preset; a new preset never overwrites one unasked
	async function save() { const n = nm.value.trim(), bad = nameOk(n); const errs = bad ? [bad] : validateCurves(pe.chans);
		if (errs.length) { peNotice('Not saved — fix these first:\n' + errs.join('\n'), 'err'); if (bad) nm.focus(); return; }
		if (!name && psList.some(p => p.name === n) && !await ask('Overwrite preset', `Preset ${n} exists — replace it with these values?`, { ok: 'Overwrite', danger: true })) return;
		const chans = pe.chans.map(c => ({ name: c.name, pwm: c.pwm, sensor: c.sensor, curve: c.curve, critical: c.critical, stop: stopN(c.stop), hysteresis: c.hysteresis, min_on: c.min_on })), old = name;
		try { await api('/api/presets/' + encodeURIComponent(name || n), { method: 'PUT', json: { channels: chans } }); delete PD[name || n];
			if (name && n !== name) { await api(`/api/presets/${encodeURIComponent(name)}/rename`, { method: 'POST', json: { name: n } }); name = n; delete PD[n]; $('#pe-title').textContent = `Edit preset ${n}`; }
		} catch (e) { loadPresets(); return peNotice(e.message, 'err'); }
		peDirty = false; ped.close(); loadPresets();
		if (from === 'editor' && edDirty) return toast(`Preset ${n} saved — the editor still has unsaved changes; Apply to daemon writes them`, 'warn', UI.timing.toastNotice);
		toast(old ? (n !== old ? `Preset ${old} renamed to ${n} and saved` : `Preset ${n} saved`) : `Preset ${n} saved`, 'ok'); }
	fill(src.value); ped.showModal(); (ro ? $('.act .btn', body) : nm).focus();
}
// one channel block of the preset editor: fields + an editable point table (same rules as the curve editor)
const peChannel = (c, ro) => { const L = LIM, tbody = h('tbody');
	const fillTable = () => { clear(tbody); c.curve.forEach((p, j) => tbody.append(h('tr', null,
		h('td', null, h('input', { type: 'number', min: L.temp[0], max: L.temp[1], value: p[0], readonly: ro, 'aria-label': `${c.name} point ${j + 1} temp`, oninput: ev => { p[0] = +ev.target.value; } })),
		h('td', null, h('input', { type: 'number', min: 0, max: L.duty, value: p[1], readonly: ro, 'aria-label': `${c.name} point ${j + 1} duty`, oninput: ev => { p[1] = clamp(+ev.target.value, 0, L.duty); ev.target.closest('tr').cells[2].textContent = `${pct(p[1])} %`; } })),
		h('td', { class: 'hint sm' }, `${pct(p[1])} %`),
		h('td', null, ro ? null : h('button', { class: 'btn sm link', disabled: c.curve.length <= L.curve_points_min, 'aria-label': `${c.name}: remove point ${j + 1}`, title: 'remove point', onclick: () => { c.curve.splice(j, 1); fillTable(); } }, ico('trash'))))));
		add.disabled = ro || c.curve.length >= L.curve_points_max; };
	const add = h('button', { class: 'btn sm', onclick: () => { c.curve.push(gapPoint(c.curve)); c.curve.sort((a, b) => a[0] - b[0]); fillTable(); tbody.rows[tbody.rows.length - 1].cells[0].firstChild.focus(); } }, ico('plus'), 'add point');
	tbody.addEventListener('focusout', ev => { if (!tbody.contains(ev.relatedTarget)) { const before = c.curve.slice(); c.curve.sort((a, b) => a[0] - b[0]); if (before.some((x, k) => x !== c.curve[k])) fillTable(); } });
	const inp = (key, attrs) => h('input', Object.assign({ value: c[key], readonly: ro, oninput: ev => { c[key] = attrs.type === 'number' ? +ev.target.value : ev.target.value.trim(); } }, attrs));
	fillTable();
	return h('div', { class: 'pe-ch' }, h('div', { class: 'fh' }, h('span', { class: 'name' }, c.name), h('span', { class: 'subt' }, `pwm${c.pwm} · ${c.sensor}`)),
		h('div', { class: 'fields' }, h('label', { title: TIP.critical }, 'critical °C', inp('critical', { type: 'number', min: L.critical_min, max: L.critical_max })), h('label', { title: TIP.stop }, 'stop', inp('stop', { type: 'text', placeholder: 'auto' })),
			h('label', { title: TIP.hyst }, 'hysteresis °C', inp('hysteresis', { type: 'number', min: 0, max: L.hysteresis_max, step: 1 })), h('label', { title: TIP.minOn }, 'min on', Object.assign(minOnSel(c, () => {}), { disabled: ro }))),
		h('div', { class: 'ptl2' }, h('table', null, h('thead', null, h('tr', null, h('th', null, '°C'), h('th', { title: TIP.duty }, 'duty'), h('th'), h('th'))), tbody), ro ? null : add)); };

// Schedules page: editor + status. Save splices the [[schedule]] tables like Apply splices [[channel]] (stripSchedules mirrors stripChannels), PUT ?strict=1; no restart (DESIGN 6b)
const SC_HDR = /^\[\[\s*schedule\s*\]\]/, SC_DAYS = ['mon', 'tue', 'wed', 'thu', 'fri', 'sat', 'sun'];
const stripSchedules = raw => { const out = []; let skip = false;
	for (const ln of raw.split('\n')) { const t = ln.trim();
		if (SC_HDR.test(t)) { skip = true; continue; }
		if (TOML_HDR.test(t)) skip = false;
		if (!skip) out.push(ln); } return out.join('\n').replace(/\n{3,}/g, '\n\n').trimEnd() + '\n\n'; };
const tomlSchedule = e => `[[schedule]]\npreset = "${e.preset}"\n` + (e.fallback ? '' : `from = "${e.from}"\nto = "${e.to}"\n` + (e.days.length && e.days.length < 7 ? `days = [${SC_DAYS.filter(d => e.days.includes(d)).map(d => `"${d}"`).join(', ')}]\n` : ''));
const scNotice = notice('#sc-notice'), scErr = notice('#sc-err'), inRel = ts => 'in ' + elapsed(Math.max(0, ts - Date.now() / 1000 | 0));
let scState = null, scDirty = false, scStat = null, scNames = [], scStash = null;
const scSetDirty = v => { scDirty = !!v; $('#sc-save').disabled = !v; $('#sc-dirty').hidden = !v; };
// page-leave guard (from go): true = the switch waits for the dialog
function scGuard(id, sec) { if (cur !== 'schedules' || id === 'schedules' || !scDirty) return false;
	ask('Leave Schedules', 'Unsaved schedule changes are discarded.', { ok: 'Discard', danger: true }).then(ok => { if (ok) { scSetDirty(false); go(id, sec); } else if (location.hash !== lastHash) location.hash = lastHash; }); return true; }
function scReset() { scState = scStat = null; scSetDirty(false); scErr(''); scNotice(''); clear($('#sc-tbl tbody')); clear($('#sc-kv')); }
const scValidate = () => { const errs = [], hhmm = /^\d{2}:\d{2}$/; let fb = 0;
	scState.forEach((e, i) => { const q = `entry ${i + 1}: `;
		if (!e.preset) errs.push(q + 'choose a preset');
		if (e.fallback) { if (++fb > 1) errs.push(q + 'only one fallback allowed'); }
		else if (!hhmm.test(e.from) || !hhmm.test(e.to)) errs.push(q + 'from and to need a time (HH:MM)');
		else if (e.from === e.to) errs.push(q + 'from equals to'); });
	if (scState.length > 16) errs.push('at most 16 entries'); return errs; };
// day toggles update in place (aria-pressed, "every day" hint) so the focus stays
const scDays = e => { const all = h('span', { class: 'all' }, 'every day'), sync = () => { bt.forEach((b, i) => b.setAttribute('aria-pressed', String(e.days.includes(SC_DAYS[i])))); all.hidden = !!e.days.length; };
	const bt = SC_DAYS.map(d => h('button', { 'aria-label': d, onclick: () => { e.days = e.days.includes(d) ? e.days.filter(x => x !== d) : e.days.concat(d); if (e.days.length === 7) e.days = []; sync(); scSetDirty(1); } }, d[0].toUpperCase() + d[1]));
	sync(); return h('div', { class: 'days', role: 'group', 'aria-label': 'days' }, bt, all); };
function scBuild(focus) { // focus: [row, selector] after a structural change
	const tb = clear($('#sc-tbl tbody')), es = scState || [], hasFb = es.some(e => e.fallback), empty = t => tb.append(h('tr', null, h('td', { colspan: 5, class: 'empty' }, t))), add = $('#sc-add');
	add.disabled = !scStat || !scNames.length; add.title = scStat && !scNames.length ? 'create a preset first (Fans page → New preset…)' : '';
	if (!scStat) return empty('No scheduler in this daemon — [[schedule]] tables are not applied.');
	if (!es.length) empty(scNames.length ? 'No schedule — the daemon keeps the curves it has. Add an entry to switch presets by time of day.' : 'No schedule — and no presets to switch between yet: create one on the Fans page first (New preset…).');
	es.forEach((e, i) => { const known = () => !e.preset || scNames.includes(e.preset), miss = h('span', { class: 'miss', hidden: known() }, 'preset missing'); // a fresh row without a choice is not "missing"
		const sel = h('select', { 'aria-label': `entry ${i + 1} preset`, onchange: () => { e.preset = sel.value; miss.hidden = known(); scSetDirty(1); } },
			!e.preset ? h('option', { value: '', selected: true }, '— choose —') : known() ? null : h('option', { value: e.preset, selected: true }, `${e.preset} (missing)`), scNames.map(n => h('option', { value: n, selected: n === e.preset }, n)));
		const time = k => h('input', { type: 'time', value: e[k], 'aria-label': `entry ${i + 1} ${k}`, required: true, oninput: ev => { e[k] = ev.target.value; scSetDirty(1); } });
		const swap = h('button', { class: 'btn sm link', disabled: !e.fallback && hasFb, title: !e.fallback && hasFb ? 'there is already a fallback entry' : null,
			onclick: () => { e.fallback = !e.fallback; if (e.fallback) { e.from = e.to = ''; e.days = []; } scSetDirty(1); scBuild([i, '.sub button']); } }, e.fallback ? 'add window' : 'make fallback');
		tb.append(h('tr', { class: e.active ? 'active' : '' }, h('td', null, sel, h('div', { class: 'sub' }, e.active ? h('span', { class: 'act' }, 'ACTIVE') : null, miss, swap)),
			e.fallback ? h('td', { colspan: 3, class: 'fb' }, 'fallback — outside every window') : [h('td', null, time('from')), h('td', null, time('to')), h('td', null, scDays(e))],
			h('td', { class: 'rowact' }, h('button', { class: 'btn icon link', 'aria-label': `remove entry ${i + 1}`, onclick: () => { scState.splice(i, 1); scSetDirty(1); scBuild([Math.min(i, scState.length - 1), '.rowact button']); } }, ico('trash'))))); });
	if (focus) { const r = tb.rows[focus[0]]; ((r && r.querySelector(focus[1])) || $('#sc-add')).focus(); }
}
// the daemon's clock: /api/state carries the snapshot's unix ts (up to one cycle old), so the largest ts − browser-now seen is the skew; the offset of the
// host zone comes from the schedules' timezone ("CEST +02:00"); ticks every second while the page is current, "browser time" before the first snapshot
let clkOff = null;
const scTick = () => { const el = $('#sc-now'); if (!el || cur !== 'schedules') return; const tz = scStat && scStat.timezone || '', m = /([+-])(\d\d):(\d\d)$/.exec(tz), s = Date.now() / 1000 + (clkOff || 0);
	el.textContent = (m ? new Date((s + (m[1] === '-' ? -1 : 1) * (m[2] * 3600 + m[3] * 60)) * 1000).toISOString().slice(11, 19) : new Date(s * 1000).toTimeString().slice(0, 8)) + ' · ' + (clkOff === null ? 'browser time' : tz || '—'); };
function renderScStatus() { const s = scStat; if (!s) return; const es = s.entries || [], a = es[s.active], n = s.next, l = s.last;
	kv($('#sc-kv'), [['now', h('dd', { id: 'sc-now', title: 'the daemon\'s local time — windows are compared against this clock' })],
		['active', a ? a.fallback ? `${a.preset} (fallback)` : `${a.preset} ${a.from}–${a.to}${a.days && a.days.length ? ' · ' + a.days.join(' ') : ''}` : es.length ? 'none (outside every window)' : 'none'],
		['next switch', n ? h('dd', null, h('time', { datetime: new Date(n.ts * 1000).toISOString() }, abs(n.ts)), ` (${inRel(n.ts)}) → ${n.preset || 'no preset (no fallback)'}`) : es.length ? 'none within 8 days' : '—'],
		['last switch', l ? h('dd', null, `${l.preset} · `, tm(l.ts), l.ok ? ' · ok' : h('span', { class: 't-crit' }, ` — failed: ${l.error || 'unknown error'}`)) : 'none yet']]); scTick();
	scNotice(l && !l.ok ? `Last switch failed: ${l.error || 'unknown error'} — the previous curves stay; retried at the next transition.` : '', ''); }
async function loadSchedules() { if (!signedIn()) return; // the 60 s poll never rebuilds a dirty editor
	try { const [s, ps] = await Promise.all([api('/api/schedules'), api('/api/presets').catch(() => ({ body: [] }))]);
		scStat = s.body || {}; scNames = (ps.body || []).map(p => p.name); renderScStatus();
		if (!scDirty) { scState = (scStat.entries || []).map(e => ({ preset: e.preset || '', from: e.from || '', to: e.to || '', days: (e.days || []).filter(d => SC_DAYS.includes(d)), fallback: !!e.fallback, active: !!e.active })); scBuild(); }
		else if (!$('#sc-tbl tbody').rows.length) scBuild(); // scStash restored after a sign-in: the table is still empty
	} catch (e) { if (e.status === 401) return; scStat = scState = null; scSetDirty(false); kv($('#sc-kv'), [['scheduler', e.status === 501 ? 'unavailable (501)' : e.message]]); scNotice(''); scBuild(); } }
on('#sc-add', 'click', () => { if (!scState) return; scState.push({ preset: scNames[0] || '', from: '', to: '', days: [], fallback: false }); scSetDirty(1); scBuild([scState.length - 1, 'select']); });
on('#sc-revert', 'click', () => { scSetDirty(false); scErr(''); loadSchedules(); toast('Reverted', ''); });
on('#sc-save', 'click', async () => { if (!scState) return; const errs = scValidate();
	if (errs.length) return scErr('Not saved — fix these first:\n' + errs.join('\n'), 'err');
	await loadConfig(); // splice into the file as it is now
	const body = stripSchedules(cfgRaw) + scState.map(tomlSchedule).join('\n');
	try { const r = await api('/api/config?strict=1', { method: 'PUT', body, headers: { 'Content-Type': 'application/toml' } });
		const warn = r.body && Array.isArray(r.body.warnings) && r.body.warnings.length ? 'Config warnings:\n' + r.body.warnings.join('\n') : '';
		scErr((r.status === 202 ? 'Written — restart required: systemctl restart n5-fangov\n' : '') + warn, '');
		toast(r.status === 202 ? 'Schedule written — restart required' : warn ? 'Schedule saved with warnings' : 'Schedule saved', r.status === 202 || warn ? 'warn' : 'ok');
		scSetDirty(false); await loadConfig(); loadSchedules();
	} catch (e) { scErr(e.message, 'err'); } }); // notice is role=alert: no toast on top

// alerts page + Settings → Alert transport; unsaved form edits survive the refresh; missing transports are disabled
let alDirty = false; on('#al-form', 'input', () => { alDirty = true; });
// mail_to for auto/mail, webhook_url + webhook_format for webhook
const transVis = () => { const t = $('#al-transport').value; $('#al-mailto-l').hidden = !/^(auto|mail)$/.test(t); $('#al-wh-l').hidden = $('#al-whf-l').hidden = t !== 'webhook'; };
on('#al-transport', 'change', transVis);
function renderAlertsTab() {
	const a = alerts; if (!a) return; const t = a.template || {}, sel = $('#al-transport');
	for (const o of sel.options) { const av = o.value === 'pve' ? a.pve_available : o.value === 'mail' ? a.mail_available : true; o.disabled = !av; o.textContent = o.value + (av ? '' : ' (not available)'); }
	if (!alDirty) { sel.value = a.transport || 'auto'; $('#al-mailto').value = a.mail_to || ''; $('#al-wh').value = a.webhook_url || ''; $('#al-whf').value = a.webhook_format || 'json'; } transVis();
	const av = x => x ? 'available' : 'not available'; kv($('#al-status'), [['effective', a.effective || '—'], ['pve-notify', av(a.pve_available)], ['mail(1)', av(a.mail_available) + (a.mail_available ? '' : ' — apt install bsd-mailx or choose pve/log')],
		['webhook', a.webhook_url ? h('dd', { class: 'mono' }, `${a.webhook_url} (${a.webhook_format || 'json'})`) : 'no URL configured'], ['cooldown', fmtDur(a.cooldown)]]);
	$('#al-tpl-card').hidden = !a.pve_available;
	const yn = x => x ? 'yes' : 'no'; kv($('#al-tpl'), [['installed', yn(t.installed)], ['current', !t.installed ? '—' : t.current ? 'yes' : 'no — outdated'], ['writable', yn(t.writable)], ['path', h('dd', { class: 'mono' }, t.path || '—')]]);
	const b = $('#al-tpl-btn'), upToDate = t.installed && t.current; b.textContent = t.installed ? 'Update template' : 'Install template'; b.disabled = !t.writable || upToDate;
	$('#al-tpl-reason').textContent = !t.writable ? (t.reason || 'not writable') : upToDate ? 'up to date' : '';
	const tb = clear($('#al-kinds tbody')), last = a.last || {};
	for (const k of a.kinds || []) tb.append(h('tr', null, h('td', { class: 'mono' }, k.kind), h('td', null, k.description || ''), h('td', null, tm(last[k.kind] || 0))));
	if (!(a.kinds || []).length) tb.append(h('tr', null, h('td', { colspan: 3, class: 'empty' }, 'no alert kinds reported')));
	alertList($('#al-recent'), a.recent || []);
}
on('#al-form', 'submit', async ev => { ev.preventDefault(); const t = $('#al-transport').value, j = { transport: t };
	if (/^(auto|mail)$/.test(t)) j.mail_to = $('#al-mailto').value.trim();
	if (t === 'webhook') { j.webhook_url = $('#al-wh').value.trim(); j.webhook_format = $('#al-whf').value; if (!/^https?:\/\/\S+$/.test(j.webhook_url)) { toast('Webhook: an absolute http(s) URL is required', 'err'); return $('#al-wh').focus(); } }
	const r = await act(() => api('/api/alerts', { method: 'PUT', json: j })); if (!r) return;
	alDirty = false; toast('Transport saved — effective: ' + (r.body.status && r.body.status.effective), 'ok'); loadAlerts(); loadConfig(); }); // [alert] changed in the file: cfgRaw follows
const sendTest = async () => { // Alerts page and Settings → Alert transport share the button
	try { const r = await api('/api/alerts/test', { method: 'POST' }); toast('Test alert sent via ' + r.body.transport, 'ok'); }
	catch (e) { if (e.status === 409) return toast('Test alert already running', 'warn'); toast('Test alert failed' + (e.body && e.body.transport ? ` (${e.body.transport})` : '') + ': ' + e.message, 'err'); }
	loadAlerts(); };
on('#al-test', 'click', sendTest); on('#al-test2', 'click', sendTest);
on('#al-tpl-btn', 'click', async () => { const r = await act(() => api('/api/alerts/template', { method: 'POST' })); if (r) { toast('Template written to ' + r.body.path, 'ok'); loadAlerts(); } });

// about (public; `go` is empty for anonymous readers, so the page is re-read on every sign-in / sign-out)
const loadAbout = () => api('/api/about').then(r => renderAbout(r.body || {})).catch(() => {});
function renderAbout(a) {
	$('#ab-name').textContent = a.name || 'n5-fangov'; $('#ab-version').textContent = 'v' + (a.version || version || '?').replace(/^v/, ''); preBadge([$('#ab-beta')], a.prerelease);
	const link = (id, href, text) => { const e = $(id); e.href = href || '#'; e.textContent = text || href || '—'; };
	link('#ab-license', a.license_url, a.license); link('#ab-repo', a.repo, (a.repo || '').replace(/^https?:\/\//, '')); link('#ab-author', a.author_url, a.author); link('#ab-rel', a.repo + '/releases', 'GitHub releases');
	$('#ab-go').textContent = a.go || '—'; $('#ab-go').hidden = $('#ab-go-t').hidden = !a.go; $('#ab-mock').hidden = !MOCK;
	const ul = clear($('#ab-credits')); for (const c of a.credits || []) ul.append(h('li', null, h('a', { href: c.url, rel: 'noopener', target: '_blank' }, c.name), c.note ? ' — ' + c.note : ''));
}

// log: last N lines, re-read every 10 s while the page is open
let logLines = [], logSrc = '';
const logLine = l => typeof l === 'string' ? l : JSON.stringify(l);
async function loadLog() {
	try { const r = await api('/api/log?lines=' + LIM.logLines); const b = r.body;
		logLines = (b && b.lines || []).map(logLine); logSrc = b && b.source || ''; renderLog();
		$('#lg-clear').disabled = logSrc === 'journal';
		$('#lg-clear').title = logSrc === 'journal' ? 'Log file disabled — the journal cannot be cleared' : 'Truncate the log file (rotated files and journal untouched)';
	} catch (e) { $('#log').textContent = 'log: ' + e.message; }
}
on('#lg-export', 'click', () => act(() => download('/api/log/export', 'n5-fangov.log'), n => `Log exported as ${n}`));
on('#lg-clear', 'click', async () => {
	if (!await ask('Clear log', 'Clear the current log file? Rotated files and the systemd journal are untouched.', { ok: 'Clear', danger: true })) return;
	const r = await act(() => api('/api/log', { method: 'DELETE' })); if (!r) return;
	toast('Log cleared — ' + (r.body && r.body.note || 'journal untouched'), 'ok'); loadLog();
});
function renderLog() {
	const q = $('#lg-filter').value.toLowerCase(), pre = clear($('#log')); let n = 0;
	for (const l of logLines) { if (q && !l.toLowerCase().includes(q)) continue; n++;
		pre.append(h('span', { class: /error|fail/i.test(l) ? 'e' : /warn/i.test(l) ? 'w' : '' }, l + '\n')); }
	$('#lg-meta').textContent = `last ${LIM.logLines} lines` + (logSrc ? ` · source: ${logSrc}` : '') + (q ? ` · ${n} of ${logLines.length} match` : '');
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
// raw tier: one point per daemon cycle, so the cap follows [daemon] interval (2..30 s → 3600..240 points) plus a margin for jitter; anonymous (no config): the window only
const maxPts = rg => { if (rg.maxPoints) return rg.maxPoints; const iv = cfg && cfg.daemon ? durS(cfg.daemon.interval) : 0; return iv > 0 ? Math.ceil(rg.windowS / iv) + 12 : Infinity; };
// 2 h: incremental (since); 24 h / 7 d: the averaged tier is reloaded whole (bucket means change until the bucket closes)
async function loadHistory() {
	const g = histGen, rg = R(), inc = !!(rg.since && lastTs);
	try { const r = await api(`/api/history?minutes=${rg.minutes}` + (inc ? '&since=' + lastTs : '')); if (g !== histGen) return;
		const pts = (r.body || []).filter(p => !inc || p.ts > lastTs).sort((a, b) => a.ts - b.ts);
		if (!inc) hist = [];
		if (pts.length) { hist = hist.concat(pts); lastTs = hist[hist.length - 1].ts; }
		const cut = Date.now() / 1000 - rg.windowS, cap = maxPts(rg); while (hist.length && (hist[0].ts < cut || hist.length > cap)) hist.shift();
		renderCharts(); drawSparks();
	} catch (e) {}
}
const resetHistory = () => { hist = []; lastTs = 0; histGen++; loadHistory(); };
let timers = [], polling = false, repoll = false;
async function poll() { // a call mid-poll queues one more round
	if (polling) { repoll = true; return; } polling = true;
	try { const r = await api('/api/state'), first = !snap; snap = r.body; if (snap.ts > 0) { const o = snap.ts - Date.now() / 1000; if (clkOff === null || o > clkOff || o < clkOff - 60) clkOff = o; } renderHeader(); renderCards(); renderLive(); renderSystem();
		if (cur === 'fans') drawEds(); if (first) renderCharts(); } // the history may precede the first snapshot
	catch (e) {}
	await pollSensors();
	polling = false; if (repoll) { repoll = false; poll(); }
}
const liveState = () => { const l = $('#h-live'); const paused = document.hidden; l.classList.toggle('paused', paused); l.lastChild.textContent = paused ? 'paused' : 'live'; };
function schedule() {
	for (const t of timers) clearInterval(t); timers = []; liveState();
	if (document.hidden) return;
	poll(); loadHistory(); const T = UI.timing;
	timers = [setInterval(poll, S.interval * 1000), setInterval(loadHistory, R().poll),
		setInterval(() => { if (cur === 'overview' || cur === 'alerts') loadAlerts(); }, T.alerts),
		setInterval(() => { if (cur === 'schedules') loadSchedules(); }, T.schedules), setInterval(refreshFans, T.fans),
		setInterval(() => { if (cur === 'overview' || cur === 'system') loadSystem(); }, T.system), // live parts: memory, load, link state
		setInterval(() => { if (cur === 'log') loadLog(); }, T.log), setInterval(scTick, 1000)];
}
document.addEventListener('visibilitychange', schedule);

// settings page: Display (persisted in localStorage), then the sections the former dialogs held; sub-navigation marks the section in view
// a click pins its section for a moment: the last sections cannot reach the top of the viewport, the observer would mark the one above
let subLock = 0;
const subVis = new Set(), subIO = new IntersectionObserver(es => { for (const e of es) e.isIntersecting ? subVis.add(e.target.id) : subVis.delete(e.target.id);
	if (Date.now() < subLock || cur !== 'settings') return;
	const list = $$('#st-list .st').filter(x => !x.hidden), atEnd = innerHeight + scrollY >= document.documentElement.scrollHeight - 2, first = atEnd ? list[list.length - 1] : list.find(x => subVis.has(x.id));
	if (first) markSub(first.id); }, { rootMargin: '-64px 0px -60% 0px' });
const markSub = (id, pin) => { if (pin) subLock = Date.now() + UI.timing.subLock; for (const b of $$('#subnav button')) b.dataset.sec === id ? b.setAttribute('aria-current', 'true') : b.removeAttribute('aria-current'); };
function buildSubnav() {
	const sn = clear($('#subnav')); subIO.disconnect(); subVis.clear();
	for (const st of $$('#st-list .st')) { if (st.hidden) continue; subIO.observe(st);
		sn.append(h('button', { 'data-sec': st.id, onclick: () => { st.scrollIntoView({ block: 'start' }); markSub(st.id, true); lastHash = '#settings/' + st.id; history.replaceState(null, '', lastHash); } }, st.classList.contains('danger') ? ico('warn') : null, $('h2', st).textContent)); }
	// initial marker from the scroll position (re-entered scrolled): the last section whose top passed the header
	const list = $$('#st-list .st').filter(x => !x.hidden), top = ph.offsetHeight + 16; let at = list[0]; for (const s of list) if (s.getBoundingClientRect().top <= top) at = s;
	if (at) markSub(at.id);
}
// entering the page: account + sessions, tokens (session callers only), certificate, alert transport are re-read
function renderSettings() {
	acNotice(''); acReset(); $('#ac-user').textContent = sess.user || ''; for (const u of $$('.ac-un')) u.defaultValue = sess.user || ''; // hidden username fields of the password forms
	const tok = /^(cookie|basic)$/.test(sess.via || ''); $('#st-tokens').hidden = $('#ac-tokens').hidden = !tok;
	buildSubnav(); loadAccount(); openCert();
}
$('#s-unit').value = S.unit; $('#s-interval').value = String(S.interval); $('#s-theme').value = S.theme; $('#s-nav').value = S.nav;
on('#s-nav', 'change', ev => { S.nav = ev.target.value; saveS(); buildNav(); });
on('#s-unit', 'change', ev => { S.unit = ev.target.value; saveS(); if (snap) { renderCards(); renderCharts(); renderSensors(); fillSensorSelects(); redrawAll(); } });
on('#s-interval', 'change', ev => { S.interval = +ev.target.value; saveS(); schedule(); });
on('#s-theme', 'change', ev => { S.theme = ev.target.value; saveS(); document.documentElement.dataset.theme = S.theme; redrawAll(); });
matchMedia('(prefers-color-scheme: light)').addEventListener('change', () => { if (S.theme === 'system') redrawAll(); });
document.documentElement.dataset.theme = S.theme;
// settings bundle
const gNotice = notice('#g-notice');
on('#s-export', 'click', () => { act(() => download('/api/config/export', 'n5-fangov-settings.json'), n => `Settings exported as ${n}`); });
on('#s-import', 'click', () => $('#s-file').click());
on('#s-file', 'change', async () => {
	const inp = $('#s-file'), f = inp.files[0]; inp.value = ''; if (!f) return;
	if (f.size > LIM.importBytes) return toast('Settings file exceeds 1 MiB', 'err');
	const text = await f.text(); let j = null; try { j = JSON.parse(text); } catch (e) {}
	if (!j || typeof j !== 'object' || Array.isArray(j)) return toast(`${f.name}: not a JSON settings bundle`, 'err');
	if (!await ask('Import settings', `Import settings from ${f.name}?\nDaemon config and presets are replaced (after validation).`, { ok: 'Import', danger: true })) return;
	try { const r = await api('/api/config/import', { method: 'POST', body: text, headers: { 'Content-Type': 'application/json' } });
		if (r.status === 202) { gNotice('Settings imported — restart required: systemctl restart n5-fangov'); toast('Imported — restart required', 'warn', UI.timing.toastErr); }
		else { toast('Settings imported', 'ok'); }
		await loadConfig(); edState = null; dirty(false); if (cur === 'fans') { loadEditor(); loadPresets(); } poll();
	} catch (e) { toast(e.message, 'err', UI.timing.toastLong); }
});

// account & sessions, API tokens (Settings page)
const acNotice = notice('#ac-notice');
const acReset = () => { for (const f of ['#ac-pw-f', '#ac-us-f', '#ac-tk-f']) { $(f).reset(); $(f).hidden = true; } $('#ac-tk-new').hidden = true; $('#ac-tk-secret').value = ''; $('#ac-tk-never').hidden = true; };
async function loadAccount() {
	try { const a = (await api('/api/account')).body; $('#ac-user').textContent = a.user || sess.user || ''; const tb = clear($('#ac-sessions tbody')), ss = a.sessions || [];
		for (const s of ss) tb.append(h('tr', { class: s.current ? 'active' : '' }, h('td', { class: 'mono' }, s.id, s.current ? h('span', { class: 'act' }, 'THIS SESSION') : null),
			h('td', null, tm(s.created)), h('td', null, tm(s.last_seen)), h('td', null, tm(s.expires, true), s.remember ? ' · remembered' : null), h('td', { class: 'mono' }, s.ip || '')));
			} catch (e) { acNotice('account: ' + e.message, 'err'); }
	if (!$('#ac-tokens').hidden) loadTokens();
}
// API tokens: session callers only (a token can never manage tokens); the secret is shown once, right after creation
async function loadTokens() { const tb = clear($('#ac-tok tbody'));
	try { const ts = (await api('/api/tokens')).body.tokens || [];
		for (const t of ts) tb.append(h('tr', { class: t.expired ? 'dim' : '' }, h('td', null, t.name, h('span', { class: 'tid mono' }, t.id)), h('td', { class: 'mono' }, t.scope), h('td', null, tm(t.created)),
			h('td', null, t.expires ? h('span', { class: t.expired ? 'expired' : '' }, tm(t.expires, true), t.expired ? ' · expired' : null) : 'never'), h('td', null, t.last_used ? tm(t.last_used) : 'never'), h('td', { class: 'mono' }, t.last_ip || '—'),
			h('td', null, h('button', { class: 'btn sm danger', onclick: async () => { if (!await ask('Revoke token', `Revoke token ${t.name}? Every client using it stops working at once.`, { ok: 'Revoke', danger: true })) return;
				if (await act(() => api('/api/tokens/' + encodeURIComponent(t.id), { method: 'DELETE' }), `Token ${t.name} revoked`)) loadTokens(); } }, 'Revoke'))));
		if (!ts.length) tb.append(h('tr', null, h('td', { colspan: 7, class: 'empty' }, 'no tokens')));
	} catch (e) { tb.append(h('tr', null, h('td', { colspan: 7, class: 'empty' }, e.status === 501 ? 'tokens: unavailable' : 'tokens: ' + e.message))); } }
on('#ac-tk', 'click', () => { acReset(); $('#ac-tk-f').hidden = false; $('#ac-tk-name').focus(); });
on('#ac-tk-ttl', 'change', () => { $('#ac-tk-never').hidden = $('#ac-tk-ttl').value !== '0'; });
on('#ac-tk-f', 'submit', async ev => { ev.preventDefault(); const name = $('#ac-tk-name').value.trim(), ttl = +$('#ac-tk-ttl').value;
	if (!LIM.token.test(name)) return acNotice('token name: letters, digits, space, . _ - (1–32), not starting with punctuation', 'err');
	try { const r = await api('/api/tokens', { method: 'POST', json: { name, scope: $('#ac-tk-scope').value, ttl_days: ttl } }); acReset(); acNotice('');
		$('#ac-tk-nn').textContent = r.body.name || name; $('#ac-tk-secret').value = r.body.token || ''; $('#ac-tk-new').hidden = false; $('#ac-tk-warn').hidden = !r.body.warning; $('#ac-tk-warn').textContent = r.body.warning ? r.body.warning + ' — revoke it here when the client is retired.' : '';
		toast(`Token ${name} created`, 'ok'); $('#ac-tk-copy').focus(); loadTokens(); }
	catch (e) { acNotice(e.message, 'err'); $('#ac-tk-name').focus(); } });
on('#ac-tk-copy', 'click', () => { const inp = $('#ac-tk-secret'); const fb = () => { inp.focus(); inp.select(); toast('Clipboard blocked — the secret is selected, press Ctrl+C', 'warn'); };
	navigator.clipboard ? navigator.clipboard.writeText(inp.value).then(() => toast('Token copied', 'ok'), fb) : fb(); });
on('#ac-tk-done', 'click', () => { $('#ac-tk-new').hidden = true; $('#ac-tk-secret').value = ''; $('#ac-tk').focus(); });
$$('#st-account [data-cancel], #st-tokens [data-cancel]').forEach(b => b.addEventListener('click', acReset));
for (const [b, f, i] of [['#ac-pw', '#ac-pw-f', '#ac-cur'], ['#ac-us', '#ac-us-f', '#ac-ucur']]) on(b, 'click', () => { acReset(); $(f).hidden = false; $(i).focus(); });
const acFail = (e, id) => { acNotice(e.message, 'err'); $(id).value = ''; $(id).focus(); };
on('#ac-pw-f', 'submit', async ev => { ev.preventDefault(); const cur = $('#ac-cur').value, n = $('#ac-new').value, len = [...n].length; // code points, as the server counts
	if (len < LIM.password_min || len > LIM.password_max) return acNotice(`new password: ${LIM.password_min}–${LIM.password_max} characters`, 'err'); if (n !== $('#ac-new2').value) return acNotice('the two new passwords differ', 'err'); if (!cur) return acNotice('current password required', 'err');
	try { await api('/api/account/password', { method: 'POST', json: { current_password: cur, new_password: n } }); acReset(); acNotice(''); toast('Password changed', 'ok'); loadAccount(); }
	catch (e) { acFail(e, '#ac-cur'); } });
on('#ac-us-f', 'submit', async ev => { ev.preventDefault(); const cur = $('#ac-ucur').value, u = $('#ac-uname').value.trim();
	if (!LIM.user.test(u)) return acNotice('user name: letters, digits, _ . - (1–32)', 'err'); if (!cur) return acNotice('current password required', 'err');
	try { const r = await api('/api/account/user', { method: 'POST', json: { current_password: cur, user: u } }); sess.user = r.body.user || u; $('#h-user-n').textContent = sess.user; acReset(); acNotice(''); toast('User name changed to ' + sess.user, 'ok'); loadAccount(); }
	catch (e) { acFail(e, '#ac-ucur'); } });
on('#ac-revoke', 'click', async () => { if (!await ask('Sign out other sessions', 'Sign out every other session? This one stays signed in.', { ok: 'Sign out others', danger: true })) return;
	const r = await act(() => api('/api/account/sessions/revoke', { method: 'POST', json: { others: true } })); if (r) { toast(`${r.body.revoked || 0} other session(s) signed out`, 'ok'); loadAccount(); } });

// certificate (Settings page; protected, POSTs need CSRF)
const ctNotice = notice('#ct-notice');
const ctForm = (id, first) => { for (const f of ['#ct-regen-f', '#ct-upload-f']) $(f).hidden = f !== id || !$(f).hidden; if (!$(id).hidden) $(first).focus(); };
async function loadCert() { if (!signedIn()) return; try { cert = (await api('/api/tls')).body; } catch (e) { cert = null; if (e.status !== 501 && e.status !== 401) toast('certificate: ' + e.message, 'err'); } secState(); }
function renderCert() {
	const c = cert || { mode: 'off' }, i = c.info, b = $('#ct-mode'), fb = !!c.fallback, isFile = c.mode === 'file', isAuto = c.mode === 'auto' || fb;
	b.textContent = fb ? 'automatic (fallback)' : isFile ? 'own certificate' : isAuto ? 'automatic' : 'TLS off'; b.className = 'badge ' + (fb ? 'warn' : isAuto ? 'ok' : c.mode);
	$('#ct-off').hidden = !!i; $('#ct-body').hidden = !i; $('#ct-reset').hidden = !isFile && !fb; $('#ct-regen').hidden = isFile || fb; // fallback: regenerate is refused (409) until reset to auto
	$('#dz-reset').disabled = !isFile && !fb; $('#dz-regen').disabled = !i || isFile || fb;
	if (!i) return;
	const left = dLeft(i.not_after), soon = left < UI.thresh.certSoonDays, until = h('dd', { class: left < 0 ? 'expired' : soon ? 'soon' : '' }, i.not_after.slice(0, 10) + (left < 0 ? ' — expired' : soon ? ` — in ${left} days` : ''));
	kv($('#ct-kv'), [['subject', i.subject], ['issuer', i.issuer], ['valid from', i.not_before.slice(0, 10)], ['valid until', until], ['key', i.key_algo + (i.is_ca ? ' · CA flag (trust anchor)' : '')], ['serial', h('dd', { class: 'mono' }, i.serial_hex)]]);
	const san = clear($('#ct-san')); for (const n of i.dns_names) san.append(h('span', null, n)); for (const n of i.ips) san.append(h('span', { class: 'ip' }, n));
	if (!i.dns_names.length && !i.ips.length) san.append(h('span', { class: 'empty' }, 'no SANs'));
	$('#ct-fp').textContent = i.fingerprint_sha256;
	const lack = [], w = (c.warnings || []).filter(x => { const m = /^SAN list lacks host (.+)$/.exec(x); if (m) lack.push(m[1]); return !m; });
	if (lack.length) w.push(`Certificate does not cover ${lack.join(', ')} — under HSTS the browser will refuse ${lack.length > 1 ? 'these names' : 'that name'}.`);
	if (fb) w.unshift('The configured certificate files could not be loaded — the automatic certificate is served (see the daemon log). Upload the pair again or reset to auto.');
	if (w.length) ctNotice(w.join('\n'), 'warn');
}
const ctClearUpload = () => { for (const id of ['#ct-cpem', '#ct-kpem', '#ct-cfile', '#ct-kfile']) $(id).value = ''; $('#ct-force').checked = false; $('#ct-force-l').hidden = true; };
async function openCert() { ctNotice(''); $('#ct-regen-f').hidden = $('#ct-upload-f').hidden = true; ctClearUpload(); await loadCert(); renderCert(); }
on('#h-cert', 'click', () => go('settings', 'st-cert'));
$$('#st-cert [data-cancel]').forEach(b => b.addEventListener('click', () => { b.closest('form').hidden = true; ctClearUpload(); }));
for (const x of ['crt', 'cer']) on('#ct-' + x, 'click', () => act(() => download('/api/tls/cert.' + x, 'n5-fangov.' + x), n => `Downloaded ${n} — now import it (see How to trust)`));
on('#ct-copy', 'click', () => navigator.clipboard.writeText($('#ct-fp').textContent).then(() => toast('Fingerprint copied', 'ok'), () => toast('Clipboard blocked — select the text', 'warn')));
on('#ct-regen', 'click', () => { $('#ct-newkey').checked = false; ctForm('#ct-regen-f', '#ct-newkey'); });
on('#ct-upload', 'click', () => ctForm('#ct-upload-f', '#ct-cfile'));
// re-read /api/tls and the config (the [web] tls keys changed)
const ctDone = async (r, msg) => { const w = r.body.warning ? [r.body.warning] : r.body.warnings || [];
	toast(msg + (w.length ? ' (warnings)' : ''), w.length ? 'warn' : 'ok', UI.timing.toastNotice);
	await loadCert(); renderCert(); const have = new Set((cert && cert.warnings) || []), extra = w.filter(x => !have.has(x));
	if (extra.length) { const n = $('#ct-notice'); ctNotice((n.hidden ? '' : n.textContent + '\n') + extra.join('\n'), 'warn'); } loadConfig(); };
// regenerate: the form's checkbox or the danger zone's "with new key" button
const ctRegen = async nk => { if (nk && !await ask('New private key', 'Generate a new private key?\nEvery browser and OS store trusting the current certificate must import the new one.', { ok: 'Regenerate', danger: true })) return;
	const r = await act(() => api('/api/tls/regenerate', { method: 'POST', json: { keep_key: !nk } })); if (!r) return; $('#ct-regen-f').hidden = true; ctDone(r, 'Certificate regenerated'); };
on('#ct-regen-f', 'submit', ev => { ev.preventDefault(); ctRegen($('#ct-newkey').checked); });
on('#dz-regen', 'click', () => ctRegen(true));
for (const [f, ta] of [['#ct-cfile', '#ct-cpem'], ['#ct-kfile', '#ct-kpem']]) $(f).addEventListener('change', async ev => { const x = ev.target.files[0]; if (!x) return;
	if (x.size > LIM.pemBytes) return toast(x.name + ': larger than 64 KiB', 'err'); $(ta).value = await x.text(); });
// 400 + force_required: the leaf does not cover this session's host name
on('#ct-upload-f', 'submit', async ev => { ev.preventDefault(); const c = $('#ct-cpem').value.trim(), k = $('#ct-kpem').value.trim();
	if (!/BEGIN CERTIFICATE/.test(c) || !/PRIVATE KEY/.test(k)) return ctNotice('Need a PEM CERTIFICATE block and a PRIVATE KEY block.', 'err');
	let r; try { r = await api('/api/tls/upload', { method: 'POST', json: { cert: c, key: k, force: $('#ct-force').checked } }); }
	catch (e) { if (e.body && e.body.force_required) { $('#ct-force-l').hidden = false; $('#ct-force-h').textContent = e.body.host; ctNotice(e.message, 'err'); toast('Certificate does not cover ' + e.body.host, 'err'); } else toast(e.message, 'err'); return; }
	$('#ct-upload-f').hidden = true; ctClearUpload(); ctDone(r, 'Own certificate installed'); });
const ctReset = async () => { if (!await ask('Back to auto', 'Back to the automatic certificate? The uploaded pair is deleted.', { ok: 'Back to auto', danger: true })) return;
	const r = await act(() => api('/api/tls/reset', { method: 'POST' })); if (r) ctDone(r, 'Automatic certificate active'); };
on('#ct-reset', 'click', ctReset); on('#dz-reset', 'click', ctReset);

// boot: session first, protected bits via applyAuth. With ?mock=1 the boot waits for mock.js (never requested otherwise).
const boot = async () => {
	if (MOCK) toast('Mock mode', 'warn', UI.timing.toastNotice);
	api('/api/version').then(r => { version = r.body.version || ''; if (typeof r.body.tls === 'boolean') tls = r.body.tls; if (r.body.prerelease !== undefined) preBadge($$('.beta'), r.body.prerelease);
		if (r.body.limits) { Object.assign(LIM, r.body.limits); applyLimits(); } secState(); renderHeader(); }).catch(() => {});
	try { sess = (await api('/api/session')).body || sess; } catch (e) { sess = anon(); }
	await applyAuth();
	if (document.hidden) { poll(); loadHistory(); }
	schedule();
};
if (MOCK) { const s = h('script', { src: 'mock.js' }); s.addEventListener('load', boot); s.addEventListener('error', () => toast('mock.js could not be loaded', 'err', UI.timing.toastLong)); document.head.append(s); }
else boot();
})();
