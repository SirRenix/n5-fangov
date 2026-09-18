# Dashboard screenshots

All images come from the built-in mock (`internal/web/static/index.html?mock=1`), so they
show example values only (`n5host`, `192.0.2.x`, `n5.lan`, `fans.example`) — no data from
a real host. Desktop views are 1280 px wide, dark theme, sidebar expanded; the mobile views
are 375 px wide. The version badge shows the mock's version string.

Regenerate the set with `shots.mjs` (Node ≥ 22, Chrome/Chromium; talks to the browser
over the DevTools protocol; the mock is `mock.js` next to `app.js`, requested only behind
`?mock=1`): serve `internal/web/static/` on a local port, start Chrome
headless with `--remote-debugging-port=9223 --lang=en-US --hide-scrollbars`, then
`node docs/screenshots/shots.mjs docs/screenshots http://127.0.0.1:8797/index.html 9223`.

Capture method (0.4.0): a **page** shot is a full-page capture — the emulated viewport grows
to the document height (capped at 3600 px) before the capture, so the 100 vh sidebar and the
sticky action bar span the whole image instead of repeating; dialogs, the header crops and
toasts are viewport clips at 1280×800. Pages are reached by hash (`#fans`,
`#settings/st-cert`, `#about/compat`); the mock's `&tab=` parameter still maps
`curves|manual|presets → fans` and `compat → about`. The "Mock mode" toast is removed
before each capture; every interactive state is reached by clicking the same controls a
user would.

| File | View | Mock state | Note |
|---|---|---|---|
| `01-overview-anonymous.png` | Overview, anonymous | `?mock=1` | tiles + charts only; full page |
| `02-login-dialog.png` | Sign-in dialog | `?mock=1` → *Sign in* | *Remember me* |
| `03-overview-signed-in.png` | Overview, signed in | `?mock=1&user=1` | full page: tiles with 2 h sparklines, three charts (Extra sensors as the third), Sensors grouped by kind, System glance, Recent alerts |
| `04-header.png` | Header crop | `?mock=1&user=1` | title, status chip, uptime, version + pre-release badge, live, user, *Sign out* (1280×60) |
| `05-sidebar-rail.png` | Sidebar collapsed | *Collapse* in the sidebar footer | 56 px icon rail, state in `localStorage` (`nav`) |
| `06-system.png` | System | `#system` | Host / Machine / CPU / Fan controller, memory modules, GPU · NPU, Network, Storage; full page |
| `07-fans.png` | Fans | `#fans` | one card per channel: curve editor left, *Live & override* right, presets row, sticky action bar; full page |
| `08-fans-validation-error.png` | Fans, validation error | critical cleared, stop `300` → *Apply* | red notice, nothing sent |
| `09-fans-restart-required.png` | Fans, restart required | `&restart=1`, critical +1 → *Apply* (the mock answers 202) | restart notice, stays after the editor reload until *Revert* |
| `10-fans-manual-switch.png` | Fans, Manual switch on | hdd channel: Auto / Manual switch → Manual, slider at 40 | `role="switch"`, minimum-60 hint for HDD-like channels |
| `11-preset-editor.png` | Preset editor dialog | *Save current as…* | name, description, per-channel tables, prefilled from the editor state |
| `12-schedules.png` | Schedules | `#schedules` | editable rows (preset, from / to, days, fallback), ACTIVE, *Add entry*, status block; full page |
| `13-schedules-last-switch-failed.png` | Schedules, failed last switch | `&schedfail=1` | warn notice, error in the status block |
| `14-alerts.png` | Alerts | `#alerts` | effective transport, kinds with last delivery, recent alerts, *Send test alert*; full page |
| `15-alerts-test-toast.png` | Test-alert toast | *Send test alert* | crop of the lower right corner (640×240) |
| `16-log.png` | Log | `#log` | filter, export, clear; full page |
| `17-settings.png` | Settings | `#settings` | Display · Account & sessions · API tokens · Certificate · Alert transport · Backup · Danger zone, sub-navigation left; full page |
| `18a-settings-certificate-auto.png` | Certificate section, automatic | `#settings/st-cert`, "How to trust" opened | fingerprint, SAN chips, downloads |
| `18b-certificate-fallback.png` | Certificate section, fallback | `&tls=fallback` | warn badge *automatic (fallback)* |
| `18c-certificate-soon.png` | Certificate section, expiring | `&tls=soon` | expiry warning |
| `18d-certificate-off.png` | Certificate section, TLS off | `&tls=off` | loopback explanation |
| `19-settings-tokens.png` | Settings → API tokens | `#settings/st-tokens` → *Create token…* → *Create* | tokens table, the secret shown once with *Copy* |
| `20-about-compatibility.png` | About with the Compatibility card | `#about/compat` | licence, links, credits; profiles table with ACTIVE, verified / untested badges; full page |
| `21-connection-lost.png` | Connection banner | banner unhidden by script (same DOM the client shows after two failed polls) | |
| `22-overview-light.png` | Overview, light theme | `localStorage` theme `light`, `?mock=1&user=1` | full page |
| `23a-mobile-overview-anonymous.png` | Mobile Overview, anonymous | 375×812 emulation, `?mock=1` | bottom bar Overview · About; full page |
| `23b-mobile-overview-signed-in.png` | Mobile Overview, signed in | `?mock=1&user=1` | bottom bar Overview · Fans · Alerts · Settings · More; full page |
| `24-mobile-fans.png` | Mobile Fans | `#fans` at 375 px | channel selector (segmented control) shows one channel at a time |
| `25-mobile-more-sheet.png` | Mobile More sheet | *More* in the bottom bar | the remaining pages with their group |
| `26-overview-24h.png` | Overview, 24 h range | `?mock=1&user=1` → *24 h* | range selector 2 h · 24 h · 7 d, averaged tier, `Www HH:MM` axis, CSV button; the sparklines keep the last 2 h |
| `27-header-cert-warning.png` | Header with the certificate warning chip | `&tls=soon` | crop (1280×60); the chip leads to Settings → Certificate |
| `28-home-assistant-tiles.png` | Home Assistant, System view with the n5-fangov tiles | not from the mock: a real Home Assistant with the rest sensors of [API and integrations](../12-api.md#home-assistant); labels re-set to the documentation wording | static asset — currently still stored as `32-home-assistant-tiles.png`, rename with the regeneration |

The 0.3.1 set (`01`–`32`, tabs and dialogs of the old dashboard) stays in this directory
until the 0.4.0 pages are merged and the script has been run against them; the image
references in [Dashboard](../04-dashboard.md) and the [README](../../README.md) are
updated in the same step.

Not reproducible from the mock and therefore not included: the browser's own
certificate warning and the OS trust dialogs ([HTTPS](../08-https-security.md#the-certificate),
three steps) and the PVE notification matcher ([Alerts](../07-alerts.md#the-pve-side))
— take those on a real box when needed.
