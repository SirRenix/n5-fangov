// n5-fangov dashboard mock — loaded by app.js only with ?mock=1, never referenced by index.html.
// Publishes window.n5mock(path, opt) → Promise<{status, body, filename?}>; api() calls it instead of fetch.
// Flags: &user=1 &auth=none &tls=off|file|soon|fallback &tab= &syserr=1 &reject=1 &restart=1 &expire=1 &schedfail=1 &pwm4=1 &lag=1 &down=1
// Names and addresses are documentation values (n5host, 192.0.2.x, n5.lan, example.test).
'use strict';
window.n5mock = (() => {
	const Q = new URLSearchParams(location.search), t0 = Date.now() / 1000, MV = '0.4.0-rc5', PRE = MV.split('-')[1] || '';
	const interp = (curve, t) => { if (!curve.length) return 0; if (t <= curve[0][0]) return curve[0][1];
		for (let i = 1; i < curve.length; i++) if (t <= curve[i][0]) { const [t0, d0] = curve[i - 1], [t1, d1] = curve[i]; return t1 === t0 ? d1 : d0 + (d1 - d0) * (t - t0) / (t1 - t0); }
		return curve[curve.length - 1][1]; };
	const REF = { cpu: [[85, 2000], [140, 3120], [179, 3830], [217, 4445], [255, 5073]], ssd: [[74, 2130], [140, 3280], [179, 3790], [217, 4230], [255, 4687]], hdd: [[87, 1237], [105, 1650], [140, 2250], [179, 2725], [217, 3160], [255, 3540]] };
	const M = { auth: Q.get('auth') === 'none' ? 'none' : 'basic', in: Q.get('user') === '1' || Q.get('auth') === 'none', user: 'admin', remember: false, exp: !!Q.get('expire') };
	const cfg = { daemon: { interval: '10s', step_up: 40, step_down: 15, stall_min_duty: 60, stall_cycles: 2, profile: 'auto' },
		web: { listen: '0.0.0.0:8010', auth: M.auth }, log: { file: '/var/log/n5-fangov/n5-fangov.log', max_size_mb: 10, max_files: 5 },
		channel: [
			{ name: 'cpu', pwm: 1, sensor: 'k10temp', curve: [[45, 85], [80, 255]], critical: 88, stop: 'auto', hysteresis: 0, min_on: '0s' },
			{ name: 'ssd', pwm: 2, sensor: 'nvme:max', curve: [[40, 74], [70, 255]], critical: 75, stop: 'auto', hysteresis: 2, min_on: '1m0s' },
			{ name: 'hdd', pwm: 3, sensor: 'drivetemp:max,disk:sda', curve: [[36, 105], [46, 255]], critical: 56, stop: 87, hysteresis: 3, min_on: '5m0s' }],
		schedule: [{ preset: 'n5pro-quiet', from: '22:00', to: '07:00', days: [] }, { preset: 'n5pro-cool', from: '13:00', to: '18:00', days: ['sat', 'sun'] }, { preset: 'n5pro-balanced', from: '', to: '', days: [] }] };
	if (Q.get('pwm4') === '1') cfg.channel.push({ name: 'pcie', pwm: 4, sensor: 'ec:board', curve: [[30, 60], [60, 200]], critical: 80, stop: 120, hysteresis: 0, min_on: '0s' });
	if (Q.get('schedfail')) cfg.schedule[0].preset = 'night'; // the failed switch below names this entry: a preset the store lacks ("(missing)" in the editor)
	const shift = n => cfg.channel.map(c => Object.assign({}, c, { curve: c.curve.map(p => [p[0] + n, p[1]]) }));
	const presets = {
		'n5pro-balanced': { builtin: true, description: 'Recommended: HDDs held near 40 °C, audible under load only', ch: cfg.channel },
		'n5pro-quiet': { builtin: true, description: 'Quiet: lowest noise, HDDs around 45 °C', ch: shift(4) }, 'n5pro-cool': { builtin: true, description: 'Cool: drives first', ch: shift(-6) }, summer: { ch: shift(-4) },
		// a user preset that shares cpu and ssd with n5pro-balanced (only the hdd curve differs): the channel badges list both, the active set names one
		alternative: { ch: [cfg.channel[0], cfg.channel[1]].concat(shift(2).slice(2, 3)) } };
	const overrides = {}, ovLag = {};
	// day/night shape (peak in the afternoon) on top of the short-period wobble, so 24 h / 7 d look plausible
	const day = t => Math.sin(((t / 86400) % 1 - .3) * 2 * Math.PI);
	const temp = (name, t) => ({ cpu: 38 + 9 * Math.sin(t / 900) + 3 * Math.sin(t / 130) + 3 * day(t), ssd: 41 + 4 * Math.sin(t / 1400 + 1) + 2 * day(t), hdd: 39 + 2.5 * Math.sin(t / 2600 + 2) + 2.5 * day(t), pcie: 34 + 2 * Math.sin(t / 700) + 2 * day(t) })[name];
	// sensor catalogue: id → [description, base curve, offset, kind]
	const SENS = { k10temp: ['CPU Tctl', 'cpu', 0], 'nvme:max': ['hottest NVMe', 'ssd', 0], 'drivetemp:max': ['hottest drive', 'hdd', 0], 'disk:nvme0n1': ['Example NVMe 1000GB (nvme0n1, nvme)', 'ssd', -1, 'ssd'], 'disk:nvme2n1': ['Example NVMe 2000GB (nvme2n1, nvme)', 'ssd', .5, 'ssd'],
		'disk:sda': ['EX HDD 20TB (sda, drivetemp)', 'hdd', 0, 'hdd'], 'disk:sdc': ['EX HDD 8TB (sdc, drivetemp)', 'hdd', -1.5, 'hdd'], 'ec:ambient': ['EC ambient', 'sys', -6], 'ec:board': ['EC mainboard', 'sys', 2], 'ec:cpu': ['EC CPU probe', 'cpu', 1.5],
		'ec:system': ['EC system', 'sys', 0], 'hwmon:acpitz:temp1': ['ACPI thermal zone', 'sys', 5], 'hwmon:nic1:temp1': ['NIC PHY', 'sys', 18],
		'hwmon:spd5118:temp1': ['DIMM 0 SPD', 'sys', 8], 'hwmon:amdgpu:temp1': ['iGPU edge', 'cpu', -3], 'hwmon:minisforum_n5_it5571:temp1': ['EC chip', 'sys', 4] };
	const PAT = [{ id: 'hwmon:<name>:tempN', description: 'any hwmon device by name and temperature index' }, { id: 'ec:<label>', description: 'extra temperature of the detected fan controller profile' }, { id: 'disk:<dev>', description: 'one block device by name (/sys/block/<dev>)' }];
	const sv = (id, t) => { const [, src, off] = SENS[id]; return +((src === 'sys' ? 32 + 1.5 * Math.sin(t / 700) : temp(src, t)) + off).toFixed(1); };
	let dash = ['hwmon:amdgpu:temp1', 'hwmon:nic1:temp1'];
	const rpmOf = (name, d) => REF[name] ? Math.round(interp(REF[name], d) + 20 * Math.sin(d)) : -1;
	const point = t => { const p = { ts: Math.floor(t), temp: {}, duty: {}, rpm: {} };
		for (const c of cfg.channel) { const tv = temp(c.name, t); const d = c.name in overrides ? overrides[c.name] : Math.round(interp(c.curve, tv));
			p.temp[c.name] = +tv.toFixed(1); p.duty[c.name] = d; p.rpm[c.name] = rpmOf(c.name, d); }
		if (dash.length) { p.extra = {}; for (const id of dash) if (SENS[id]) p.extra[id] = sv(id, t); } return p; };
	// averaged tiers (DESIGN 6a): ≤ 2 h raw at 10 s, ≤ 24 h 1-min means, else 5-min means
	const history = (minutes, since, extra) => { const now = Date.now() / 1000, step = minutes <= 120 ? 10 : minutes <= 1440 ? 60 : 300, out = [];
		for (let t = Math.floor((now - minutes * 60) / step) * step; t <= now; t += step) if (t > since) { const pt = point(t); if (!extra) delete pt.extra; out.push(pt); } return out; };
	const csv = minutes => { const chs = cfg.channel.map(c => c.name), ex = dash.filter(id => SENS[id]).sort(), pts = history(minutes, 0, true);
		const rows = [['ts', 'time', ...chs.flatMap(n => [n + '_temp', n + '_duty', n + '_rpm']), ...ex].join(',')];
		for (const p of pts) rows.push([p.ts, new Date(p.ts * 1000).toISOString(), ...chs.flatMap(n => [p.temp[n], p.duty[n], p.rpm[n]]), ...ex.map(id => p.extra && p.extra[id] !== undefined ? p.extra[id] : '')].join(','));
		return rows.join('\n') + '\n'; };
	const tomlV = v => typeof v === 'string' ? `"${v}"` : Array.isArray(v) ? `[${v.map(tomlV).join(', ')}]` : v;
	const sec = n => `[${n}]\n` + Object.entries(cfg[n]).map(([k, v]) => `${k} = ${tomlV(v)}`).join('\n') + '\n\n';
	const tomlCh = c => `[[channel]]\nname = "${c.name}"\npwm = ${c.pwm}\nsensor = ${c.sensor.includes(',') ? tomlV(c.sensor.split(',')) : tomlV(c.sensor)}\ncurve = [${c.curve.map(p => `[${p[0]}, ${p[1]}]`).join(', ')}]\ncritical = ${c.critical}\nstop = ${c.stop === 'auto' ? '"auto"' : c.stop}\n`
		+ (c.hysteresis ? `hysteresis = ${c.hysteresis}\n` : '') + (c.min_on && c.min_on !== '0s' ? `min_on = "${c.min_on}"\n` : '');
	const tomlSched = s => `[[schedule]]\npreset = "${s.preset}"\n` + (s.from ? `from = "${s.from}"\nto = "${s.to}"\n` : '') + (s.days.length ? `days = ${tomlV(s.days)}\n` : '');
	const raw = () => sec('daemon') + sec('web') + sec('log') + cfg.channel.map(tomlCh).join('\n') + '\n' + cfg.schedule.map(tomlSched).join('\n');
	// PUT /api/config: parse the [[channel]] and [[schedule]] tables back (strict: the daemon's rules, rule 8 would substitute defaults with a warning);
	// like the daemon, the mock keeps only what the body carries — a client that drops the [[schedule]] tables loses them (visible in raw() and on the Schedules card)
	const kv = (blk, k) => { const m = new RegExp(`^${k}\\s*=\\s*(.+)$`, 'm').exec(blk); return m ? m[1].trim() : ''; };
	const tables = (body, name) => body.split(new RegExp(`^\\[\\[${name}\\]\\]\\s*$`, 'm')).slice(1).map(b => b.split(/^\[/m)[0]);
	const parseSchedules = body => tables(body, 'schedule').map(b => ({ preset: kv(b, 'preset').replace(/"/g, ''), from: kv(b, 'from').replace(/"/g, ''), to: kv(b, 'to').replace(/"/g, ''), days: [...kv(b, 'days').matchAll(/"([^"]*)"/g)].map(x => x[1]) }));
	const parseChannels = body => tables(body, 'channel').map(b => { const s = kv(b, 'sensor'), mo = kv(b, 'min_on'), st = kv(b, 'stop');
		return { name: kv(b, 'name').replace(/"/g, ''), pwm: +kv(b, 'pwm'), sensor: s.startsWith('[') ? [...s.matchAll(/"([^"]*)"/g)].map(x => x[1]).join(',') : s.replace(/"/g, ''),
			curve: [...kv(b, 'curve').matchAll(/\[\s*(-?[\d.]+)\s*,\s*(-?[\d.]+)\s*\]/g)].map(x => [+x[1], +x[2]]), critical: +kv(b, 'critical'), stop: /^"auto"$/i.test(st) ? 'auto' : /^\d+$/.test(st) ? +st : st.replace(/"/g, ''),
			hysteresis: +kv(b, 'hysteresis') || 0, min_on: mo ? mo.replace(/"/g, '') : '0s' }; });
	// the daemon's channel rules (config.ValidateCurve, parser.stop, intField / durField): every warning is an error here
	const durS = s => { const m = /^(?:(\d+)h)?(?:(\d+)m)?(?:(\d+)s)?$/.exec(s); return m && m[0] ? (+m[1] || 0) * 3600 + (+m[2] || 0) * 60 + (+m[3] || 0) : -1; };
	const check = chs => { const errs = [];
		for (const c of chs) { const n = c.name, pts = c.curve;
			if (pts.length < 2 || pts.length > 8) errs.push(`channel ${n}: curve: ${pts.length} points, need 2..8`);
			pts.forEach((p, i) => { if (!Number.isInteger(p[0]) || !Number.isInteger(p[1])) errs.push(`channel ${n}: point ${i} is not [temp, duty] integers`);
				else if (p[0] < -20 || p[0] > 120) errs.push(`channel ${n}: point ${i} temp ${p[0]} outside -20..120`);
				else if (p[1] < 0 || p[1] > 255) errs.push(`channel ${n}: point ${i} duty ${p[1]} outside 0..255`);
				else if (i && p[0] <= pts[i - 1][0]) errs.push(`channel ${n}: point ${i} temp ${p[0]} not above previous ${pts[i - 1][0]}`);
				else if (i && p[1] < pts[i - 1][1]) errs.push(`channel ${n}: point ${i} duty ${p[1]} below previous ${pts[i - 1][1]}`); });
			const last = pts.length ? pts[pts.length - 1][0] : 0; if (!Number.isInteger(c.critical) || c.critical < last + 1 || c.critical > 150) errs.push(`channel ${n}: critical ${c.critical} out of range ${last + 1}..150`);
			const st = c.stop === '' || /^auto$/i.test(String(c.stop)) ? 'auto' : +c.stop; if (st !== 'auto' && !(Number.isInteger(st) && st >= 60 && st <= 255)) errs.push(`channel ${n}: stop "${c.stop}" is neither auto nor a fixed duty 60..255`);
			if (!Number.isInteger(c.hysteresis) || c.hysteresis < 0 || c.hysteresis > 10) errs.push(`channel ${n}: hysteresis ${c.hysteresis} out of range 0..10`);
			const mo = durS(c.min_on); if (mo < 0) errs.push(`channel ${n}: min_on "${c.min_on}" is not a duration`); else if (mo > 3600) errs.push(`channel ${n}: min_on ${c.min_on} above 1h0m0s`); }
		if (Q.get('reject')) errs.push('channel cpu: sensor "k10temp" not found (mock &reject=1)'); return errs; };
	const logs = [];
	for (let i = 0; i < 200; i++) { const t = t0 - (200 - i) * 300; const p = point(t);
		logs.push(`${new Date(t * 1000).toISOString().slice(0, 19)} ${i % 37 === 5 ? 'WARN stall: hdd rpm=0 at duty=105' : i % 53 === 7 ? 'ERROR sensor drivetemp:max: no devices' : i % 71 === 9 ? 'INFO schedule: preset "n5pro-quiet" applied (22:00–07:00)' : 'INFO'} cpu ${p.temp.cpu}/${p.duty.cpu} hdd ${p.temp.hdd}/${p.duty.hdd}`); }
	const wait = v => new Promise(r => setTimeout(r, 120, v)), ok = (body, filename) => wait({ status: 200, body: typeof body === 'string' ? body : JSON.parse(JSON.stringify(body)), filename }); // a copy, as a fetch would deliver: the page never holds the mock's live objects
	const q = Q.get('tls'), T = { mode: q === 'off' || q === 'file' || q === 'fallback' ? (q === 'fallback' ? 'file' : q) : 'auto', fb: q === 'fallback', n: 0 };
	const tlsInfo = () => { const up = T.mode === 'file' && !T.fb, cn = up ? 'CN=fans.example,O=Homelab' : 'CN=n5.lan,O=n5-fangov', d = new Date(t0 * 1000); d.setFullYear(d.getFullYear() + (up ? 1 : 10));
		return { subject: cn, issuer: up ? 'CN=Homelab CA' : cn, dns_names: up ? ['fans.example'] : ['n5.lan', 'n5host', 'localhost'], ips: up ? [] : ['192.0.2.20', '127.0.0.1', '::1'],
			not_before: new Date(t0 * 1000 - 36e5).toISOString(), not_after: q === 'soon' ? new Date(t0 * 1000 + 12 * 864e5).toISOString() : d.toISOString(), is_ca: !up, key_algo: up ? 'RSA 2048' : 'ECDSA P-256',
			serial_hex: '3F0' + T.n + 'A9C1', fingerprint_sha256: Array.from({ length: 32 }, (_, i) => ((i * 37 + T.n * 11) % 256 | 256).toString(16).slice(1).toUpperCase()).join(':') }; };
	const fail = (msg, status, extra) => Promise.reject(Object.assign(new Error(msg + (extra && extra.errors ? '\n' + extra.errors.join('\n') : '')), { status, body: Object.assign({ error: msg }, extra || {}) }));
	const lacks = (...hs) => hs.map(x => 'SAN list lacks host ' + x);
	const mockTLS = (p, opt) => {
		if (p === '/api/tls') return ok({ mode: T.fb ? 'auto (fallback from file)' : T.mode, fallback: !!T.fb, info: T.mode === 'off' ? null : tlsInfo(), hosts: ['192.0.2.20', 'n5.lan', 'n5host', 'localhost'],
			warnings: T.mode === 'file' && !T.fb ? lacks('192.0.2.20', 'n5.lan', 'n5host') : q === 'soon' ? ['certificate expires in 12 days'] : [] });
		if (T.mode === 'off') return fail('tls is off', 409);
		if (p === '/api/tls/cert.crt') return ok('-----BEGIN CERTIFICATE-----\nMIIBmock\n-----END CERTIFICATE-----\n', 'n5-fangov-n5host.crt');
		if (p === '/api/tls/cert.cer') return ok('0mock', 'n5-fangov-n5host.cer');
		if (p === '/api/tls/regenerate') { if (T.mode === 'file') return fail('custom certificate active; reset to auto first', 409);
			T.n++; const keep = !opt.json || opt.json.keep_key !== false; return ok({ ok: true, keep_key: keep, info: tlsInfo(), warning: keep ? undefined : 'new private key: re-download and trust the certificate' }); }
		if (p === '/api/tls/upload') { const j = opt.json || {}; if (!/BEGIN CERTIFICATE/.test(j.cert || '') || !/PRIVATE KEY/.test(j.key || '')) return fail('certificate: no PEM CERTIFICATE block', 400);
			if (!j.force) return fail('certificate does not cover "' + location.hostname + '"', 400, { host: location.hostname, force_required: true });
			T.mode = 'file'; T.fb = false; T.n++; return ok({ ok: true, mode: 'file', info: tlsInfo(), warnings: lacks('192.0.2.20', 'n5host') }); }
		if (p === '/api/tls/reset') { T.mode = 'auto'; T.fb = false; return ok({ ok: true, mode: 'auto', info: tlsInfo() }); }
		return fail('mock: not found ' + p, 404);
	};
	// alerts panel (webhook: effective only with a URL; the URL is returned as stored — the log redacts, the API does not)
	const A = { transport: 'auto', mail_to: 'root', webhook_url: '', webhook_format: 'json', tpl: { installed: true, current: true, writable: true, path: '/etc/pve/notification-templates/default' } };
	const KINDS = { sensor: 'sensor unreadable', stall: 'fan at 0 rpm', temp: 'critical temp', write: 'pwm write failed', device: 'hwmon device vanished', config: 'config replaced', 'config-channels': 'channel set changed', profile: 'profile changed',
		start: 'daemon started', restart: 'restarted', failed: 'unit failed', kernel: 'kernel/DKMS changed', tls: 'cert unreadable', web: 'web listener failed', schedule: 'scheduled preset switch failed', test: 'test alert' };
	const recent = [[720, 'stall', 'hdd: rpm=0 at duty 105, raised to 255'], [5400, 'temp', 'cpu 91.5 °C ≥ critical 88, forced to 255', 'pve-notify: exit status 1'], [11220, 'sensor', 'drivetemp:max: no devices'], [93600, 'start', 'n5-fangov ' + MV + ' started'], [3 * 86400, 'tls', 'certificate unreadable']]
		.map(([ago, kind, msg, error]) => Object.assign({ ts: Math.floor(t0 - ago), kind, msg }, error ? { error } : {}));
	if (Q.get('schedfail')) recent.unshift({ ts: Math.floor(t0 - 3600), kind: 'schedule', msg: 'preset "night" not found (22:00–07:00 kept the previous curves)' });
	const effective = () => A.transport === 'auto' || A.transport === 'pve' ? 'pve-notify' : A.transport === 'webhook' ? (A.webhook_url ? 'webhook' : 'log') : A.transport;
	const alertStatus = () => ({ transport: A.transport, effective: effective(), mail_to: A.mail_to, webhook_url: A.webhook_url, webhook_format: A.webhook_format, pve_available: true, mail_available: false, template: Object.assign({}, A.tpl), cooldown: '30m0s', kinds: Object.entries(KINDS).map(([kind, description]) => ({ kind, description })) });
	const iso = ago => new Date((t0 - ago) * 1000).toISOString();
	const sessions = () => [{ id: 'a1b2c3d4', created: iso(5400), expires: iso(5400 - (M.remember ? 30 : .5) * 86400), last_seen: iso(30), remember: M.remember, ip: '192.0.2.30', current: true },
		{ id: '9f8e7d6c', created: iso(6 * 86400), expires: iso(-24 * 86400), last_seen: iso(4 * 3600), remember: true, ip: '192.0.2.31', current: false }];
	// API tokens (DESIGN 9): secret n5t_ + 43 chars, shown once; name unique (409); expired ones stay listed until revoked
	const TOK = [{ id: '3fa9c1d2', name: 'home-assistant', scope: 'control', created: iso(20 * 86400), expires: iso(-70 * 86400), last_used: iso(120), last_ip: '192.0.2.40', expired: false },
		{ id: '8b1e77aa', name: 'grafana', scope: 'read', created: iso(200 * 86400), expires: iso(110 * 86400), last_used: iso(111 * 86400), last_ip: '192.0.2.41', expired: true },
		{ id: 'c0ffee12', name: 'backup script', scope: 'admin', created: iso(3 * 86400), expires: null, last_used: null, last_ip: '', expired: false }];
	const secret = () => 'n5t_mock' + Array.from({ length: 35 }, () => 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_'[Math.random() * 64 | 0]).join('');
	const mockTokens = (p, m, opt) => {
		if (m === 'GET') return ok({ tokens: TOK.map(t => Object.assign({}, t)) });
		if (m === 'POST') { const j = opt.json || {}, name = String(j.name || ''), scope = j.scope || 'read', ttl = j.ttl_days === undefined ? 90 : +j.ttl_days;
			if (!/^[A-Za-z0-9][A-Za-z0-9 ._-]{0,31}$/.test(name)) return fail('name: letters, digits, space, . _ - (1..32, no leading punctuation)', 400);
			if (!/^(read|control|admin)$/.test(scope)) return fail('scope: read, control or admin', 400);
			if (!(ttl >= 0 && ttl <= 3650)) return fail('ttl_days: 0..3650', 400);
			if (TOK.some(t => t.name === name)) return fail('token name taken', 409); if (TOK.length >= 50) return fail('at most 50 tokens', 409);
			const t = { id: (Math.random() * 0xffffffff >>> 0).toString(16).padStart(8, '0'), name, scope, created: iso(0), expires: ttl ? iso(-ttl * 86400) : null, last_used: null, last_ip: '', expired: false }; TOK.unshift(t);
			return wait({ status: 201, body: Object.assign({ ok: true, token: secret(), id: t.id, name, scope, expires: t.expires }, ttl ? {} : { warning: 'token never expires' }) }); }
		if (m === 'DELETE') { const id = p.split('/')[3], i = TOK.findIndex(t => t.id === id); if (i < 0) return fail('no such token', 404); TOK.splice(i, 1); return ok({ ok: true, revoked: id }); }
		return fail('method not allowed', 405);
	};
	// schedules (DESIGN 6b): active = first window containing the local time, else the fallback; next = next window edge
	// timezone like the daemon's zoneLabel: abbreviation + offset ("CEST +02:00"), so the Schedules clock can show the host's local time
	const zoneLabel = () => { const d = new Date(), o = -d.getTimezoneOffset(), a = Math.abs(o), n = (Intl.DateTimeFormat('en-GB', { timeZoneName: 'short' }).formatToParts(d).find(p => p.type === 'timeZoneName') || {}).value || 'UTC';
		return `${n} ${o < 0 ? '-' : '+'}${String(a / 60 | 0).padStart(2, '0')}:${String(a % 60).padStart(2, '0')}`; };
	const DAYS =['sun', 'mon', 'tue', 'wed', 'thu', 'fri', 'sat'], hm = s => { const [hh, mm] = s.split(':'); return +hh * 60 + +mm; };
	const inWin = (e, d) => { if (!e.from) return false; const t = d.getHours() * 60 + d.getMinutes(), f = hm(e.from), to = hm(e.to), day = to < f && t < to ? new Date(d.getTime() - 864e5) : d;
		return (to < f ? t >= f || t < to : t >= f && t < to) && (!e.days.length || e.days.includes(DAYS[day.getDay()])); };
	const activeIdx = d => { const i = cfg.schedule.findIndex(e => inWin(e, d)); return i >= 0 ? i : cfg.schedule.findIndex(e => !e.from); };
	const schedStatus = () => { const now = new Date(), cur = activeIdx(now); let next = null;
		for (let m = 1; m <= 8 * 1440 && !next; m++) { const d = new Date(now.getTime() + m * 60000); d.setSeconds(0, 0); const i = activeIdx(d); if (i !== cur) next = { ts: Math.floor(d / 1000), preset: i >= 0 ? cfg.schedule[i].preset : '' }; }
		const bad = !!Q.get('schedfail');
		return { entries: cfg.schedule.map((e, i) => Object.assign({}, e, { fallback: !e.from, active: i === cur })), active: cur, next,
			last: { ts: Math.floor(t0 - 3600), preset: bad ? 'night' : 'n5pro-balanced', ok: !bad, error: bad ? 'preset "night" not found' : '' }, timezone: zoneLabel() }; };
	// system inventory (generic names, documentation MACs); disk temp_c live from the same hwmon disk:<dev> reads, null without one
	const G = 1 << 30, sysMock = now => ({ host: { hostname: 'n5host', os: 'Debian GNU/Linux 13 (trixie)', kernel: '7.0.12-1-pve', uptime_s: 435723, load1: +(.6 + .3 * Math.sin(now / 300)).toFixed(2), load5: .7, load15: .66 },
		machine: { vendor: 'Example Vendor', product: 'N5-class mini server', board: 'EXB-01', board_vendor: 'Example Boards Ltd', bios_version: '1.05', bios_date: '03/31/2026' },
		cpu: { model: 'Example Ryzen-class 12-core APU w/ Radeon-class iGPU', sockets: 1, cores: 12, threads: 24, max_mhz: 5157 },
		memory: { total_bytes: 62.4 * G, available_bytes: (34 + 4 * Math.sin(now / 400)) * G, swap_total_bytes: 8 * G, swap_free_bytes: 8 * G, installed_bytes: 96 * G, smbios: '3.7',
			modules: ['P0 CHANNEL A', 'P0 CHANNEL B'].map(bank => ({ slot: 'DIMM 0', bank, size_bytes: 48 * G, type: 'DDR5', form_factor: 'SODIMM', speed_mts: 5600, manufacturer: 'Example Memory', part: 'EX-DDR5-48G-5600', ecc: true })) },
		gpus: [{ name: 'Example iGPU [Radeon-class 890M]', vendor: 'EXS/GFX', pci: '0000:c7:00.0', driver: 'amdgpu' }],
		npus: [{ name: 'Example Neural Processing Unit', pci: '0000:c8:00.1', driver: 'amdxdna', driver_version: '2.23.0_20260412,0000000000000000000000000000000000000000', accel: 'accel0' }],
		nics: [{ name: 'nic0', pci: '0000:c5:00.0', model: 'EX8126 5GbE Controller', driver: 'r8169', speed_mbit: -1, state: 'down', duplex: '', mac: '02:00:00:00:00:01', mtu: 1500 },
			{ name: 'nic1', pci: '0000:c4:00.0', model: 'EXN113 NBase-T/IEEE 802.3an Ethernet Controller [10G]', driver: 'atlantic', speed_mbit: 2500, state: 'up', duplex: 'full', mac: '02:00:00:00:00:02', mtu: 1500 }],
		storage: { controllers: [{ kind: 'sata', name: 'EXB58x AHCI SATA controller', pci: '0000:c1:00.0', driver: 'ahci' }, { kind: 'nvme', name: 'Example NVMe SSD Controller', pci: '0000:c2:00.0', driver: 'nvme' }, { kind: 'nvme', name: 'Example NVMe SSD Controller', pci: '0000:c3:00.0', driver: 'nvme' }],
			disks: [['nvme0n1', 'Example NVMe 1000GB', 1000204886016, 0, 'nvme', 'disk:nvme0n1'], ['nvme1n1', 'Example NVMe 1000GB', 1000204886016, 0, 'nvme', 'ssd', 2], ['nvme2n1', 'Example NVMe 2000GB', 2000398934016, 0, 'nvme', 'disk:nvme2n1'],
				['sda', 'EX HDD 20TB', 20000588955648, 1, 'sata', 'disk:sda'], ['sdb', 'EX HDD 20TB', 20000588955648, 1, 'sata', 'hdd', 1], ['sdc', 'EX HDD 8TB', 8001563222016, 1, 'sata', 'disk:sdc'], ['sdd', 'EX SATA SSD 1TB', 1000204886016, 0, 'sata', '']]
				.map(([name, model, size_bytes, rot, transport, s, off]) => ({ name, model, size_bytes, rotational: !!rot, transport, temp_c: SENS[s] ? sv(s, now) : s ? +(temp(s, now) + off).toFixed(1) : null, sensor: SENS[s] ? s : '' })) },
		fan_controller: { profile: 'n5pro', hwmon: '/sys/class/hwmon/hwmon14', module: 'minisforum_n5_it5571', module_version: '0.2.0' },
		collected: Math.floor(now), static_at: Math.floor(t0), errors: Q.get('syserr') ? ['lspci: exec: "lspci": executable file not found in $PATH (device names from ids only)'] : [] });
	const openapi = () => ({ openapi: '3.1.0', info: { title: 'n5-fangov API', version: MV }, servers: [{ url: '/' }],
		components: { securitySchemes: { bearer: { type: 'http', scheme: 'bearer' }, basic: { type: 'http', scheme: 'basic' }, cookie: { type: 'apiKey', in: 'cookie', name: 'n5fangov_session' } } },
		paths: Object.fromEntries([['/api/state', 'get', 'Regulation snapshot', 'read'], ['/api/history', 'get', 'History points (minutes, since)', 'read'], ['/api/history.csv', 'get', 'History as CSV', 'read'], ['/api/override/{name}', 'put', 'Manual duty', 'control'],
			['/api/presets/{name}/apply', 'post', 'Apply a preset', 'control'], ['/api/schedules', 'get', 'Schedule status', 'read'], ['/api/tokens', 'get', 'List API tokens (session only)', '']]
			.map(([path, m, summary, scope]) => [path, { [m]: { summary, 'x-scope': scope, responses: { 200: { description: 'OK' } } } }])) });
	const GH = 'https://github.com/', PUB = /^\/api\/(version|about|session|login|logout|state|history|openapi\.json)$/;
	return (path, opt) => {
		opt = opt || {};
		const m = opt.method || 'GET', u = new URL(path, location.origin), p = u.pathname, now = Date.now() / 1000;
		if (p === '/api/session') return ok({ authenticated: M.in, mode: M.auth, user: M.in ? M.user : undefined, remember: M.remember, via: M.in && M.auth === 'basic' ? 'cookie' : 'none' });
		if (p === '/api/login') { const j = opt.json || {}; if (j.user !== M.user || j.password !== 'admin') return fail('invalid user or password', 401);
			M.in = true; M.remember = !!j.remember; return ok({ ok: true, user: M.user, expires: Math.floor(now + (M.remember ? 30 : .5) * 86400), remember: M.remember }); }
		if (p === '/api/logout') { M.in = M.auth === 'none'; return wait({ status: 204, body: null }); }
		if (p === '/api/version') return ok({ name: 'n5-fangov', version: MV, prerelease: PRE, auth: M.auth, tls: T.mode !== 'off',
			limits: { min_hdd_override: 60, critical_min: 30, critical_max: 150, curve_points_max: 8, dashboard_sensors_max: 8, password_min: 8, password_max: 128, hysteresis_max: 10, min_on_max_s: 3600 } });
		if (p === '/api/about') return ok({ name: 'n5-fangov', version: MV, prerelease: PRE, license: 'GPL-2.0-only', license_url: 'https://www.gnu.org/licenses/old-licenses/gpl-2.0.html',
			repo: GH + 'SirRenix/n5-fangov', author: 'SirRenix', author_url: GH + 'SirRenix', go: M.in ? 'go1.25.1' : '',
			credits: [['ltdstudio/minisforum-n5-it5571', 'the kernel driver'], ['Sl0thC0der/proxfansx', 'dashboard idea; nct67xx/it87xx profiles']].map(([name, note]) => ({ name, url: GH + name, note })) });
		if (p === '/api/openapi.json') return ok(openapi());
		if (M.in && M.exp && now - t0 > 15 && p === '/api/state') { M.in = M.exp = false; return fail('unauthorized', 401); } // &expire=1: session dies once after 15 s
		if (Q.get('down') === '1' && now - t0 > 2 && /^\/api\/(state|history|sensors)$/.test(p)) return fail('network: mock down', 0); // &down=1: after the boot the poll path fails like a lost daemon (status 0 = no answer) → connection banner
		if (p === '/api/state') { const pt = point(now), stall = (now | 0) % 40 < 3, hold = (now | 0) % 300 < 90;
			const body = { ts: pt.ts, status: 'ok', profile: 'n5pro', verified: true, dry_run: false, uptime_s: 435723,
				// like the daemon: an override replaces the target and the mode, stall and critical win on top; &lag=1 reports the previous
				// override state for two more polls (the daemon's snapshot follows a PUT/DELETE only with the next cycle)
				channels: cfg.channel.map(c => { const n = c.name, st = n === 'hdd' && stall, lg = ovLag[n], ov = lg && lg.left-- > 0 ? lg.ov : overrides[n]; if (lg && lg.left <= 0) delete ovLag[n];
					const cd = Math.round(interp(c.curve, pt.temp[n])), duty = ov === undefined ? cd : ov; // curve duty while the lagged state says auto; rpm follows the (lagged) duty, as on the daemon
					const ch = { name: n, pwm: c.pwm, sensor: c.sensor, temp: pt.temp[n], duty, target: ov === undefined ? n === 'cpu' ? cd + 22 : cd : ov, rpm: st ? 0 : rpmOf(n, duty),
					mode: st ? 'stall' : ov !== undefined ? 'manual' : 'auto' };
					// hysteresis: the held reading lags the raw one; min_on: a running hold now and then
					if (c.hysteresis) { const held = Math.round(pt.temp[n]) - 1; if (Math.abs(held - pt.temp[n]) >= .1) ch.held_temp = held; }
					if (c.min_on !== '0s' && hold && n === 'hdd') ch.hold_until = Math.floor(now - (now | 0) % 300 + 90);
					return ch; }) };
			if (M.in) Object.assign(body, { hwmon_path: '/sys/class/hwmon/hwmon14', extra_temps: { 'ec:cpu': +(pt.temp.cpu + 1.5).toFixed(1) }, alerts: { stall: Math.floor(now - 720) }, watched: pt.extra || {} });
			return ok(body); }
		if (p === '/api/history') { const min = Math.min(10080, Math.max(1, +u.searchParams.get('minutes') || 120)); return ok(history(min, +u.searchParams.get('since') || 0, M.in)); }
		if (!M.in && !PUB.test(p)) return fail('unauthorized', 401);
		if (p === '/api/history.csv') { const min = Math.min(10080, Math.max(1, +u.searchParams.get('minutes') || 120)); return ok(csv(min), `n5-fangov-history-n5host-${new Date(t0 * 1000).toISOString().slice(0, 19).replace(/[-:]/g, '').replace('T', '-')}.csv`); }
		if (p === '/api/config' && m === 'GET') return ok({ config: cfg, raw: raw() });
		if (p === '/api/config' && m === 'PUT') { const chs = parseChannels(opt.body || ''), errs = check(chs);
			if (u.searchParams.get('strict') === '1' && errs.length) return fail('config rejected', 400, { errors: errs });
			// &restart=1: 202 although the channel set is unchanged (the daemon answers so for a profile change too); a changed channel set is written but not applied
			const restart = Q.get('restart') === '1' || chs.length !== cfg.channel.length; if (chs.length === cfg.channel.length) cfg.channel = chs; cfg.schedule = parseSchedules(opt.body || '');
			return wait({ status: restart ? 202 : 200, body: { ok: true, restart_required: restart, warnings: errs } }); }
		if (p === '/api/sensors') return ok(Object.entries(SENS).map(([id, [d, , , kind]]) => Object.assign({ id, description: `${d} (now ${sv(id, now)} °C)`, temp: sv(id, now) }, kind ? { kind } : {})).concat(PAT));
		if (p === '/api/dashboard') { if (m === 'PUT') { const ids = (opt.json || {}).sensors || []; if (ids.length > 8) return fail('at most 8 sensors', 400);
				dash = ids; return ok({ ok: true, sensors: dash, warnings: ids.filter(i => !SENS[i]).map(i => i + ': unresolved') }); }
			return ok({ sensors: dash }); }
		if (p.startsWith('/api/override/')) { const n = p.split('/')[3];
			const lag = () => { if (Q.get('lag') === '1') ovLag[n] = { ov: overrides[n], left: 2 }; }; // &lag=1: the state answers keep the old mode for two polls
			if (m === 'DELETE') { lag(); delete overrides[n]; return ok({ ok: true, channel: n, mode: 'auto' }); }
			if ((n === 'hdd' || cfg.channel.some(c => c.name === n && c.stop !== 'auto')) && opt.json.duty < 60) return fail(`channel ${n} (fixed stop duty / not chip-regulated): manual duty must be at least 60`, 400);
			lag(); overrides[n] = opt.json.duty; return ok({ ok: true, channel: n, duty: opt.json.duty, mode: 'manual' }); }
		if (p === '/api/presets') return ok(Object.entries(presets).map(([name, v]) => ({ name, channels: v.ch.map(c => c.name), builtin: !!v.builtin, description: v.description })));
		if (p.startsWith('/api/presets/')) { const n = p.split('/')[3], b = presets[n] && presets[n].builtin, sub = p.split('/')[4];
			if (m === 'PUT') { if (b) return fail('built-in preset', 409); const chs = opt.json && opt.json.channels; // a JSON body = the composed channels (preset editor), validated like the config; empty = the running tables
				if (chs) { const errs = check(chs.map(c => Object.assign({ stop: 'auto', hysteresis: 0, min_on: '0s' }, c))); if (errs.length) return fail('preset rejected', 400, { errors: errs });
					if (chs.map(c => c.name).sort().join() !== cfg.channel.map(c => c.name).sort().join()) return fail('preset rejected', 400, { errors: ['channel: names do not match the running config'] }); }
				presets[n] = { ch: (chs || cfg.channel).map(c => Object.assign({}, c)) }; return ok({ ok: true, saved: n }); }
			if (m === 'DELETE') { if (b) return fail('built-in preset', 409); if (!presets[n]) return fail('no such preset', 404); delete presets[n]; return ok({ ok: true }); }
			if (m === 'GET') { if (!presets[n]) return fail('unknown preset ' + n, 404); return ok({ name: n, builtin: !!b, description: presets[n].description, channels: presets[n].ch }); }
			if (sub === 'rename') { const nn = opt.json.name; if (b || (presets[nn] && presets[nn].builtin)) return fail('built-in preset', 409); if (!presets[n]) return fail('unknown preset ' + n, 404); if (presets[nn]) return fail('preset exists', 409); presets[nn] = presets[n]; delete presets[n]; return ok({ ok: true, name: nn }); }
			if (presets[n]) cfg.channel = presets[n].ch.map(c => Object.assign({}, c)); return wait({ status: n === 'summer' ? 202 : 200, body: { ok: true } }); }
		if (p === '/api/schedules') return ok(schedStatus());
		if (p.startsWith('/api/tokens')) return mockTokens(p, m, opt);
		if (p === '/api/log' && m === 'DELETE') { logs.length = 0; return ok({ cleared: true, note: 'journal untouched' }); }
		if (p === '/api/log') return ok({ lines: logs.slice(-(+u.searchParams.get('lines') || 100)), source: 'file' });
		if (p === '/api/log/export') return ok(logs.join('\n') + '\n', 'n5-fangov-mock-20260915-120000.log');
		if (p === '/api/config/export') return ok({ format: 1, version: MV, exported: Math.floor(t0), config: raw(), presets: {} }, 'n5-fangov-settings-mock.json');
		if (p === '/api/config/import') { let j; try { j = JSON.parse(opt.body); } catch (e) { j = null; }
			if (!j || j.format !== 1) return fail('import rejected: bundle format missing\nexpected "format": 1', 400);
			return wait({ status: /restart/.test(opt.body) ? 202 : 200, body: { ok: true } }); }
		if (p === '/api/profiles') return ok([{ name: 'n5pro', title: 'Minisforum N5 Pro (IT5571 EC)', verified: true, notes: 'EC does not resume HDD regulation after a write; stop = fixed duty.' }, { name: 'nct67xx', title: 'Nuvoton NCT67xx (SmartFan IV)', verified: false, notes: 'Auto = pwmN_enable 5.' }]);
		if (p === '/api/alerts') { if (m === 'PUT') { const j = opt.json || {}; if (!/^(auto|pve|mail|webhook|log|off)$/.test(j.transport)) return fail('transport: unknown value', 400);
				if (j.mail_to !== undefined && /[\s"']/.test(j.mail_to || '')) return fail('mail_to: no spaces or quotes', 400);
				if (j.webhook_url !== undefined && j.webhook_url !== '' && !/^https?:\/\/[^\s/@]+/.test(j.webhook_url)) return fail('webhook_url: absolute http(s) URL with a host, no userinfo', 400);
				if (j.webhook_format !== undefined && !/^(json|text)$/.test(j.webhook_format)) return fail('webhook_format: json or text', 400);
				if (j.transport === 'webhook' && !(j.webhook_url === undefined ? A.webhook_url : j.webhook_url)) return fail('webhook_url required for transport webhook', 400);
				A.transport = j.transport; if (j.mail_to !== undefined) A.mail_to = j.mail_to || 'root'; if (j.webhook_url !== undefined) A.webhook_url = j.webhook_url; if (j.webhook_format !== undefined) A.webhook_format = j.webhook_format;
				return ok({ ok: true, status: alertStatus() }); }
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
		if (p === '/api/system') return ok(sysMock(now));
		return fail('mock: not found ' + p, 404);
	};
})();
