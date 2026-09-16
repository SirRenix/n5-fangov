# Dashboard screenshots

All images come from the built-in mock (`internal/web/static/index.html?mock=1`), so they
show example values only (`n5host`, `192.0.2.x`, `n5.lan`, `fans.example`) — no data from
a real host. Desktop views are 1280×800, dark theme; the three mobile views are 375 px
wide. The version badge shows the mock's version string.

Regenerate the set with `shots.mjs` (Node ≥ 22, Chrome/Chromium; talks to the browser
over the DevTools protocol): serve `internal/web/static/` on a local port, start Chrome
headless with `--remote-debugging-port=9223 --lang=en-US --hide-scrollbars`, then
`node docs/screenshots/shots.mjs docs/screenshots http://127.0.0.1:8797/index.html 9223`.
The "Mock mode" toast is removed before each capture; every interactive state is reached
by clicking the same controls a user would.

| File | View | Mock state | Note |
|---|---|---|---|
| `01-overview-anonymous.png` | Overview, anonymous | `?mock=1` | what a visitor sees before signing in |
| `02-login-dialog.png` | Sign-in dialog | `?mock=1` → *Sign in* | *Remember me* |
| `03-overview-signed-in-top.png` | Overview, signed in | `?mock=1&user=1` | channel cards, charts |
| `04-overview-signed-in-bottom.png` | Overview, lower half | `?mock=1&user=1`, scrolled | Extra sensors chart, Sensors card, System card, Recent alerts |
| `05-header.png` | Header crop | `?mock=1&user=1` | status chip, uptime, version + BETA, lock, live, user, gear (1280×92) |
| `06-settings-popover.png` | Settings gear | `?mock=1&user=1` → ⚙ | unit, interval, theme, export/import, Certificate…, Account… |
| `07-curves.png` | Curves | `?mock=1&user=1&tab=curves` | editor, table, `+ add point`, crit line, now marker, duty→RPM reference |
| `08-curves-error.png` | Curves, validation error | critical cleared, stop `300` → *Apply* | red notice, nothing sent |
| `09-curves-restart-required.png` | Curves, restart required | notice text set to the client's 202 string | **synthesised**: in the audited build the notice is hidden again by the editor reload right after Apply (`docs/AUDIT.md` §4); regenerate through a real 202 once the frontend fix is merged |
| `10-manual.png` | Manual | `?mock=1&user=1&tab=manual`, hdd slider at 40 | minimum-60 hint for HDD-like channels |
| `11-presets.png` | Presets | `?mock=1&user=1&tab=presets` → *Details* | built-in / recommended badges, channel tables |
| `12-alerts.png` | Alerts | `?mock=1&user=1&tab=alerts` | transport form, template card, kinds table, recent |
| `13-alerts-test-toast.png` | Test-alert toast | *Send test alert* | crop of the lower right corner (640×240) |
| `14-certificate-auto.png` | Certificate panel, automatic | lock icon, "How to trust" opened | fingerprint, SAN chips, downloads |
| `15-certificate-upload.png` | Certificate upload after `force_required` | *Upload own certificate…* → submit | *install anyway* checkbox with the HSTS warning |
| `16a-certificate-fallback.png` | Certificate panel, fallback | `?mock=1&user=1&tls=fallback` → lock | warn badge *automatic (fallback)* |
| `16b-certificate-soon.png` | Certificate panel, expiring | `?mock=1&user=1&tls=soon` → lock | expiry warning |
| `16c-certificate-off.png` | Certificate panel, TLS off | `?mock=1&user=1&tls=off` → ⚙ → *Certificate…* | loopback explanation |
| `17-account.png` | Account dialog | ⚙ → *Account…* → *Change password…* | sessions table with THIS SESSION |
| `18a-system.png` | System, top | `?mock=1&user=1&tab=system` | Host / Machine / CPU / Fan controller, memory modules |
| `18b-system-scrolled.png` | System, scrolled | same, scrolled 800 px | GPU · NPU, Network, Storage |
| `19-system-error.png` | System with source error | `?mock=1&user=1&tab=system&syserr=1` | notice for a missing `lspci` |
| `20-log.png` | Log | `?mock=1&user=1&tab=log` | filter, export, clear |
| `21-compatibility.png` | Compatibility | `?mock=1&user=1&tab=compat` | ACTIVE, verified/untested badges |
| `22-about.png` | About | `?mock=1&user=1&tab=about` | licence, links, credits |
| `23-connection-lost.png` | Connection banner | banner unhidden by script (same DOM the client shows after two failed polls) | |
| `24-overview-light.png` | Overview, light theme | `localStorage` theme `light`, `?mock=1&user=1` | |
| `25a-mobile-overview-anonymous.png` | Mobile Overview, anonymous | 375×812 emulation, `?mock=1`, full page | 375×1375 |
| `25b-mobile-overview-signed-in.png` | Mobile Overview, signed in | `?mock=1&user=1` | |
| `26-mobile-curves.png` | Mobile Curves | `?mock=1&user=1&tab=curves` | touch drag on the points |
| `27-overview-24h.png` | Overview, 24 h range | `?mock=1&user=1` → range `24 h` | averaged charts, range selector, CSV button |
| `28-curves-fields.png` | Curves editor fields | `?mock=1&user=1&tab=curves` | sensor select with a composite entry, critical, stop, hysteresis, min on (crop) |
| `29-schedules.png` | Presets, Schedules card | `?mock=1&user=1&tab=presets`, scrolled | entries with ACTIVE, next and last switch, timezone (`&schedfail=1` for the failed-switch variant) |
| `30-alerts-webhook.png` | Alerts, webhook transport | `?mock=1&user=1&tab=alerts` → transport `webhook` | URL and format fields |
| `31-account-tokens.png` | Account dialog, API tokens | ⚙ → *Account…* → *Create token…* | token table, create form, the secret shown once |

Not reproducible from the mock and therefore not included: the browser's own
certificate warning and the OS trust dialogs ([HTTPS](../08-https-security.md#the-certificate),
three steps) and the PVE notification matcher ([Alerts](../07-alerts.md#the-pve-side))
— take those on a real box when needed. The pages that embed these images:
[Dashboard](../04-dashboard.md) (all views) and the [README](../../README.md)
(`03-overview-signed-in-top.png`).
