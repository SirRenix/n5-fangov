// n5-fangov dashboard — config versions (/api/config/versions): the Settings card and the Undo toast. A module loaded on demand
// (DESIGN 11a): app.js inserts it on first use and hands over what it needs; window.n5cv(X) returns { show, undo }.
'use strict';
window.n5cv = X => {
const { h, api, ask, toast, UI } = X;
const when = ts => new Date(ts * 1000).toLocaleString();
let host = null;
const list = async () => (await api('/api/config/versions')).body || [];
// diff current → version, changed lines only: red = goes (current file), green = comes (this version)
const diffEl = d => { const lines = (d.diff || []).filter(o => o.op !== ' ');
	return h('div', { class: 'vs-d' }, h('p', { class: 'hint sm' }, lines.length ? `Against the current file: ${d.removed} line(s) go (red), ${d.added} come (green)` + (d.password_changed ? ' · the password differs' : '') : 'Identical to the current file.'),
		lines.length ? h('pre', null, lines.map(o => h('span', { class: o.op === '+' ? 'add' : 'del' }, `${o.op} ${o.line}\n`))) : null); };
// restore: confirm with the summary (the password warning when the hash differs), then the refresh an import does; quiet = the Undo
// toast (no question unless the password would change)
async function restore(id, quiet) {
	let d; try { d = (await api('/api/config/versions/' + id)).body; } catch (e) { return toast(e.message, 'err'); }
	const ch = d.changes || [], sum = `${d.removed} line(s) go, ${d.added} come` + (ch.length ? ` (${ch.join(', ')})` : '') + '.';
	if ((!quiet || d.password_changed) && !await ask(`Restore version ${id}`, sum + (d.password_changed ? '\nThis also resets the password to the one of that version (takes effect after a restart).' : '') + '\nThe current state is kept as a version.', { ok: 'Restore', danger: !!d.password_changed })) return;
	let r; try { r = await api(`/api/config/versions/${id}/restore`, { method: 'POST' }); } catch (e) { return toast(e.message, 'err', UI.timing.toastLong); }
	const b = r.body || {}, k = b.restart_keys || [], w = b.restart_required || k.length;
	toast(`Version ${id} restored` + (b.restart_required ? ' — restart required (channel set or profile)' : '') + (k.length ? ` — applies after restart: ${k.join(', ')}` : ''), w ? 'warn' : 'ok', w ? UI.timing.toastLong : 0);
	await X.cfgRefresh(); if (host && host.isConnected) show(host);
}
async function show(el) { host = el; let l;
	try { l = await list(); } catch (e) { return el.replaceChildren(h('p', { class: 'notice err' }, e.message)); }
	if (!l.length) return el.replaceChildren(h('p', { class: 'hint sm' }, 'No versions yet — the first change through the daemon keeps the text before it.'));
	const tb = h('tbody');
	for (const v of l) { const dr = h('tr', { class: 'vs-x', hidden: true }, h('td', { colspan: 5 }));
		const diff = async () => { if (!dr.hidden) return (dr.hidden = true);
			try { dr.firstChild.replaceChildren(diffEl((await api('/api/config/versions/' + v.id)).body)); dr.hidden = false; } catch (e) { toast(e.message, 'err'); } };
		tb.append(h('tr', null, h('td', null, h('b', null, v.id)), h('td', null, when(v.ts), h('br'), h('span', { class: 'hint sm' }, v.by || (v.reason === 'external edit' ? 'by hand' : 'daemon'))),
			h('td', null, v.reason), h('td', null, (v.sections || []).join(', ') || '—'),
			h('td', { class: 'vs-a' }, h('button', { class: 'btn sm', onclick: diff }, 'Diff'), h('button', { class: 'btn sm', onclick: () => restore(v.id) }, 'Restore'))), dr); }
	el.replaceChildren(h('div', { class: 'tbl-wrap' }, h('table', { class: 'tbl sm vs' }, h('thead', null, h('tr', null, ...['#', 'Taken', 'Replaced by', 'Changed after', ''].map(t => h('th', { scope: 'col' }, t)))), tb)));
}
// "Applied — Undo" for 10 s: only when the write stored a new version (newer than before, the id app.js read ahead); Undo restores it
async function undo(msg, before) { let id = 0; try { const l = await list(); id = l.length && l[0].id > before ? l[0].id : 0; } catch (e) {}
	if (!id) return toast(msg, 'ok');
	const b = h('button', { class: 'btn sm', onclick: () => { b.closest('.toast').remove(); restore(id, 1); } }, 'Undo');
	toast(h('span', { class: 'vs-u' }, msg + ' — ', b), 'ok', 10000);
}
return { show, undo };
};
