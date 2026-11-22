// n5-fangov dashboard — curve preview (POST /api/preview, nothing applied). A module loaded on demand (DESIGN 11a): app.js
// inserts it on the first open and hands over what it needs; window.n5pv(X) returns { open, close, sched, redraw }.
'use strict';
window.n5pv = X => {
const { h, $, api, chart, charts, RANGES, seriesColor, ED, stopN, cssVar } = X;
// kind ed = the editor's draft of one channel (recomputed 1 s after an edit), pre = a preset (all channels), as = one channel
// with the live assist switch inverted (PV.on() at request time); one panel at a time, under its card. One request in flight:
// a change meanwhile is sent when it returns, an answer whose body (draft + span) is no longer the latest is dropped; the span
// of the answer is the one it was asked for
const PV = { sp: '24h', w: {} };
const body = () => { const c = PV.kind === 'ed' && ED[PV.ch] && ED[PV.ch].c, b = { span: PV.sp };
	if (c) b.channels = [{ name: c.name, sensor: c.sensor, curve: c.curve, critical: +c.critical, stop: stopN(c.stop), hysteresis: +c.hysteresis || 0, min_on: c.min_on }];
	else if (PV.kind === 'pre') b.preset = PV.ch; else if (PV.kind === 'as') b.assist = { [PV.ch]: PV.on() }; return b; };
const close = () => { if (PV.el) PV.el.remove(); for (const k in PV.w) charts.splice(charts.indexOf(PV.w[k]), 1); PV.el = null; PV.w = {}; };
// the header names the comparison: what the recorded duty is set against
const what = () => PV.kind === 'pre' ? `preset "${PV.ch}"` : PV.kind === 'as' ? `assist ${PV.on() ? 'on' : 'off'}` : 'unsaved curve';
const open = (card, kind, ch, on) => { close(); Object.assign(PV, { kind, ch, on, el: $('#t-pv').content.firstElementChild.cloneNode(true) }); card.after(PV.el);
	for (const b of PV.el.querySelectorAll('[data-sp]')) b.onclick = () => { PV.sp = b.dataset.sp; run(); };
	$('[data-pvx]', PV.el).onclick = close; $('[data-pvd]', PV.el).href = X.docs('04-dashboard.md#preview'); run(); PV.el.scrollIntoView({ block: 'nearest' }); };
const sched = () => { if (PV.el && PV.kind === 'ed') { clearTimeout(PV.t); PV.t = setTimeout(run, 1000); } };
const key = c => { const i = h('i'); i.style.background = cssVar(c); return i; };
const row = (k, a, b) => h('tr', null, h('td', null, k), h('td', null, a), h('td', null, b));
async function run() { const el = PV.el; if (!el) return; if (PV.busy) { PV.q = 1; return; }
	for (const b of el.querySelectorAll('[data-sp]')) { const on = b.dataset.sp === PV.sp; b.classList.toggle('on', on); b.setAttribute('aria-pressed', on); }
	$('h2', el).textContent = `Preview${PV.kind === 'pre' ? '' : ' ' + PV.ch}: recorded vs. ${what()}`;
	const rq = body(), tok = JSON.stringify(rq), sp = rq.span; let r, err; PV.busy = 1; PV.q = 0;
	try { r = (await api('/api/preview', { method: 'POST', json: rq })).body; } catch (e) { err = e; }
	PV.busy = 0; if (el !== PV.el || PV.q || JSON.stringify(body()) !== tok) return run();
	const out = $('.pv-s', el);
	if (err) { for (const k in PV.w) PV.w[k].classList.add('pv-off'); out.textContent = err.message; return; }
	out.replaceChildren();
	r.order.forEach((n, i) => { const x = r.channels[n]; if (PV.kind !== 'pre' && n !== PV.ch) return; const notes = (x.notes || []).map(t => `${n}: ${t}`).concat(x.measured ? [x.measured] : []);
		const hint = () => notes.map(t => h('p', { class: 'hint sm' }, t));
		if (x.error) { if (PV.w[n]) PV.w[n].classList.add('pv-off'); return out.append(h('p', null, `${n}: no preview — ${x.error}`), ...hint()); }
		let w = PV.w[n]; if (!w) { w = PV.w[n] = h('div', { class: 'chart' }, h('canvas', { role: 'img', 'aria-label': `${n}: recorded duty and duty with the change` }), h('div', { class: 'tip', hidden: true }));
			w.lg = h('div', { class: 'legend' }); $('.pv-c', el).append(h('h3', null, n), w.lg, w); }
		w.classList.remove('pv-off'); const d = (k, f) => x.series.filter(p => p[k] !== null).map(p => [p.ts, f ? p.boost : p[k]]);
		// lines only, no fill: an area would read as a share. The boost line is drawn (and named) only when the change carries one
		const s = [{ name: 'recorded', color: '--fg3', data: d('actual') }, { name: 'with the change', color: seriesColor(i), data: d('preview') }];
		if (x.series.some(p => p.boost > 0)) s.push({ name: 'assist boost', color: '--man', data: d('preview', 1) });
		w.lg.replaceChildren(...s.map(z => h('span', null, key(z.color), z.name + (z.color === '--man' ? ' (duty points)' : ''))));
		chart(w, s, { fmt: v => Math.round(v), yMin: 0, yMax: 255, rg: RANGES[sp], ev: 1, lines: 1 });
		const a = x.summary.actual, p = x.summary.preview;
		out.append(h('table', { class: 'tbl sm pv-t' }, h('thead', null, h('tr', null, h('th', null, n), h('th', null, 'recorded'), h('th', null, 'with the change'))),
			h('tbody', null, row('mean duty', Math.round(a.mean), Math.round(p.mean)), row('P95 / max', `${a.p95} / ${a.max}`, `${p.p95} / ${p.max}`),
				row('≥ 200', `${a.min_ge_200} min`, `${p.min_ge_200} min`), row('assist active', `${a.assist_min} min`, `${p.assist_min} min`))), ...hint()); });
	$('.pv-n', el).textContent = r.note; }
return { open, close, sched, redraw: () => { for (const k in PV.w) PV.w[k]._st.draw(); } };
};
