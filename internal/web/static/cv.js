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
// restore: confirm with the summary, the restart-only keys and the warnings (sign-in off, listener off loopback, another user, the
// password), then the refresh an import does; quiet = the Undo toast (no question unless there is something to warn about); cur = the
// hash the file must still have (409: someone wrote meanwhile, nothing is undone)
async function restore(id, quiet, cur) {
	let d; try { d = (await api('/api/config/versions/' + id)).body; } catch (e) { return toast(e.message, 'err'); }
	const ch = d.changes || [], k0 = d.restart_keys || [], ws = (d.warnings || []).concat(d.password_changed ? ['This also resets the password to the one of that version (after a restart).'] : []);
	const sum = `${d.removed} line(s) go, ${d.added} come` + (ch.length ? ` (${ch.join(', ')})` : '') + '.' + (k0.length ? `\nApplies after a restart: ${k0.join(', ')}.` : '') + ws.map(w => '\nWarning: ' + w).join('');
	if ((!quiet || ws.length) && !await ask(`Restore version ${id}`, sum + '\nThe current state is kept as a version.', { ok: 'Restore', danger: ws.length > 0 })) return;
	let r; try { r = await api(`/api/config/versions/${id}/restore`, { method: 'POST', json: cur ? { if_current: cur } : {} }); }
	catch (e) { return e.status === 409 ? toast('Not undone: the config changed meanwhile — see Settings → Config versions', 'warn', UI.timing.toastLong) : toast(e.message, 'err', UI.timing.toastLong); }
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
// "Applied — Undo" for 10 s: id = the version the write stored, sha = the text it wrote; Undo restores id only while the file is unchanged
function undo(msg, id, sha) {
	const b = h('button', { class: 'btn sm', onclick: () => { b.closest('.toast').remove(); restore(id, 1, sha); } }, 'Undo');
	toast(h('span', { class: 'vs-u' }, msg + ' — ', b), 'ok', 10000);
}
return { show, undo };
};
