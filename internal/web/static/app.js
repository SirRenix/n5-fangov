/* pvefand web UI - vanilla JS, no build step, no external resources. */
(function () {
  'use strict';

  var $ = function (id) { return document.getElementById(id); };
  var REFRESH_MS = 5000;
  var HISTORY_MIN = 120;
  var state = null;      // last /api/state
  var history = [];      // last /api/history
  var sensors = [];      // /api/sensors
  var curveForm = [];    // parsed [[channel]] blocks for the Curves tab
  var tomlBlocks = null; // split raw TOML
  var auth = null;       // base64 "user:pass" once signed in
  var pendingLogin = null;

  try { auth = sessionStorage.getItem('pvefand.auth'); } catch (e) { auth = null; }

  /* ---------- helpers ---------- */
  function el(tag, cls, text) {
    var e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text !== undefined && text !== null) e.textContent = text;
    return e;
  }
  function clear(node) { while (node.firstChild) node.removeChild(node.firstChild); }
  function pct(duty) { return Math.round(duty * 100 / 255); }
  function fmtTemp(t) { return (t === null || t === undefined || t <= -900) ? 'n/a' : t.toFixed(1) + ' °C'; }
  function tempClass(t) {
    if (t === null || t === undefined || t <= -900) return 't-na';
    if (t < 45) return 't-cool';
    if (t < 65) return 't-ok';
    if (t < 80) return 't-warm';
    return 't-hot';
  }
  function modeClass(m) {
    if (m === 'auto') return 'ok';
    if (m === 'manual') return 'info';
    if (m === 'critical' || m === 'failsafe' || m === 'stall') return 'crit';
    return 'warn';
  }
  function fmtTime(ts) {
    if (!ts) return '-';
    var d = new Date(ts * 1000);
    return d.toLocaleString();
  }
  function fmtUptime(s) {
    if (s === undefined || s === null) return '-';
    var d = Math.floor(s / 86400), h = Math.floor(s % 86400 / 3600), m = Math.floor(s % 3600 / 60);
    return (d ? d + 'd ' : '') + h + 'h ' + m + 'm';
  }
  var toastTimer = null;
  function toast(msg, kind) {
    var t = $('toast');
    t.textContent = msg;
    t.className = 'toast' + (kind ? ' ' + kind : '');
    t.hidden = false;
    clearTimeout(toastTimer);
    toastTimer = setTimeout(function () { t.hidden = true; }, kind === 'err' ? 8000 : 4000);
  }

  /* ---------- API ---------- */
  function api(method, path, body, raw) {
    var headers = { 'X-Pvefand-Csrf': '1' };
    if (auth) headers['Authorization'] = 'Basic ' + auth;
    if (body !== undefined) headers['Content-Type'] = raw ? 'text/plain; charset=utf-8' : 'application/json';
    return fetch(path, {
      method: method, headers: headers, cache: 'no-store', credentials: 'same-origin',
      body: body === undefined ? undefined : (raw ? body : JSON.stringify(body))
    }).then(function (res) {
      if (res.status === 401) {
        return login().then(function () { return api(method, path, body, raw); });
      }
      return res.text().then(function (txt) {
        var data = null;
        try { data = txt ? JSON.parse(txt) : null; } catch (e) { data = { error: txt }; }
        if (!res.ok) {
          var err = new Error((data && data.error) || ('HTTP ' + res.status));
          err.status = res.status;
          throw err;
        }
        return { status: res.status, data: data };
      });
    });
  }
  function get(path) { return api('GET', path).then(function (r) { return r.data; }); }

  function login() {
    if (pendingLogin) return pendingLogin;
    pendingLogin = new Promise(function (resolve, reject) {
      var box = $('login'), form = $('login-form');
      box.hidden = false;
      $('login-pass').value = '';
      setTimeout(function () { ($('login-user').value ? $('login-pass') : $('login-user')).focus(); }, 0);
      function done(ok) {
        form.onsubmit = null; $('login-cancel').onclick = null;
        box.hidden = true; pendingLogin = null;
        ok ? resolve() : reject(new Error('sign-in cancelled'));
      }
      form.onsubmit = function (ev) {
        ev.preventDefault();
        auth = btoa(unescape(encodeURIComponent($('login-user').value + ':' + $('login-pass').value)));
        try { sessionStorage.setItem('pvefand.auth', auth); } catch (e) { /* ignore */ }
        done(true);
      };
      $('login-cancel').onclick = function () { auth = null; done(false); };
    });
    return pendingLogin;
  }

  /* ---------- tabs ---------- */
  var tabs = document.querySelectorAll('#tabs .tab');
  function showTab(name) {
    for (var i = 0; i < tabs.length; i++) tabs[i].classList.toggle('active', tabs[i].dataset.tab === name);
    var panes = document.querySelectorAll('main .pane');
    for (var j = 0; j < panes.length; j++) panes[j].classList.toggle('active', panes[j].id === 'tab-' + name);
    try { localStorage.setItem('pvefand.tab', name); } catch (e) { /* ignore */ }
    if (name === 'curves' && !tomlBlocks) loadCurves();
    if (name === 'presets') loadPresets();
    if (name === 'log') loadLog();
    if (name === 'compat') loadProfiles();
    if (name === 'manual') renderManual();
    if (name === 'overview') renderCharts();
  }
  for (var ti = 0; ti < tabs.length; ti++) {
    tabs[ti].addEventListener('click', function () { showTab(this.dataset.tab); });
  }

  /* ---------- overview ---------- */
  function renderHeader() {
    var s = state;
    var st = $('hdr-status');
    if (!s) { st.textContent = 'offline'; st.className = 'badge crit'; return; }
    st.textContent = s.dry_run ? 'dry-run' : s.status;
    st.className = 'badge ' + (s.status === 'ok' ? (s.dry_run ? 'warn' : 'ok') : 'crit');
    $('hdr-profile').textContent = s.profile ? s.profile + (s.verified ? ' · verified' : ' · untested') : 'no profile';
    var live = $('hdr-live');
    clear(live);
    s.channels.forEach(function (c) {
      var sp = el('span');
      sp.appendChild(el('b', null, c.name));
      sp.appendChild(document.createTextNode(' ' + fmtTemp(c.temp) + ' · ' + pct(c.duty) + '%' + (c.rpm >= 0 ? ' · ' + c.rpm + ' rpm' : '')));
      live.appendChild(sp);
    });
  }

  function renderCards() {
    var box = $('cards');
    clear(box);
    if (!state) { box.appendChild(el('p', 'muted', 'Daemon not reachable.')); return; }
    state.channels.forEach(function (c) {
      var card = el('div', 'card');
      var head = el('div', 'card-head');
      head.appendChild(el('span', 'card-name', c.name));
      head.appendChild(el('span', 'badge ' + modeClass(c.mode), c.mode));
      card.appendChild(head);
      var vals = el('div', 'card-vals');
      var v1 = el('div'); v1.appendChild(el('div', 'lbl', 'Temp'));
      v1.appendChild(el('div', 'val ' + tempClass(c.temp), fmtTemp(c.temp))); vals.appendChild(v1);
      var v2 = el('div'); v2.appendChild(el('div', 'lbl', 'Duty'));
      var dv = el('div', 'val', String(c.duty)); dv.appendChild(el('small', null, pct(c.duty) + '%')); v2.appendChild(dv); vals.appendChild(v2);
      var v3 = el('div'); v3.appendChild(el('div', 'lbl', 'RPM'));
      v3.appendChild(el('div', 'val', c.rpm >= 0 ? String(c.rpm) : 'n/a')); vals.appendChild(v3);
      card.appendChild(vals);
      card.appendChild(el('div', 'card-sub', 'pwm' + c.pwm + ' · ' + (c.sensor || '-') + ' · target ' + c.target));
      box.appendChild(card);
    });
    var extras = Object.keys(state.extra_temps || {});
    if (extras.length) {
      var card = el('div', 'card');
      var head = el('div', 'card-head'); head.appendChild(el('span', 'card-name', 'other sensors')); card.appendChild(head);
      var kv = el('dl', 'kv');
      extras.sort().forEach(function (k) {
        kv.appendChild(el('dt', null, k));
        kv.appendChild(el('dd', tempClass(state.extra_temps[k]), fmtTemp(state.extra_temps[k])));
      });
      card.appendChild(kv);
      box.appendChild(card);
    }
  }

  function renderHardware(version) {
    var dl = $('hw');
    clear(dl);
    var s = state || {};
    var rows = [
      ['Profile', s.profile || '-'],
      ['Verification', s.verified ? 'verified on hardware' : 'from documentation - untested'],
      ['hwmon path', s.hwmon_path || '-'],
      ['Status', s.status || '-'],
      ['Uptime', fmtUptime(s.uptime_s)],
      ['Version', version || '-']
    ];
    rows.forEach(function (r) {
      dl.appendChild(el('dt', null, r[0]));
      var dd = el('dd');
      if (r[0] === 'Verification') dd.appendChild(el('span', 'badge ' + (s.verified ? 'ok' : 'warn'), r[1]));
      else dd.textContent = r[1];
      dl.appendChild(dd);
    });
    var tb = $('alerts');
    clear(tb);
    var al = Object.keys(s.alerts || {});
    $('alerts-none').hidden = al.length > 0;
    al.sort(function (a, b) { return s.alerts[b] - s.alerts[a]; }).forEach(function (k) {
      var tr = el('tr');
      var td = el('td'); td.appendChild(el('span', 'badge crit', k)); tr.appendChild(td);
      tr.appendChild(el('td', 'mono', fmtTime(s.alerts[k])));
      tb.appendChild(tr);
    });
  }

  /* ---------- canvas charts ---------- */
  function setupCanvas(cv) {
    var dpr = window.devicePixelRatio || 1;
    var w = cv.clientWidth || 400, h = cv.clientHeight || 180;
    if (cv.width !== Math.round(w * dpr) || cv.height !== Math.round(h * dpr)) {
      cv.width = Math.round(w * dpr); cv.height = Math.round(h * dpr);
    }
    var ctx = cv.getContext('2d');
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.clearRect(0, 0, w, h);
    return { ctx: ctx, w: w, h: h };
  }
  var chartEls = {};
  function renderCharts() {
    if (!state || !$('tab-overview').classList.contains('active')) return;
    var box = $('charts');
    var names = state.channels.map(function (c) { return c.name; });
    var stale = Object.keys(chartEls).filter(function (n) { return names.indexOf(n) < 0; });
    stale.forEach(function (n) { box.removeChild(chartEls[n].wrap); delete chartEls[n]; });
    names.forEach(function (n) {
      if (!chartEls[n]) {
        var wrap = el('div', 'chart');
        var h4 = el('h4', null, n + ' ');
        var lg = el('span', 'legend'); lg.appendChild(el('span', 'temp', '— temp °C')); lg.appendChild(document.createTextNode('  '));
        lg.appendChild(el('span', 'rpm', '— rpm')); lg.appendChild(document.createTextNode('  '));
        lg.appendChild(el('span', 'duty', '· · duty %'));
        h4.appendChild(lg); wrap.appendChild(h4);
        var cv = el('canvas'); wrap.appendChild(cv);
        box.appendChild(wrap);
        chartEls[n] = { wrap: wrap, cv: cv };
      }
      drawHistory(chartEls[n].cv, n);
    });
  }
  function drawHistory(cv, name) {
    var c = setupCanvas(cv), ctx = c.ctx, w = c.w, h = c.h;
    var padL = 34, padR = 44, padT = 8, padB = 18;
    var pw = w - padL - padR, ph = h - padT - padB;
    var now = Math.floor(Date.now() / 1000), t0 = now - HISTORY_MIN * 60;
    var pts = history.filter(function (p) { return p.ts >= t0; });
    var temps = [], rpms = [];
    pts.forEach(function (p) {
      var t = p.temp && p.temp[name], r = p.rpm && p.rpm[name];
      if (t !== undefined && t > -900) temps.push(t);
      if (r !== undefined && r >= 0) rpms.push(r);
    });
    var tMin = temps.length ? Math.min.apply(null, temps) : 20, tMax = temps.length ? Math.max.apply(null, temps) : 80;
    tMin = Math.floor((tMin - 5) / 10) * 10; tMax = Math.ceil((tMax + 5) / 10) * 10; if (tMax - tMin < 20) tMax = tMin + 20;
    var rMax = rpms.length ? Math.max.apply(null, rpms) : 0; rMax = Math.max(500, Math.ceil(rMax / 500) * 500);
    var css = getComputedStyle(document.documentElement);
    var colLine = css.getPropertyValue('--line').trim(), colFg2 = css.getPropertyValue('--fg2').trim();
    var colT = css.getPropertyValue('--temp').trim(), colR = css.getPropertyValue('--rpm').trim(), colD = css.getPropertyValue('--duty').trim();
    var x = function (ts) { return padL + (ts - t0) / (HISTORY_MIN * 60) * pw; };
    var yT = function (t) { return padT + (1 - (t - tMin) / (tMax - tMin)) * ph; };
    var yR = function (r) { return padT + (1 - r / rMax) * ph; };
    var yD = function (d) { return padT + (1 - d / 255) * ph; };
    ctx.font = '10px system-ui, sans-serif'; ctx.strokeStyle = colLine; ctx.fillStyle = colFg2; ctx.lineWidth = 1;
    // grid + left axis (temp), right axis (rpm)
    for (var i = 0; i <= 4; i++) {
      var yy = padT + ph * i / 4;
      ctx.beginPath(); ctx.moveTo(padL, yy + .5); ctx.lineTo(w - padR, yy + .5); ctx.stroke();
      ctx.textAlign = 'right'; ctx.fillText(String(Math.round(tMax - (tMax - tMin) * i / 4)), padL - 4, yy + 3);
      ctx.textAlign = 'left'; ctx.fillText(String(Math.round(rMax - rMax * i / 4)), w - padR + 4, yy + 3);
    }
    // x ticks every 30 min
    ctx.textAlign = 'center';
    for (var m = 0; m <= HISTORY_MIN; m += 30) {
      var ts = t0 + m * 60, xx = x(ts);
      ctx.beginPath(); ctx.moveTo(xx + .5, padT); ctx.lineTo(xx + .5, padT + ph); ctx.stroke();
      var d = new Date(ts * 1000);
      ctx.fillText(('0' + d.getHours()).slice(-2) + ':' + ('0' + d.getMinutes()).slice(-2), xx, h - 5);
    }
    if (!pts.length) { ctx.textAlign = 'center'; ctx.fillText('no history yet', padL + pw / 2, padT + ph / 2); return; }
    function line(color, getY, dashed) {
      ctx.strokeStyle = color; ctx.lineWidth = dashed ? 1 : 1.6; ctx.setLineDash(dashed ? [3, 3] : []);
      ctx.beginPath(); var open = false;
      pts.forEach(function (p) {
        var y = getY(p);
        if (y === null) { open = false; return; }
        if (!open) { ctx.moveTo(x(p.ts), y); open = true; } else ctx.lineTo(x(p.ts), y);
      });
      ctx.stroke(); ctx.setLineDash([]);
    }
    line(colD, function (p) { var v = p.duty && p.duty[name]; return v === undefined ? null : yD(v); }, true);
    line(colR, function (p) { var v = p.rpm && p.rpm[name]; return (v === undefined || v < 0) ? null : yR(v); }, false);
    line(colT, function (p) { var v = p.temp && p.temp[name]; return (v === undefined || v <= -900) ? null : yT(v); }, false);
  }

  function drawCurve(cv, ch) {
    var c = setupCanvas(cv), ctx = c.ctx, w = c.w, h = c.h;
    var padL = 30, padR = 10, padT = 8, padB = 18, pw = w - padL - padR, ph = h - padT - padB;
    var css = getComputedStyle(document.documentElement);
    var colLine = css.getPropertyValue('--line').trim(), colFg2 = css.getPropertyValue('--fg2').trim();
    var colAcc = css.getPropertyValue('--acc').trim(), colCrit = css.getPropertyValue('--crit').trim(), colT = css.getPropertyValue('--temp').trim();
    var x = function (t) { return padL + t / 100 * pw; }, y = function (d) { return padT + (1 - d / 255) * ph; };
    ctx.font = '10px system-ui, sans-serif'; ctx.strokeStyle = colLine; ctx.fillStyle = colFg2; ctx.lineWidth = 1;
    for (var i = 0; i <= 4; i++) {
      var yy = padT + ph * i / 4; ctx.beginPath(); ctx.moveTo(padL, yy + .5); ctx.lineTo(w - padR, yy + .5); ctx.stroke();
      ctx.textAlign = 'right'; ctx.fillText(String(Math.round(100 - 25 * i)) + '%', padL - 3, yy + 3);
    }
    ctx.textAlign = 'center';
    for (var t = 0; t <= 100; t += 20) { ctx.beginPath(); ctx.moveTo(x(t) + .5, padT); ctx.lineTo(x(t) + .5, padT + ph); ctx.stroke(); ctx.fillText(t + '°', x(t), h - 5); }
    var pts = ch.curve.filter(function (p) { return isFinite(p[0]) && isFinite(p[1]); }).slice().sort(function (a, b) { return a[0] - b[0]; });
    if (pts.length) {
      ctx.strokeStyle = colAcc; ctx.lineWidth = 2; ctx.beginPath();
      ctx.moveTo(x(0), y(pts[0][1]));
      pts.forEach(function (p) { ctx.lineTo(x(Math.min(100, Math.max(0, p[0]))), y(p[1])); });
      ctx.lineTo(x(100), y(pts[pts.length - 1][1])); ctx.stroke();
      ctx.fillStyle = colAcc;
      pts.forEach(function (p) { ctx.beginPath(); ctx.arc(x(p[0]), y(p[1]), 3.5, 0, Math.PI * 2); ctx.fill(); });
    }
    if (isFinite(ch.critical) && ch.critical > 0) {
      ctx.strokeStyle = colCrit; ctx.setLineDash([4, 3]); ctx.beginPath(); ctx.moveTo(x(ch.critical) + .5, padT); ctx.lineTo(x(ch.critical) + .5, padT + ph); ctx.stroke(); ctx.setLineDash([]);
    }
    var live = state && state.channels.filter(function (c) { return c.name === ch.name; })[0];
    if (live && live.temp > -900) {
      ctx.fillStyle = colT; ctx.beginPath(); ctx.arc(x(live.temp), y(live.duty), 4.5, 0, Math.PI * 2); ctx.fill();
    }
  }

  /* ---------- TOML [[channel]] block handling ---------- */
  function stripComment(s) {
    var q = false, out = '';
    for (var i = 0; i < s.length; i++) {
      var ch = s[i];
      if (ch === '"' && s[i - 1] !== '\\') q = !q;
      if (ch === '#' && !q) break;
      out += ch;
    }
    return out;
  }
  function unquote(v) {
    v = v.trim();
    if ((v[0] === '"' && v[v.length - 1] === '"') || (v[0] === "'" && v[v.length - 1] === "'")) return v.slice(1, -1);
    return v;
  }
  function splitToml(text) {
    var lines = text.split(/\r?\n/), blocks = [], cur = { header: null, lines: [] };
    lines.forEach(function (ln) {
      var m = ln.match(/^\s*(\[\[?\s*[A-Za-z0-9_."'-]+\s*\]\]?)\s*(#.*)?$/);
      if (m) { blocks.push(cur); cur = { header: m[1].replace(/\s+/g, ''), lines: [] }; }
      else cur.lines.push(ln);
    });
    blocks.push(cur);
    return blocks;
  }
  function parseChannel(lines) {
    var ch = { name: '', pwm: 1, sensor: '', curve: [], critical: '', stop: 'auto', extra: [] };
    var i = 0;
    while (i < lines.length) {
      var raw = lines[i], s = stripComment(raw).trim(); i++;
      if (!s) continue;
      var m = s.match(/^([A-Za-z0-9_-]+)\s*=\s*(.*)$/);
      if (!m) { ch.extra.push(raw); continue; }
      var k = m[1], v = m[2].trim();
      if (k === 'curve') {
        // arrays may span lines: accumulate until brackets balance
        var depth = 0, acc = v, guard = 0;
        var count = function (str) { for (var j = 0; j < str.length; j++) { if (str[j] === '[') depth++; else if (str[j] === ']') depth--; } };
        count(acc);
        while (depth > 0 && i < lines.length && guard++ < 50) { var nx = stripComment(lines[i]).trim(); i++; acc += nx; count(nx); }
        try { ch.curve = JSON.parse(acc.replace(/,\s*\]/g, ']')); } catch (e) { ch.extra.push(raw); }
      } else if (k === 'name' || k === 'sensor') ch[k] = unquote(v);
      else if (k === 'pwm' || k === 'critical') ch[k] = parseInt(v, 10);
      else if (k === 'stop') ch.stop = unquote(v);
      else ch.extra.push(raw);
    }
    while (ch.extra.length && !ch.extra[ch.extra.length - 1].trim()) ch.extra.pop();
    return ch;
  }
  function renderChannelToml(ch) {
    var out = ['[[channel]]', 'name = "' + ch.name + '"', 'pwm = ' + ch.pwm, 'sensor = "' + ch.sensor + '"',
      'curve = [' + ch.curve.map(function (p) { return '[' + p[0] + ', ' + p[1] + ']'; }).join(', ') + ']',
      'critical = ' + ch.critical, 'stop = ' + (/^\d+$/.test(String(ch.stop)) ? ch.stop : '"' + ch.stop + '"')];
    return out.concat(ch.extra).join('\n');
  }
  function assembleToml() {
    var out = [], ci = 0;
    tomlBlocks.forEach(function (b) {
      if (b.header === '[[channel]]') { out.push(renderChannelToml(curveForm[ci++]) + '\n'); return; }
      var txt = (b.header !== null ? b.header + '\n' : '') + b.lines.join('\n');
      if (txt.trim() !== '' || b.header !== null) out.push(txt.replace(/\s+$/, '') + '\n');
    });
    return out.join('\n');
  }

  /* ---------- curves tab ---------- */
  function loadCurves() {
    return get('/api/config').then(function (cfg) {
      $('raw').value = cfg.raw || '';
      tomlBlocks = splitToml(cfg.raw || '');
      curveForm = tomlBlocks.filter(function (b) { return b.header === '[[channel]]'; }).map(function (b) { return parseChannel(b.lines); });
      renderCurves();
    }).catch(function (e) { toast('load config: ' + e.message, 'err'); });
  }
  function renderCurves() {
    var box = $('curves');
    clear(box);
    if (!curveForm.length) { box.appendChild(el('p', 'muted', 'No [[channel]] tables in config. Use the raw editor to add channels (restart required).')); return; }
    curveForm.forEach(function (ch, idx) {
      var card = el('div', 'curve');
      card.appendChild(el('h4', null, (ch.name || 'channel ' + (idx + 1)) + ' (pwm' + ch.pwm + ')'));
      var cv = el('canvas'); card.appendChild(cv);
      var f = el('div', 'fields');
      f.appendChild(el('label', null, 'Sensor'));
      var sel = el('select'); var known = false;
      sensors.forEach(function (s) { var o = el('option', null, s.id + (s.description ? ' - ' + s.description : '')); o.value = s.id; if (s.id === ch.sensor) { o.selected = true; known = true; } sel.appendChild(o); });
      if (!known) { var o = el('option', null, ch.sensor || '(none)'); o.value = ch.sensor; o.selected = true; sel.appendChild(o); }
      sel.onchange = function () { ch.sensor = sel.value; };
      f.appendChild(sel);
      f.appendChild(el('label', null, 'Critical °C'));
      var crit = el('input'); crit.type = 'number'; crit.min = 30; crit.max = 120; crit.value = ch.critical;
      crit.oninput = function () { ch.critical = parseInt(crit.value, 10); drawCurve(cv, ch); };
      f.appendChild(crit);
      f.appendChild(el('label', null, 'Stop'));
      var stop = el('input'); stop.type = 'text'; stop.value = ch.stop; stop.placeholder = 'auto or 0..255';
      stop.oninput = function () { ch.stop = stop.value.trim(); };
      f.appendChild(stop);
      card.appendChild(f);
      var tbl = el('table', 'pts');
      var thead = el('tr'); ['Temp °C', 'Duty (0-255)', '%', ''].forEach(function (t) { thead.appendChild(el('th', null, t)); }); tbl.appendChild(thead);
      function rows() {
        while (tbl.rows.length > 1) tbl.deleteRow(1);
        ch.curve.forEach(function (p, pi) {
          var tr = el('tr');
          var tdT = el('td'), inT = el('input'); inT.type = 'number'; inT.min = -20; inT.max = 120; inT.value = p[0];
          inT.oninput = function () { p[0] = parseFloat(inT.value); drawCurve(cv, ch); }; tdT.appendChild(inT); tr.appendChild(tdT);
          var tdD = el('td'), inD = el('input'), pc = el('td', 'mono', pct(p[1]) + '%'); inD.type = 'number'; inD.min = 0; inD.max = 255; inD.value = p[1];
          inD.oninput = function () { p[1] = parseInt(inD.value, 10); pc.textContent = isFinite(p[1]) ? pct(p[1]) + '%' : '-'; drawCurve(cv, ch); }; tdD.appendChild(inD); tr.appendChild(tdD);
          tr.appendChild(pc);
          var tdX = el('td', 'del'), bx = el('button', 'btn small', '×'); bx.type = 'button'; bx.title = 'remove point';
          bx.onclick = function () { ch.curve.splice(pi, 1); rows(); drawCurve(cv, ch); }; tdX.appendChild(bx); tr.appendChild(tdX);
          tbl.appendChild(tr);
        });
      }
      rows();
      card.appendChild(tbl);
      var add = el('button', 'btn small', '+ point'); add.type = 'button';
      add.onclick = function () {
        if (ch.curve.length >= 8) { toast('max 8 points', 'err'); return; }
        var last = ch.curve[ch.curve.length - 1] || [40, 80];
        ch.curve.push([Math.min(100, last[0] + 10), Math.min(255, last[1] + 40)]); rows(); drawCurve(cv, ch);
      };
      var act = el('div', 'actions'); act.appendChild(add); card.appendChild(act);
      box.appendChild(card);
      requestAnimationFrame(function () { drawCurve(cv, ch); });
    });
  }
  function validateCurves() {
    for (var i = 0; i < curveForm.length; i++) {
      var ch = curveForm[i], n = ch.name || ('channel ' + (i + 1));
      if (ch.curve.length < 2 || ch.curve.length > 8) return n + ': 2..8 curve points required';
      for (var j = 0; j < ch.curve.length; j++) {
        var p = ch.curve[j];
        if (!isFinite(p[0]) || !isFinite(p[1]) || p[1] < 0 || p[1] > 255) return n + ': invalid point ' + (j + 1);
        if (j > 0 && p[0] <= ch.curve[j - 1][0]) return n + ': temperatures must ascend';
      }
      if (!isFinite(ch.critical) || ch.critical < 30 || ch.critical > 120) return n + ': critical must be 30..120';
      if (!(ch.stop === 'auto' || (/^\d+$/.test(ch.stop) && +ch.stop <= 255))) return n + ': stop must be "auto" or 0..255';
    }
    return null;
  }
  function putConfig(text) {
    return api('PUT', '/api/config', text, true).then(function (r) {
      var note = $('curves-note');
      if (r.status === 202 || (r.data && r.data.restart_required)) {
        note.hidden = false; note.className = 'note'; note.textContent = 'Config saved. Restart required: ' + ((r.data && r.data.message) || 'channel set or profile changed') + ' - run: systemctl restart pvefand';
        toast('saved, restart required', 'ok');
      } else {
        note.hidden = false; note.className = 'note ok'; note.textContent = 'Config saved and reloaded.';
        toast('config applied', 'ok');
      }
      tomlBlocks = null;
      return loadCurves();
    }).catch(function (e) { toast(e.message, 'err'); });
  }
  $('curves-apply').onclick = function () {
    var err = validateCurves();
    if (err) { toast(err, 'err'); return; }
    putConfig(assembleToml());
  };
  $('curves-reload').onclick = function () { tomlBlocks = null; loadCurves(); };
  $('raw-save').onclick = function () { putConfig($('raw').value); };
  $('raw-from-form').onclick = function () {
    var err = validateCurves(); if (err) { toast(err, 'err'); return; }
    $('raw').value = assembleToml(); toast('raw text regenerated - not saved yet');
  };

  /* ---------- manual tab ---------- */
  var manualEls = {};
  function renderManual() {
    var box = $('manual');
    if (!state) { clear(box); manualEls = {}; box.appendChild(el('p', 'muted', 'Daemon not reachable.')); return; }
    if (box.firstChild && box.firstChild.tagName === 'P') { clear(box); manualEls = {}; }
    var names = state.channels.map(function (c) { return c.name; });
    Object.keys(manualEls).forEach(function (n) { if (names.indexOf(n) < 0) { box.removeChild(manualEls[n].wrap); delete manualEls[n]; } });
    state.channels.forEach(function (c) {
      var m = manualEls[c.name];
      if (!m) {
        var wrap = el('div', 'mch');
        var head = el('div', 'head'); head.appendChild(el('b', null, c.name)); var badge = el('span', 'badge'); head.appendChild(badge); wrap.appendChild(head);
        var range = el('input'); range.type = 'range'; range.min = 0; range.max = 255; range.value = c.duty; wrap.appendChild(range);
        var vals = el('div', 'vals'); var cur = el('span'); var sel = el('span'); vals.appendChild(cur); vals.appendChild(sel); wrap.appendChild(vals);
        var act = el('div', 'actions');
        var apply = el('button', 'btn primary', 'Apply'); var autoB = el('button', 'btn', 'Auto');
        act.appendChild(apply); act.appendChild(autoB); wrap.appendChild(act);
        m = manualEls[c.name] = { wrap: wrap, badge: badge, range: range, cur: cur, sel: sel, apply: apply, autoB: autoB, touched: false };
        range.oninput = function () { m.touched = true; m.sel.textContent = 'selected ' + range.value + ' (' + pct(+range.value) + '%)'; };
        apply.onclick = function () {
          api('PUT', '/api/override/' + encodeURIComponent(c.name), { duty: parseInt(range.value, 10) })
            .then(function () { m.touched = false; toast(c.name + ' → manual ' + range.value, 'ok'); return refresh(); })
            .catch(function (e) { toast(e.message, 'err'); });
        };
        autoB.onclick = function () {
          api('DELETE', '/api/override/' + encodeURIComponent(c.name))
            .then(function () { m.touched = false; toast(c.name + ' → auto', 'ok'); return refresh(); })
            .catch(function (e) { toast(e.message, 'err'); });
        };
        box.appendChild(wrap);
      }
      m.badge.textContent = c.mode; m.badge.className = 'badge ' + modeClass(c.mode);
      var curTxt = el('span'); curTxt.appendChild(document.createTextNode('current ')); curTxt.appendChild(el('b', null, c.duty + ' (' + pct(c.duty) + '%)'));
      curTxt.appendChild(document.createTextNode(c.rpm >= 0 ? ' · ' + c.rpm + ' rpm' : '')); clear(m.cur); m.cur.appendChild(curTxt);
      if (!m.touched) { m.range.value = c.duty; m.sel.textContent = c.mode === 'manual' ? 'override active' : 'following curve'; }
      m.autoB.disabled = c.mode !== 'manual';
    });
  }

  /* ---------- presets ---------- */
  function loadPresets() {
    get('/api/presets').then(function (list) {
      var tb = $('presets'); clear(tb);
      $('presets-none').hidden = list.length > 0;
      list.forEach(function (p) {
        var tr = el('tr');
        tr.appendChild(el('td', 'mono', p.name));
        tr.appendChild(el('td', null, (p.channels || []).join(', ')));
        var td = el('td'); var b = el('button', 'btn small primary', 'Apply'); td.appendChild(b); tr.appendChild(td);
        b.onclick = function () {
          if (!confirm('Apply preset "' + p.name + '"? Current curves will be replaced.')) return;
          api('POST', '/api/presets/' + encodeURIComponent(p.name) + '/apply').then(function (r) {
            toast(r.status === 202 ? 'preset applied - restart required' : 'preset ' + p.name + ' applied', 'ok');
            tomlBlocks = null; return refresh();
          }).catch(function (e) { toast(e.message, 'err'); });
        };
        tb.appendChild(tr);
      });
    }).catch(function (e) { toast('presets: ' + e.message, 'err'); });
  }
  $('preset-save').onclick = function () {
    var name = $('preset-name').value.trim();
    if (!/^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$/.test(name)) { toast('name: letters, digits, _ . - (max 64)', 'err'); return; }
    api('PUT', '/api/presets/' + encodeURIComponent(name)).then(function () { toast('preset ' + name + ' saved', 'ok'); $('preset-name').value = ''; loadPresets(); })
      .catch(function (e) { toast(e.message, 'err'); });
  };

  /* ---------- log ---------- */
  function loadLog() {
    get('/api/log?lines=' + encodeURIComponent($('log-lines').value)).then(function (r) {
      var pre = $('log'); pre.textContent = (r.lines || []).join('\n') || '(empty)'; pre.scrollTop = pre.scrollHeight;
    }).catch(function (e) { $('log').textContent = 'log unavailable: ' + e.message; });
  }
  $('log-refresh').onclick = loadLog;
  $('log-lines').onchange = loadLog;

  /* ---------- compatibility ---------- */
  function loadProfiles() {
    get('/api/profiles').then(function (list) {
      var tb = $('profiles'); clear(tb);
      list.forEach(function (p) {
        var tr = el('tr');
        var td0 = el('td', 'mono', p.name); if (p.active) { td0.appendChild(document.createTextNode(' ')); td0.appendChild(el('span', 'badge info', 'active')); } tr.appendChild(td0);
        tr.appendChild(el('td', null, p.title));
        var td = el('td'); td.appendChild(el('span', 'badge ' + (p.verified ? 'ok' : 'warn'), p.verified ? 'verified on hardware' : 'from documentation - untested')); tr.appendChild(td);
        tr.appendChild(el('td', null, p.notes || ''));
        tb.appendChild(tr);
      });
    }).catch(function (e) { toast('profiles: ' + e.message, 'err'); });
  }

  /* ---------- refresh loop ---------- */
  var version = '';
  function refresh() {
    return Promise.all([get('/api/state'), get('/api/history?minutes=' + HISTORY_MIN)]).then(function (r) {
      state = r[0]; history = r[1] || [];
      document.title = 'pvefand · ' + (state.profile || '') + (state.status !== 'ok' ? ' · ' + state.status : '');
      renderHeader(); renderCards(); renderHardware(version); renderCharts();
      if ($('tab-manual').classList.contains('active')) renderManual();
    }).catch(function (e) {
      state = null; renderHeader(); renderCards();
      $('hdr-profile').textContent = e.message;
    });
  }
  get('/api/version').then(function (v) { version = v.version || ''; renderHardware(version); }).catch(function () { /* ignore */ });
  get('/api/sensors').then(function (s) { sensors = s || []; if (tomlBlocks) renderCurves(); }).catch(function () { /* ignore */ });
  refresh();
  setInterval(refresh, REFRESH_MS);
  window.addEventListener('resize', function () { renderCharts(); });
  var savedTab = null;
  try { savedTab = localStorage.getItem('pvefand.tab'); } catch (e) { /* ignore */ }
  if (savedTab && savedTab !== 'overview') showTab(savedTab);
})();
