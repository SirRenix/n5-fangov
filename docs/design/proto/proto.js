// 0.4.0 dashboard prototype (DESIGN.md §11a). Renders the target structure from the mock only:
// proto.html?mock=1 [&user=1] [&nav=side|rail|top] [&spark=0] [&fans=stack|tabs] [&compat=page|about]
// [&page=<id>] [&theme=dark|light] [&tls=off|file|soon|fallback] [&dlg=login|preset|more] [&pwm4=1] [&schedfail=1]
// No production code: app.js is untouched; this file borrows its helpers and formats where the picture depends on them.
'use strict';
const $ = (s, r) => (r || document).querySelector(s), $$ = (s, r) => [...(r || document).querySelectorAll(s)];
const h = (tag, attrs, ...kids) => { const e = document.createElement(tag);
	for (const k in attrs || {}) { const v = attrs[k]; if (v === null || v === undefined || v === false) continue;
		if (k === 'class') e.className = v; else if (k === 'text') e.textContent = v; else if (k.startsWith('on')) e.addEventListener(k.slice(2), v); else e.setAttribute(k, v === true ? '' : v); }
	for (const k of kids.flat()) if (k !== null && k !== undefined && k !== false) e.append(k.nodeType ? k : document.createTextNode(k)); return e; };
const clear = e => { while (e.firstChild) e.removeChild(e.firstChild); return e; };
const NS = 'http://www.w3.org/2000/svg';
const ico = (name, cls) => { const s = document.createElementNS(NS, 'svg'); s.setAttribute('class', 'ic' + (cls ? ' ' + cls : '')); s.setAttribute('aria-hidden', 'true'); const u = document.createElementNS(NS, 'use'); u.setAttribute('href', '#i-' + name); s.append(u); return s; };
const cssVar = n => getComputedStyle(document.documentElement).getPropertyValue(n).trim();
const SERIES = Array.from({ length: 8 }, (_, i) => '--s' + (i + 1));
const Q = new URLSearchParams(location.search);
const F = { nav: Q.get('nav') || (localStorage.getItem('n5-proto-nav') || 'side'), spark: Q.get('spark') !== '0', fans: Q.get('fans') || 'stack', compat: Q.get('compat') || 'page', theme: Q.get('theme') || 'dark' };
document.documentElement.dataset.theme = F.theme;
const hm = ts => new Date(ts * 1000).toTimeString().slice(0, 5);
const pct = d => Math.round(d / 255 * 100);
const fmtT = v => !(v > -900) ? '—' : v.toFixed(1);
const rel = ts => { const s = Math.max(0, Date.now() / 1000 - ts | 0); return s < 60 ? `${s} s ago` : s < 3600 ? `${s / 60 | 0} min ago` : s < 86400 ? `${s / 3600 | 0} h ago` : `${s / 86400 | 0} d ago`; };
const fmtUp = s => { const d = s / 86400 | 0, hh = s % 86400 / 3600 | 0, m = s % 3600 / 60 | 0; return d ? `${d}d ${hh}h` : hh ? `${hh}h ${m}m` : `${m}m`; };
const toTs = v => typeof v === 'number' ? v : Date.parse(v) / 1000;
const abs = ts => new Date(ts * 1000).toLocaleString();
const tm = (v, future) => { const ts = toTs(v); return !(ts > 0) ? '—' : h('time', { datetime: new Date(ts * 1000).toISOString() }, future ? abs(ts) : rel(ts)); };
const kv = (el, rows) => { clear(el); for (const [k, v] of rows) el.append(h('dt', null, k), v && v.nodeType ? v : h('dd', null, v)); return el; };
const interp = (curve, t) => { if (!curve.length) return 0; if (t <= curve[0][0]) return curve[0][1];
	for (let i = 1; i < curve.length; i++) if (t <= curve[i][0]) { const [t0, d0] = curve[i - 1], [t1, d1] = curve[i]; return t1 === t0 ? d1 : d0 + (d1 - d0) * (t - t0) / (t1 - t0); } return curve[curve.length - 1][1]; };
const isHdd = c => /hdd|drive|disk/.test(c.name) || /^drivetemp/.test(c.sensor || '');
const tempClass = (t, crit) => { if (t === null || t <= -900) return 'na'; if (!crit) return ''; const r = t / crit; return r >= 1 ? 't-crit' : r >= .85 ? 't-hot' : r >= .6 ? 't-warm' : 't-ok'; };
const modeBadge = m => h('span', { class: 'mode m-' + (m || 'unknown') }, m || 'unknown');
const toast = (msg, kind) => { const box = $('#toasts div'); const t = h('div', { class: 'toast ' + (kind || '') }, msg, h('button', { 'aria-label': 'Dismiss', onclick: () => t.remove() }, '×')); box.append(t); setTimeout(() => t.remove(), 5000); };
const MIN_ON = [['0s', 'off'], ['30s', '30 s'], ['1m0s', '1 min'], ['2m0s', '2 min'], ['5m0s', '5 min'], ['10m0s', '10 min'], ['30m0s', '30 min'], ['1h0m0s', '1 h']];
const DAYS = ['mon', 'tue', 'wed', 'thu', 'fri', 'sat', 'sun'];

// ---- pages / navigation --------------------------------------------------------------------
const PAGES = [
	{ g: 'Monitor', id: 'overview', t: 'Overview', ic: 'gauge', anon: true }, { g: 'Monitor', id: 'system', t: 'System', ic: 'chip' },
	{ g: 'Control', id: 'fans', t: 'Fans', ic: 'fan' }, { g: 'Control', id: 'schedules', t: 'Schedules', ic: 'clock' },
	{ g: 'Operate', id: 'alerts', t: 'Alerts', ic: 'bell' }, { g: 'Operate', id: 'log', t: 'Log', ic: 'list' },
	{ g: 'Settings', id: 'settings', t: 'Settings', ic: 'gear' },
	{ g: 'Info', id: 'compat', t: 'Compatibility', ic: 'checklist' }, { g: 'Info', id: 'about', t: 'About', ic: 'info', anon: true }];
if (F.compat === 'about') PAGES.splice(PAGES.findIndex(p => p.id === 'compat'), 1);
const BOTTOM = ['overview', 'fans', 'alerts', 'settings'];
let cur = 'overview', signedIn = false;
const visible = () => PAGES.filter(p => signedIn || p.anon);
const navBtn = (p, extra) => h('button', { type: 'button', 'aria-current': p.id === cur ? 'page' : null, title: p.t, 'data-page': p.id, onclick: () => go(p.id) }, ico(p.ic), h('span', { class: 'lbl' }, p.t), extra);
function buildNav() {
	const nav = clear($('#nav')), shell = $('#shell');
	shell.className = 'shell' + (F.nav === 'rail' ? ' rail' : F.nav === 'top' ? ' top' : '');
	nav.append(h('div', { class: 'brand-row' }, h('span', { class: 'logo' }, ico('fan')), h('span', { class: 'brand' }, 'n5-fangov')));
	let lastG = null;
	for (const p of visible()) {
		if (p.g !== lastG) { if (lastG) nav.append(h('div', { class: 'grp-sep' })); nav.append(h('div', { class: 'grp' }, p.g)); nav.append(h('ul')); lastG = p.g; }
		nav.lastChild.append(h('li', null, navBtn(p, p.id === 'fans' ? h('i', { class: 'dirty', id: 'nav-dirty', hidden: true, title: 'unsaved changes' }) : null)));
	}
	const foot = h('div', { class: 'foot' });
	if (F.nav !== 'top') foot.append(h('ul', null, h('li', null, h('button', { type: 'button', title: F.nav === 'rail' ? 'Expand' : 'Collapse', onclick: () => { F.nav = F.nav === 'rail' ? 'side' : 'rail'; localStorage.setItem('n5-proto-nav', F.nav); buildNav(); $('#s-nav').value = F.nav; } },
		ico(F.nav === 'rail' ? 'chev-r' : 'chev-l'), h('span', { class: 'lbl' }, 'Collapse')))), h('span', { class: 'vtxt', id: 'nav-ver' }, D.version ? 'v' + D.version.version : ''));
	nav.append(foot);
	// phone: bottom bar + More sheet
	const b = clear($('#bnav')), vis = visible(), main = vis.filter(p => BOTTOM.includes(p.id)), rest = vis.filter(p => !BOTTOM.includes(p.id));
	for (const p of main) b.append(navBtn(p));
	if (rest.length) b.append(h('button', { type: 'button', 'aria-current': rest.some(p => p.id === cur) ? 'page' : null, onclick: () => openMore(rest) }, ico('more'), h('span', { class: 'lbl' }, 'More')));
}
const openMore = rest => { const ul = clear($('#more-list')); for (const p of rest) ul.append(h('li', null, h('button', { type: 'button', 'aria-current': p.id === cur ? 'page' : null, onclick: () => { $('#more').close(); go(p.id); } }, ico(p.ic), p.t, h('span', { class: 'grp-t' }, p.g)))); $('#more').showModal(); };
function go(id, sec) {
	const p = PAGES.find(x => x.id === id && (signedIn || x.anon)); if (!p) return;
	cur = p.id; history.replaceState(null, '', '#' + cur + (sec ? '/' + sec : ''));
	for (const s of $$('.pg')) s.hidden = s.id !== 'p-' + cur;
	$('#ph-title').textContent = p.t; document.title = `${p.t} · n5-fangov`;
	for (const b of $$('[data-page]')) b.dataset.page === cur ? b.setAttribute('aria-current', 'page') : b.removeAttribute('aria-current');
	const more = $('#bnav button:last-child'); if (more && !more.dataset.page) { const inMore = !BOTTOM.includes(cur); inMore ? more.setAttribute('aria-current', 'page') : more.removeAttribute('aria-current'); }
	window.scrollTo(0, 0);
	if (sec) { const el = $('#' + sec); if (el) el.scrollIntoView({ block: 'start' }); }
	if (cur === 'overview') redrawCharts();
	if (cur === 'fans') drawAllCurves();
}
document.addEventListener('click', ev => { const b = ev.target.closest('[data-go]'); if (b) go(b.dataset.go, b.dataset.sec); });

// ---- data ----------------------------------------------------------------------------------
const D = {};
let api = null;
const get = async (p, opt) => { try { return (await api(p, opt)).body; } catch (e) { return null; } };
async function load() {
	D.version = await get('/api/version'); D.session = await get('/api/session'); signedIn = !!(D.session && D.session.authenticated);
	D.state = await get('/api/state'); D.history = await get('/api/history?minutes=120'); D.profiles = await get('/api/profiles'); D.about = await get('/api/about');
	if (signedIn) { [D.config, D.sensors, D.dash, D.presets, D.sched, D.alerts, D.system, D.log, D.tls, D.account, D.tokens] = await Promise.all(['/api/config', '/api/sensors', '/api/dashboard', '/api/presets', '/api/schedules', '/api/alerts', '/api/system', '/api/log?lines=100', '/api/tls', '/api/account', '/api/tokens'].map(p => get(p)));
		D.presetDetail = {}; for (const p of D.presets || []) D.presetDetail[p.name] = await get('/api/presets/' + encodeURIComponent(p.name)); }
	else D.tls = await get('/api/tls');
}
const chList = () => (D.config && D.config.config && D.config.config.channel) || (D.state ? D.state.channels.map(c => ({ name: c.name, pwm: c.pwm, sensor: c.sensor, curve: [], critical: 0 })) : []);
const critOf = name => { const c = chList().find(x => x.name === name); return c && +c.critical > 0 ? +c.critical : null; };
const chKey = c => JSON.stringify([c.name, +c.pwm, String(c.sensor), (c.curve || []).map(p => [+p[0], +p[1]]), +c.critical, c.stop === undefined || c.stop === 'auto' ? 'auto' : String(c.stop), +c.hysteresis || 0, c.min_on || '0s']);
const presetOfChannel = c => { for (const [name, d] of Object.entries(D.presetDetail || {})) { const pc = d && d.channels && d.channels.find(x => x.name === c.name); if (pc && chKey(pc) === chKey(c)) return name; } return null; };
const activePreset = () => { const chs = chList(); for (const [name, d] of Object.entries(D.presetDetail || {})) if (d && d.channels && d.channels.length === chs.length && chs.every(c => { const pc = d.channels.find(x => x.name === c.name); return pc && chKey(pc) === chKey(c); })) return name; return null; };

// ---- canvas: line chart, sparkline, curve --------------------------------------------------
const pxSetup = cv => { const r = cv.getBoundingClientRect(), dpr = devicePixelRatio || 1; cv.width = r.width * dpr; cv.height = r.height * dpr; const ctx = cv.getContext('2d'); ctx.setTransform(dpr, 0, 0, dpr, 0, 0); return [ctx, r.width, r.height]; };
const niceStep = r => { const p = Math.pow(10, Math.floor(Math.log10(r || 1))), f = r / p; return (f < 1.5 ? 1 : f < 3.5 ? 2 : f < 7.5 ? 5 : 10) * p; };
function lineChart(cv, series, opt) { // series: [{color:'--s1', data:[[ts,v]…]}]
	const [ctx, W, H] = pxSetup(cv), pad = { l: 40, r: 58, t: 8, b: 22 }, now = Date.now() / 1000, t0 = now - 7200;
	const all = series.flatMap(s => s.data.map(p => p[1])).filter(v => v > -900); if (!all.length) return;
	let lo = Math.min(...all), hi = Math.max(...all); const span = Math.max(opt.minSpan || 15, (hi - lo) * 1.3); const mid = (hi + lo) / 2; lo = Math.floor((mid - span / 2) / 5) * 5; hi = lo + span;
	if (opt.zero) lo = 0;
	const X = t => pad.l + (t - t0) / 7200 * (W - pad.l - pad.r), Y = v => pad.t + (1 - (v - lo) / (hi - lo)) * (H - pad.t - pad.b);
	ctx.font = `11px ${cssVar('--font-sans')}`; ctx.fillStyle = cssVar('--fg3'); ctx.strokeStyle = cssVar('--line'); ctx.lineWidth = 1;
	for (let t = Math.ceil(t0 / 1800) * 1800; t <= now; t += 1800) { ctx.beginPath(); ctx.moveTo(X(t), pad.t); ctx.lineTo(X(t), H - pad.b); ctx.stroke(); ctx.textAlign = 'center'; ctx.fillText(hm(t), X(t), H - 6); }
	const st = niceStep((hi - lo) / 4); for (let v = Math.ceil(lo / st) * st; v <= hi; v += st) { ctx.beginPath(); ctx.moveTo(pad.l, Y(v)); ctx.lineTo(W - pad.r, Y(v)); ctx.stroke(); ctx.textAlign = 'right'; ctx.fillText(String(Math.round(v)), pad.l - 6, Y(v) + 4); }
	if (opt.crit) for (const [v, col] of opt.crit) { if (v < lo || v > hi) continue; ctx.setLineDash([4, 3]); ctx.strokeStyle = col; ctx.beginPath(); ctx.moveTo(pad.l, Y(v)); ctx.lineTo(W - pad.r, Y(v)); ctx.stroke(); ctx.setLineDash([]); }
	series.forEach((s, i) => { const col = cssVar(s.color); ctx.strokeStyle = col; ctx.lineWidth = 2; ctx.beginPath(); let first = true;
		for (const [t, v] of s.data) { if (!(v > -900)) { first = true; continue; } first ? ctx.moveTo(X(t), Y(v)) : ctx.lineTo(X(t), Y(v)); first = false; } ctx.stroke();
		const last = s.data[s.data.length - 1]; if (last && last[1] > -900) { ctx.fillStyle = col; ctx.font = `600 11px ${cssVar('--font-sans')}`; ctx.textAlign = 'left'; ctx.fillText(opt.fmt ? opt.fmt(last[1]) : String(Math.round(last[1])), W - pad.r + 6, Y(last[1]) + 4 + (opt.stagger ? (i % 2 ? 6 : -6) : 0)); } });
}
function spark(cv, data, color, crit) {
	const [ctx, W, H] = pxSetup(cv); const vs = data.map(p => p[1]).filter(v => v > -900); if (vs.length < 2) return;
	const lo = Math.min(...vs) - 1, hi = Math.max(...vs) + 1, n = data.length, X = i => i / (n - 1) * (W - 2) + 1, Y = v => 2 + (1 - (v - lo) / (hi - lo)) * (H - 4), col = cssVar(color);
	ctx.beginPath(); data.forEach((p, i) => i ? ctx.lineTo(X(i), Y(p[1])) : ctx.moveTo(X(i), Y(p[1]))); ctx.strokeStyle = col; ctx.lineWidth = 1.5; ctx.stroke();
	ctx.lineTo(X(n - 1), H); ctx.lineTo(X(0), H); ctx.closePath(); ctx.fillStyle = col; ctx.globalAlpha = .12; ctx.fill(); ctx.globalAlpha = 1;
	const last = data[n - 1][1]; ctx.beginPath(); ctx.arc(X(n - 1), Y(last), 2.5, 0, 7); ctx.fillStyle = col; ctx.fill();
}
function drawCurve(cv, c, now) {
	const [ctx, W, H] = pxSetup(cv), pad = { l: 34, r: 12, t: 10, b: 22 }, xMax = Math.max(100, (c.critical || 0) + 10), X = t => pad.l + t / xMax * (W - pad.l - pad.r), Y = d => pad.t + (1 - d / 255) * (H - pad.t - pad.b);
	ctx.font = `11px ${cssVar('--font-sans')}`; ctx.fillStyle = cssVar('--fg3'); ctx.strokeStyle = cssVar('--line'); ctx.lineWidth = 1;
	for (let t = 0; t <= xMax; t += 20) { ctx.beginPath(); ctx.moveTo(X(t), pad.t); ctx.lineTo(X(t), H - pad.b); ctx.stroke(); ctx.textAlign = 'center'; ctx.fillText(t + '°', X(t), H - 6); }
	for (let d = 0; d <= 255; d += 51) { ctx.beginPath(); ctx.moveTo(pad.l, Y(d)); ctx.lineTo(W - pad.r, Y(d)); ctx.stroke(); ctx.textAlign = 'right'; ctx.fillText(String(pct(d)) + '%', pad.l - 5, Y(d) + 4); }
	if (c.critical) { ctx.setLineDash([4, 3]); ctx.strokeStyle = cssVar('--crit'); ctx.beginPath(); ctx.moveTo(X(c.critical), pad.t); ctx.lineTo(X(c.critical), H - pad.b); ctx.stroke(); ctx.setLineDash([]); ctx.fillStyle = cssVar('--crit'); ctx.textAlign = 'right'; ctx.fillText('crit', X(c.critical) - 3, pad.t + 10); }
	const pts = c.curve || []; if (!pts.length) return; const col = cssVar('--info');
	ctx.strokeStyle = col; ctx.lineWidth = 2; ctx.beginPath(); ctx.moveTo(X(0), Y(pts[0][1])); for (const [t, d] of pts) ctx.lineTo(X(t), Y(d)); ctx.lineTo(X(xMax), Y(pts[pts.length - 1][1])); ctx.stroke();
	if (now > -900) { const d = interp(pts, now); ctx.setLineDash([2, 3]); ctx.strokeStyle = cssVar('--fg3'); ctx.beginPath(); ctx.moveTo(X(now), pad.t); ctx.lineTo(X(now), H - pad.b); ctx.stroke(); ctx.setLineDash([]); ctx.beginPath(); ctx.arc(X(now), Y(d), 3, 0, 7); ctx.fillStyle = cssVar('--fg'); ctx.fill(); ctx.textAlign = 'left'; ctx.fillStyle = cssVar('--fg2'); ctx.fillText('now', X(now) + 5, pad.t + 10); }
	for (const [t, d] of pts) { ctx.beginPath(); ctx.arc(X(t), Y(d), 4.5, 0, 7); ctx.fillStyle = col; ctx.fill(); ctx.strokeStyle = cssVar('--bg2'); ctx.lineWidth = 2; ctx.stroke(); }
}
const CURVES = [];
const drawAllCurves = () => { for (const [cv, c, now] of CURVES) drawCurve(cv, c, now()); };

// ---- header ---------------------------------------------------------------------------------
function renderHeader() {
	const s = D.state, v = D.version || {}; if (!s) return;
	const prof = (D.profiles || []).find(p => p.name === s.profile);
	$('#h-profile').textContent = prof ? prof.title : s.profile;
	const st = $('#h-status'); st.className = 'chip ' + s.status; st.textContent = s.status;
	$('.uptime').textContent = 'up ' + fmtUp(s.uptime_s); $('.version').textContent = 'v' + v.version; $('.badge.beta').hidden = !v.prerelease;
	const nv = $('#nav-ver'); if (nv) nv.textContent = 'v' + v.version;
	$('#h-user').hidden = !signedIn; $('#h-user-n').textContent = signedIn ? D.session.user : ''; $('#h-signin').hidden = signedIn; $('#h-signout').hidden = !signedIn;
	const c = $('#h-cert'), t = D.tls, left = t && t.info ? Math.ceil((new Date(t.info.not_after) - Date.now()) / 864e5) : null;
	c.hidden = !(t && (t.fallback || (left !== null && left < 30)));
	if (!c.hidden) $('#h-cert-t').textContent = t.fallback ? 'certificate fallback' : left < 0 ? 'certificate expired' : `certificate expires in ${left} d`;
	c.onclick = () => go('settings', 'st-cert');
	for (const el of $$('[data-auth]')) el.hidden = !signedIn;
}

// ---- overview -------------------------------------------------------------------------------
function renderTiles() {
	const box = clear($('#tiles')), s = D.state; if (!s) return;
	s.channels.forEach((c, i) => {
		const crit = critOf(c.name), man = c.mode === 'manual', tile = h('div', { class: 'card tile' });
		tile.append(h('div', { class: 'top' }, h('span', null, h('span', { class: 'name' }, c.name), h('span', { class: 'sub' }, `pwm${c.pwm} · ${c.sensor}`)), h('span', { class: 'badges' }, c.hold_until ? h('span', { class: 'badge hold' }, `hold ${Math.max(0, c.hold_until - Date.now() / 1000) / 60 | 0} min`) : null, modeBadge(c.mode))));
		tile.append(h('div', { class: 'temp ' + tempClass(c.temp, crit) }, fmtT(c.temp), h('small', null, '°C'), c.held_temp !== undefined ? h('span', { class: 'held', title: 'held reading (hysteresis)' }, `held ${c.held_temp}`) : null));
		if (F.spark) { const cv = h('canvas'); tile.append(h('div', { class: 'spark' }, cv, h('span', { class: 'rng' }, '2 h'))); requestAnimationFrame(() => spark(cv, (D.history || []).map(p => [p.ts, p.temp[c.name]]), SERIES[i % 8])); }
		const bar = h('div', { class: 'bar-h' }, h('i', { class: man ? '' : c.mode === 'stall' ? 'stall' : c.mode === 'critical' ? 'crit' : 'auto', style: `width:${pct(c.duty)}%` }), h('b', { style: `left:${pct(c.target)}%` }));
		tile.append(h('div', { class: 'row' }, h('span', { class: 'k' }, 'duty'), bar, h('span', { class: 'v' }, `${c.duty}`, c.target !== c.duty ? h('span', { class: 'tg' }, ` → ${c.target}`) : null, ` (${pct(c.duty)} %)`)));
		tile.append(h('div', { class: 'row' }, h('span', { class: 'k' }, 'rpm'), h('span'), h('span', { class: 'v' }, c.rpm < 0 ? 'no tach' : c.rpm === 0 ? h('span', { class: 't-crit' }, '0 rpm') : `${c.rpm} rpm`)));
		box.append(tile);
	});
}
function redrawCharts() {
	const hist = D.history || [], chs = (D.state ? D.state.channels : []);
	const legend = (el, names) => { clear(el); names.forEach((n, i) => el.append(h('span', { style: `color:${cssVar(SERIES[i % 8])}` }, h('i'), n))); };
	legend($('#lg-temp'), chs.map(c => c.name)); legend($('#lg-fan'), chs.map(c => c.name));
	lineChart($('#ch-temp canvas'), chs.map((c, i) => ({ color: SERIES[i % 8], data: hist.map(p => [p.ts, p.temp[c.name]]) })), { fmt: v => v.toFixed(1) + '°', crit: chs.map(c => [critOf(c.name), cssVar('--crit')]).filter(x => x[0]) });
	lineChart($('#ch-fan canvas'), chs.map((c, i) => ({ color: SERIES[i % 8], data: hist.map(p => [p.ts, p.rpm[c.name]]) })), { minSpan: 1000, fmt: v => Math.round(v) + '' });
	const ex = (D.dash && D.dash.sensors) || []; $('#extra-card').hidden = !signedIn || !ex.length;
	if (ex.length) { legend($('#lg-extra'), ex.map(id => (D.sensors || []).find(s => s.id === id)?.description.replace(/ \(now.*/, '') || id)); lineChart($('#ch-extra canvas'), ex.map((id, i) => ({ color: SERIES[(i + 3) % 8], data: hist.map(p => [p.ts, p.extra ? p.extra[id] : -999]) })), { fmt: v => v.toFixed(1) + '°', stagger: true }); }
}
const GROUPS = [['CPU', /^(k10temp|coretemp)/], ['SSD · NVMe', /^nvme/], ['HDD', /^drivetemp/], ['GPU', /^(amdgpu|nouveau|i915|radeon)/], ['NIC', /^(nic|eth|mlx|igc|ixgbe|r8169|atlantic)/], ['EC · board', /^(ec$|minisforum|acpitz|spd5118)/]];
const groupOf = s => { if (s.kind === 'ssd') return 'SSD · NVMe'; if (s.kind === 'hdd') return 'HDD'; const key = s.id.replace(/^hwmon:/, '').split(':')[0]; for (const [g, re] of GROUPS) if (re.test(key)) return g; return 'other'; };
function renderSensors() {
	const box = clear($('#sensors')), list = (D.sensors || []).filter(s => !s.id.includes('<')), on = new Set((D.dash && D.dash.sensors) || []), by = {};
	for (const s of list) (by[groupOf(s)] = by[groupOf(s)] || []).push(s);
	for (const g of ['CPU', 'SSD · NVMe', 'HDD', 'GPU', 'NIC', 'EC · board', 'other']) { if (!by[g]) continue; box.append(h('div', { class: 'grp-h' }, g));
		for (const s of by[g]) box.append(h('div', { class: 'sn' }, h('span', null, h('span', { class: 'd' }, s.description.replace(/ \(now.*/, '')), h('span', { class: 'id' }, s.id)), h('span', { class: 'val' }, s.temp.toFixed(1) + ' °C'), h('button', { class: 'btn sm' + (on.has(s.id) ? ' on' : ''), type: 'button', 'aria-pressed': on.has(s.id) }, ico('chart'), 'chart'))); }
	const y = D.system; if (y) kv($('#sys'), [['host', y.host.hostname], ['kernel', y.host.kernel], ['cpu', y.cpu.model], ['memory', `${(y.memory.available_bytes / 2 ** 30).toFixed(1)} of ${(y.memory.total_bytes / 2 ** 30).toFixed(1)} GiB free`], ['load', `${y.host.load1} · ${y.host.load5} · ${y.host.load15}`], ['controller', `${y.fan_controller.module} ${y.fan_controller.module_version}`], ['hwmon', h('dd', { class: 'mono' }, y.fan_controller.hwmon)]]);
	alertList($('#alerts'), (D.alerts && D.alerts.recent || []).slice(0, 5));
}
const alertList = (el, recent) => { clear(el); if (!recent.length) return el.append(h('li', { class: 'empty' }, 'no alerts'));
	for (const a of recent) el.append(h('li', null, h('span', { class: 'k ' + a.kind }, a.kind), h('span', { class: 'msg' }, a.msg, a.error ? h('span', { class: 'de' }, a.error) : null), tm(a.ts))); };

// ---- system --------------------------------------------------------------------------------
const GiB = 2 ** 30, fmtB = b => !(b > 0) ? '0 B' : b >= 1024 * GiB ? (b / 1024 / GiB).toFixed(1) + ' TiB' : b >= GiB ? (b / GiB).toFixed(1) + ' GiB' : Math.round(b / 2 ** 20) + ' MiB';
const na = v => v === undefined || v === null || v === '' ? '—' : v;
const trow = (tb, ...cells) => tb.append(h('tr', null, ...cells.map(c => h('td', c && c.mono ? { class: 'mono' } : null, c && c.mono ? c.mono : na(c))))), mono = v => v ? { mono: v } : null;
function renderSystem() {
	const y = D.system; if (!y) return; const hh = y.host, m = y.machine, c = y.cpu, me = y.memory;
	$('#sy-when').textContent = `live ${rel(y.collected)} · static ${rel(y.static_at)}`;
	kv($('#sy-host'), [['hostname', hh.hostname], ['OS', hh.os], ['kernel', hh.kernel], ['uptime', fmtUp(hh.uptime_s)], ['load', `${hh.load1} · ${hh.load5} · ${hh.load15}`]]);
	kv($('#sy-machine'), [['vendor', m.vendor], ['product', m.product], ['board', `${m.board} (${m.board_vendor})`], ['BIOS', `${m.bios_version} · ${m.bios_date}`]]);
	kv($('#sy-cpu'), [['model', c.model], ['topology', `${c.sockets} socket · ${c.cores} cores · ${c.threads} threads`], ['max clock', c.max_mhz + ' MHz']]);
	kv($('#sy-fan'), [['profile', y.fan_controller.profile], ['hwmon', h('dd', { class: 'mono' }, y.fan_controller.hwmon)], ['module', `${y.fan_controller.module} ${y.fan_controller.module_version}`]]);
	kv($('#sy-mem'), [['total', `${fmtB(me.total_bytes)} visible to the OS · ${fmtB(me.installed_bytes)} installed (SMBIOS ${me.smbios})`], ['available', fmtB(me.available_bytes)], ['swap', `${fmtB(me.swap_free_bytes)} of ${fmtB(me.swap_total_bytes)} free`]]);
	const tb = clear($('#sy-dimms tbody')); for (const d of me.modules) trow(tb, d.slot, d.bank, fmtB(d.size_bytes), `${d.type} ${d.form_factor}`, d.speed_mts + ' MT/s', d.ecc ? 'yes' : 'no', d.manufacturer, mono(d.part));
	const ta = clear($('#sy-acc tbody')); for (const g of y.gpus) trow(ta, 'GPU', g.name, g.vendor, mono(g.pci), g.driver, null, null); for (const n of y.npus) trow(ta, 'NPU', n.name, null, mono(n.pci), n.driver, (n.driver_version || '').split(',')[0], n.accel);
	const tn = clear($('#sy-nics tbody')); for (const n of y.nics) trow(tn, n.name, n.state, n.speed_mbit > 0 ? (n.speed_mbit >= 1000 ? n.speed_mbit / 1000 + ' Gbit/s' : n.speed_mbit + ' Mbit/s') : '—', n.duplex, n.model, n.driver, mono(n.mac), n.mtu, mono(n.pci));
	const tc = clear($('#sy-ctl tbody')); for (const k of y.storage.controllers) trow(tc, k.kind, k.name, k.driver, mono(k.pci));
	const td = clear($('#sy-disks tbody')); for (const d of y.storage.disks) trow(td, mono(d.name), fmtB(d.size_bytes), d.rotational ? 'HDD' : 'SSD', d.transport, d.model, d.temp_c === null ? '—' : d.temp_c.toFixed(1) + ' °C');
}

// ---- fans -----------------------------------------------------------------------------------
let dirty = false;
const setDirty = v => { dirty = v; $('#cv-dirty').hidden = !v; $('#cv-clean').hidden = v; const n = $('#nav-dirty'); if (n) n.hidden = !v; };
function renderFans() {
	const box = clear($('#fan-cards')), chs = chList(), sel = $('#ch-sel'); CURVES.length = 0;
	sel.hidden = F.fans !== 'tabs'; if (F.fans === 'tabs') { clear(sel); chs.forEach((c, i) => sel.append(h('button', { class: i === 0 ? 'on' : '', 'aria-pressed': i === 0, type: 'button', onclick: ev => { $$('button', sel).forEach(b => { b.classList.toggle('on', b === ev.currentTarget); b.setAttribute('aria-pressed', b === ev.currentTarget); }); $$('.fan', box).forEach((f, j) => f.hidden = j !== i); drawAllCurves(); } }, c.name, ' ', h('span', { class: 'sub' }, `pwm${c.pwm}`)))); }
	chs.forEach((c, i) => {
		const live = (D.state && D.state.channels.find(x => x.name === c.name)) || {}, man = live.mode === 'manual', pre = presetOfChannel(c);
		const cv = h('canvas', { role: 'img', 'aria-label': `${c.name} fan curve` }), nowT = () => (D.state && D.state.channels.find(x => x.name === c.name) || {}).temp;
		CURVES.push([cv, c, nowT]);
		const ptRows = h('tbody'); (c.curve || []).forEach(([t, d], j) => ptRows.append(h('tr', null, h('td', null, h('input', { type: 'number', value: t, 'aria-label': `point ${j + 1} temperature` })), h('td', null, h('input', { type: 'number', value: d, 'aria-label': `point ${j + 1} duty` })), h('td', { class: 'hint sm' }, `${pct(d)} %`), h('td', null, h('button', { class: 'btn sm link', type: 'button', disabled: (c.curve || []).length <= 2, 'aria-label': 'remove point' }, ico('trash'))))));
		const fields = h('div', { class: 'fields' },
			h('label', null, 'sensor ', h('select', null, h('option', null, c.sensor))), h('label', null, 'critical ', h('input', { type: 'number', value: c.critical, min: 30, max: 150 }), '°C'),
			h('label', null, 'stop ', h('input', { type: 'text', value: c.stop === undefined ? 'auto' : c.stop, placeholder: 'auto' })),
			h('label', null, 'hysteresis ', h('input', { type: 'number', value: c.hysteresis || 0, min: 0, max: 10 }), '°C'),
			h('label', null, 'min on ', h('select', null, ...MIN_ON.map(([v, t]) => h('option', { value: v, selected: (c.min_on || '0s') === v }, t)))));
		fields.addEventListener('input', () => setDirty(true));
		const ed = h('div', { class: 'ed' }, h('div', { class: 'cvs' }, cv), fields, h('div', { class: 'ptbl' }, h('table', null, h('thead', null, h('tr', null, h('th', null, '°C'), h('th', null, 'duty'), h('th'), h('th'))), ptRows), h('button', { class: 'btn sm', type: 'button', disabled: (c.curve || []).length >= 8 }, ico('plus'), 'add point')), h('p', { class: 'ref' }, h('span', { class: 'hint sm' }, 'duty → rpm on this hardware: 85 → ~2000 · 140 → ~3100 · 255 → ~5100')));
		ed.addEventListener('input', () => setDirty(true));
		const sw = h('button', { class: 'switch', type: 'button', role: 'switch', 'aria-checked': man, onclick: () => { const on = sw.getAttribute('aria-checked') !== 'true'; sw.setAttribute('aria-checked', on); lv.dataset.off = !on; toast(on ? `${c.name}: manual — holding ${live.duty} (${pct(live.duty)} %)` : `${c.name}: back to auto`, 'ok'); } }, h('span', { class: 'track' }), h('span', null, h('b', null, 'Manual'), ' override'));
		const rg = h('input', { type: 'range', min: isHdd(c) ? 60 : 0, max: 255, value: live.duty || 0, 'aria-label': `${c.name} manual duty` }), num = h('input', { type: 'number', min: isHdd(c) ? 60 : 0, max: 255, value: live.duty || 0, 'aria-label': `${c.name} manual duty` });
		rg.oninput = () => num.value = rg.value; num.oninput = () => rg.value = num.value;
		const lv = h('div', { class: 'live', 'data-off': !man }, h('h3', null, 'Live & override'),
			h('div', { class: 'now' }, h('span', { class: 't ' + tempClass(live.temp, +c.critical) }, fmtT(live.temp), h('small', null, ' °C')), h('span', { class: 'arrow' }, '→'), h('span', { class: 'd' }, `${live.duty}`, h('small', null, ` duty · ${pct(live.duty || 0)} %`)), h('span', { class: 'hint sm' }, live.rpm < 0 ? 'no tach' : `${live.rpm} rpm`)),
			h('div', { class: 'hint sm' }, `curve target ${live.target} · mode `, modeBadge(live.mode)),
			sw,
			h('div', { class: 'man' }, h('div', { class: 'rg' }, rg, num), h('div', { class: 'actions' }, h('button', { class: 'btn primary sm', type: 'button', onclick: () => toast(`${c.name}: manual ${num.value} (${pct(+num.value)} %)`, 'ok') }, 'Set'))),
			h('p', { class: 'warn' + (isHdd(c) ? ' on' : '') }, isHdd(c) ? 'HDD channel: the daemon refuses a manual duty below 60 (stall guard).' : 'Critical-temperature and stall guards still apply and override any manual value.'));
		box.append(h('div', { class: 'card fan', hidden: F.fans === 'tabs' && i > 0 }, h('div', { class: 'fh' }, h('span', { class: 'name' }, c.name), h('span', { class: 'sub' }, `pwm${c.pwm} · ${c.sensor}`), h('span', { class: 'badges' }, pre ? h('span', { class: 'badge preset', title: 'the preset these values match' }, ico('check'), pre) : h('span', { class: 'badge builtin', title: 'no preset matches these values' }, 'custom'), modeBadge(live.mode))), ed, lv));
	});
	requestAnimationFrame(drawAllCurves);
	// presets row
	const row = clear($('#preset-row')), act = activePreset();
	for (const p of D.presets || []) { const on = p.name === act, rec = p.name === 'n5pro-balanced';
		row.append(h('div', { class: 'pchip' + (on ? ' active' : '') }, h('i', { class: 'dot', style: `background:${on ? 'var(--ok)' : 'var(--line2)'}`, title: on ? 'active — the daemon runs these values' : '' }), rec ? ico('star') : null, h('span', { class: 'pn' }, p.name), p.builtin ? h('span', { class: 'badge builtin' }, 'built-in') : null, p.description ? h('span', { class: 'pd', title: p.description }, p.description) : null,
			h('button', { class: 'btn sm', type: 'button', disabled: on, title: on ? 'already active' : `apply ${p.name}` }, on ? 'active' : 'Apply'),
			h('button', { class: 'btn icon link', type: 'button', 'aria-label': `${p.name}: details`, title: 'Details', onclick: () => openPresetEditor(p.name) }, ico(p.builtin ? 'external' : 'edit')),
			p.builtin ? null : h('button', { class: 'btn icon link', type: 'button', 'aria-label': `${p.name}: delete`, title: 'Delete' }, ico('trash')))); }
	if (!(D.presets || []).length) row.append(h('div', { class: 'empty-cta' }, 'No presets yet — save the current curves or apply a built-in one.'));
	$('#ps-new').onclick = () => openPresetEditor(null);
	$('#cv-revert').onclick = () => { setDirty(false); toast('Reverted'); }; $('#cv-apply').onclick = () => { setDirty(false); toast('Curves applied', 'ok'); };
}
function openPresetEditor(name) {
	const dlg = $('#preset-ed'), body = clear($('#pe-body')), src = name ? D.presetDetail[name] : { channels: chList() }, ro = !!(src && src.builtin);
	$('#pe-title').textContent = name ? (ro ? `Preset ${name}` : `Edit preset ${name}`) : 'New preset';
	body.append(h('div', { class: 'src' }, 'Start from ', h('select', { disabled: !!name }, h('option', null, 'the editor (unsaved values)'), h('option', null, 'the daemon (running curves)'), ...(D.presets || []).map(p => h('option', { selected: p.name === name }, `preset ${p.name}`))), h('span', { class: 'hint sm' }, 'Saving stores the values below — nothing is applied to the daemon.')));
	body.append(h('div', { class: 'frow' }, h('label', null, 'Name ', h('input', { type: 'text', value: name || '', placeholder: 'summer', maxlength: 64, readonly: ro, autofocus: true })), h('label', null, 'Description ', h('input', { type: 'text', value: (src && src.description) || '', placeholder: 'optional', maxlength: 120, readonly: ro }))));
	for (const c of (src && src.channels) || []) body.append(h('div', { class: 'pe-ch' }, h('div', { class: 'fh' }, h('span', { class: 'name' }, c.name), h('span', { class: 'subt' }, `pwm${c.pwm} · ${Array.isArray(c.sensor) ? c.sensor.join(',') : c.sensor}`)),
		h('div', { class: 'fields' }, h('label', null, 'critical ', h('input', { type: 'number', value: c.critical, readonly: ro }), '°C'), h('label', null, 'stop ', h('input', { type: 'text', value: c.stop === undefined ? 'auto' : c.stop, style: 'width:4em', readonly: ro })), h('label', null, 'hysteresis ', h('input', { type: 'number', value: c.hysteresis || 0, readonly: ro }), '°C'), h('label', null, 'min on ', h('select', { disabled: ro }, ...MIN_ON.map(([v, t]) => h('option', { value: v, selected: (c.min_on || '0s') === v }, t))))),
		h('div', { class: 'pts' }, h('span', { class: 'hint sm', style: 'font-family:inherit;border:0;background:none' }, 'points'), ...(c.curve || []).map(([t, d]) => h('span', null, `${t} °C → ${d}`)), ro ? null : h('button', { class: 'btn sm link', type: 'button' }, ico('edit'), 'edit points'))));
	body.append(h('div', { class: 'act' }, h('button', { class: 'btn', type: 'button', onclick: () => dlg.close() }, ro ? 'Close' : 'Cancel'), ro ? null : h('button', { class: 'btn primary', type: 'button', onclick: () => { dlg.close(); toast(`Preset ${name || 'summer'} saved`, 'ok'); } }, name ? 'Save changes' : 'Save preset')));
	dlg.showModal();
}

// ---- schedules ------------------------------------------------------------------------------
function renderSchedules() {
	const s = D.sched; if (!s) return; const tb = clear($('#sc-tbl tbody')), names = (D.presets || []).map(p => p.name);
	for (const e of s.entries) { const fb = !e.from;
		tb.append(h('tr', { class: e.active ? 'active' : '' }, h('td', null, h('select', { 'aria-label': 'preset' }, ...names.map(n => h('option', { selected: n === e.preset }, n))), e.active ? h('span', { class: 'act' }, 'ACTIVE') : null),
			...(fb ? [h('td', { colspan: 3, class: 'hint sm' }, 'fallback — outside every window')] : [h('td', null, h('input', { type: 'time', value: e.from, 'aria-label': 'from' })), h('td', null, h('input', { type: 'time', value: e.to, 'aria-label': 'to' })),
				h('td', null, h('div', { class: 'days' }, ...DAYS.map(d => h('button', { type: 'button', 'aria-pressed': !e.days.length || e.days.includes(d), 'aria-label': d }, d[0].toUpperCase() + d[1])), !e.days.length ? h('span', { class: 'all' }, 'every day') : null))]),
			h('td', null, h('button', { class: 'btn icon link', type: 'button', 'aria-label': 'remove entry', title: 'Remove' }, ico('trash'))))); }
	if (!s.entries.length) tb.append(h('tr', null, h('td', { colspan: 5, class: 'empty' }, 'No schedule — the daemon keeps the curves it has. Add an entry to switch presets by time of day.')));
	const act = s.entries[s.active];
	kv($('#sc-kv'), [['active', act ? (act.fallback ? `${act.preset} (fallback)` : `${act.preset} ${act.from}–${act.to}`) : 'none'], ['next switch', s.next ? h('dd', null, `${s.next.preset || 'fallback'} `, tm(s.next.ts, true)) : '—'], ['last switch', s.last ? h('dd', null, `${s.last.preset} `, tm(s.last.ts), s.last.ok ? '' : h('span', { class: 't-crit' }, ` — ${s.last.error}`)) : '—'], ['timezone', s.timezone]]);
	const n = $('#sc-notice'); n.hidden = !(s.last && !s.last.ok); if (!n.hidden) n.textContent = `Last switch failed: ${s.last.error}. The previous curves were kept.`;
}

// ---- alerts / log / settings / about ----------------------------------------------------------
function renderAlerts() {
	const a = D.alerts; if (!a) return;
	kv($('#al-status'), [['transport', `${a.transport} → effective ${a.effective}`], ['mail to', a.mail_to], ['tools', `pve-notify ${a.pve_available ? 'available' : 'missing'} · mail ${a.mail_available ? 'available' : 'missing'}`], ['cooldown', a.cooldown + ' per kind']]);
	alertList($('#al-recent'), a.recent || []);
	const tb = clear($('#al-kinds tbody')); for (const k of a.kinds) tb.append(h('tr', null, h('td', null, h('span', { class: 'alerts' }, h('span', { class: 'k ' + k.kind }, k.kind))), h('td', null, k.description), h('td', null, a.last[k.kind] ? tm(a.last[k.kind]) : '—')));
	$('#al-test').onclick = () => toast(`Test alert sent via ${a.effective}`, 'ok');
	$('#al-transport').value = a.transport; $('#al-mailto').value = a.mail_to; kv($('#al-tpl'), [['status', a.template.installed ? (a.template.current ? 'installed, up to date' : 'installed, outdated') : 'not installed'], ['path', h('dd', { class: 'mono' }, a.template.path)], ['writable', a.template.writable ? 'yes' : 'no']]);
}
function renderLog() { const l = D.log; if (!l) return; const pre = clear($('#log')); $('#lg-meta').textContent = `${l.lines.length} lines · ${l.source}`;
	for (const ln of l.lines) pre.append(h('span', { class: /WARN/.test(ln) ? 'w' : /ERROR/.test(ln) ? 'e' : '' }, ln + '\n')); pre.scrollTop = pre.scrollHeight; }
function renderSettings() {
	$('#s-theme').value = F.theme; $('#s-nav').value = F.nav === 'top' ? 'side' : F.nav;
	$('#s-theme').onchange = ev => { F.theme = ev.target.value; document.documentElement.dataset.theme = F.theme; redrawCharts(); drawAllCurves(); renderTiles(); };
	$('#s-nav').onchange = ev => { F.nav = ev.target.value; localStorage.setItem('n5-proto-nav', F.nav); buildNav(); };
	const sn = clear($('#subnav')), secs = $$('.st'); for (const s of secs) sn.append(h('button', { type: 'button', 'data-sec': s.id, onclick: () => { s.scrollIntoView({ block: 'start' }); mark(s.id); } }, s.classList.contains('danger') ? ico('warn') : null, $('h2', s).textContent));
	const mark = id => $$('button', sn).forEach(b => b.dataset.sec === id ? b.setAttribute('aria-current', 'true') : b.removeAttribute('aria-current')); mark('st-display');
	const vis = new Set(); const io = new IntersectionObserver(es => { for (const e of es) e.isIntersecting ? vis.add(e.target.id) : vis.delete(e.target.id); const first = secs.find(s => vis.has(s.id)); if (first) mark(first.id); }, { rootMargin: '-64px 0px -60% 0px' }); secs.forEach(s => io.observe(s));
	const ac = D.account; if (ac) { $('#ac-user').textContent = ac.user; const tb = clear($('#ac-sessions tbody'));
		for (const s of ac.sessions) tb.append(h('tr', { class: s.current ? 'active' : '' }, h('td', { class: 'mono' }, s.id, s.current ? h('span', { class: 'act' }, 'THIS SESSION') : null), h('td', null, tm(s.created)), h('td', null, tm(s.last_seen)), h('td', null, tm(s.expires, true), s.remember ? h('span', { class: 'tid' }, 'remembered') : null), h('td', { class: 'mono' }, s.ip))); }
	const tk = D.tokens; if (tk) { const tb = clear($('#ac-tok tbody'));
		for (const t of tk.tokens) tb.append(h('tr', { class: t.expired ? 'dim' : '' }, h('td', null, t.name, h('span', { class: 'tid mono' }, t.id)), h('td', null, t.scope), h('td', null, tm(t.created)), h('td', { class: t.expired ? 'expired' : '' }, t.expires ? (t.expired ? 'expired' : tm(t.expires, true)) : 'never'), h('td', null, t.last_used ? tm(t.last_used) : '—'), h('td', { class: 'mono' }, t.last_ip || '—'), h('td', null, h('button', { class: 'btn sm', type: 'button' }, 'Revoke')))); }
	const t = D.tls; if (t) { const i = t.info, b = $('#ct-mode'); b.className = 'badge ' + (t.fallback ? 'warn' : t.mode === 'file' ? 'file' : t.mode === 'off' ? 'off' : 'ok'); b.textContent = t.fallback ? 'automatic (fallback)' : t.mode === 'file' ? 'own certificate' : t.mode === 'off' ? 'TLS off' : 'automatic';
		if (i) { const left = Math.ceil((new Date(i.not_after) - Date.now()) / 864e5); kv($('#ct-kv'), [['subject', i.subject], ['issuer', i.issuer], ['valid', h('dd', { class: left < 30 ? 'soon' : '' }, `${new Date(i.not_before).toLocaleDateString()} – ${new Date(i.not_after).toLocaleDateString()}${left < 30 ? ` · expires in ${left} days` : ''}`)], ['key', i.key_algo], ['serial', h('dd', { class: 'mono' }, i.serial_hex)]]);
			const san = clear($('#ct-san')); for (const d of i.dns_names) san.append(h('span', null, d)); for (const ip of i.ips) san.append(h('span', { class: 'ip' }, ip)); $('#ct-fp').textContent = i.fingerprint_sha256; }
		const n = $('#ct-notice'); n.hidden = !(t.warnings && t.warnings.length); if (!n.hidden) n.textContent = t.warnings.join('\n'); }
}
function renderAbout() { const a = D.about; if (!a) return; $('#ab-version').textContent = 'v' + a.version; $('#ab-beta').hidden = !a.prerelease;
	const link = (href, t) => h('dd', null, h('a', { href, rel: 'noopener', target: '_blank' }, t, ' ', ico('external')));
	kv($('#ab-kv'), [['license', link(a.license_url, a.license)], ['repository', link(a.repo, a.repo.replace('https://', ''))], ['author', link(a.author_url, a.author)], ['releases', link(a.repo + '/releases', 'GitHub releases')], ...(a.go ? [['built with', a.go]] : []), ['credits', h('dd', null, h('ul', { class: 'credits' }, ...a.credits.map(c => h('li', null, h('a', { href: c.url, rel: 'noopener', target: '_blank' }, c.name), ' — ' + c.note))))]]);
	const rows = tb => { clear(tb); for (const p of D.profiles || []) tb.append(h('tr', { class: D.state && D.state.profile === p.name ? 'active' : '' }, h('td', { class: 'mono' }, p.name, D.state && D.state.profile === p.name ? h('span', { class: 'act' }, 'ACTIVE') : null), h('td', null, p.title), h('td', null, h('span', { class: 'badge ' + (p.verified ? 'ok' : 'warn') }, p.verified ? 'verified on hardware' : 'from documentation · untested')), h('td', null, p.notes))); };
	rows($('#profiles tbody')); if (F.compat === 'about' && signedIn) { $('#ab-compat').hidden = false; rows($('#profiles2 tbody')); } }

// ---- boot -----------------------------------------------------------------------------------
async function boot() {
	await load();
	buildNav(); renderHeader(); renderTiles(); redrawCharts(); renderAbout();
	if (signedIn) { renderSensors(); renderSystem(); renderFans(); renderSchedules(); renderAlerts(); renderLog(); renderSettings(); }
	const tab = Q.get('tab'), map = { curves: 'fans', manual: 'fans', presets: 'fans' }, want = Q.get('page') || (tab ? map[tab] || tab : location.hash.slice(1).split('/')[0]) || 'overview', sec = location.hash.split('/')[1];
	go(PAGES.some(p => p.id === want) ? want : 'overview', sec);
	$('#h-signin').onclick = () => $('#login').showModal(); $('#h-signout').onclick = () => { location.search = location.search.replace(/&user=1/, ''); };
	$('#login form').onsubmit = ev => { ev.preventDefault(); location.search += '&user=1'; };
	for (const b of $$('[data-close]')) b.onclick = () => b.closest('dialog').close();
	for (const d of $$('dialog')) d.addEventListener('click', ev => { if (ev.target === d) d.close(); });
	const dlg = Q.get('dlg'); if (dlg === 'login') $('#login').showModal(); else if (dlg === 'preset' && signedIn) openPresetEditor(null); else if (dlg === 'more') openMore(visible().filter(p => !BOTTOM.includes(p.id)));
	addEventListener('resize', () => { redrawCharts(); drawAllCurves(); if (F.spark) renderTiles(); });
	setInterval(async () => { const s = await get('/api/state'); if (!s) return; D.state = s; const hst = await get('/api/history?minutes=120'); if (hst) D.history = hst; renderHeader(); if (cur === 'overview') { renderTiles(); redrawCharts(); } }, 5000);
	toast('Prototype · mock data · ' + [F.nav, F.spark ? 'sparklines' : 'no sparklines', F.fans, 'compat=' + F.compat].join(' · '));
}
const s = document.createElement('script'); s.src = '../../../internal/web/static/mock.js'; s.onload = () => { api = window.n5mock; boot(); }; document.head.append(s);
