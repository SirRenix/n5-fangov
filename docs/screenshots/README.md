# Dashboard screenshots

All images come from the built-in mock (`internal/web/static/index.html?mock=1`), so
they show example values only (`n5host`, `192.0.2.x`, `n5.lan`, `fans.example`); no
data from a real host. Desktop views are 1280 px wide, dark theme, sidebar expanded;
mobile views are 375 px wide. The version in the header and the sidebar footer is the
mock's version string (`MV` in `mock.js`); a release carries no badge, a pre-release
suffix shows as one.

Regenerate the set with `shots.mjs` (Node ≥ 22, Chrome/Chromium over the DevTools
protocol; the mock is `mock.js` next to `app.js`, requested only behind `?mock=1`):
serve `internal/web/static/` on a local port, start Chrome headless with
`--remote-debugging-port=9223 --lang=en-US --hide-scrollbars`, then

```
node docs/screenshots/shots.mjs docs/screenshots http://127.0.0.1:8797/index.html 9223
```

A **page** shot is a full-page capture: the emulated viewport grows to the document
height (capped at 3600 px) before the capture, so the 100 vh sidebar and the sticky
action bar span the whole image. Dialogs, the header crops and toasts are viewport
clips at 1280×800. Pages are reached by hash (`#fans`, `#settings/st-cert`,
`#about/compat`); the mock's `&tab=` parameter maps `curves|manual|presets → fans` and
`compat → about`. The "Mock mode" toast is removed before each capture; every
interactive state is reached by clicking the same controls a user would.

| File | View | Mock state | Note |
|---|---|---|---|
| `01-overview-anonymous.png` | Overview, anonymous | `?mock=1` | tiles + charts only; full page |
| `02-login-dialog.png` | Sign-in dialog | `?mock=1` → *Sign in* | *Remember me* |
| `03-overview-signed-in.png` | Overview, signed in | `?mock=1&user=1` | full page: tiles with 2 h sparklines, three charts (Extra sensors as the third), Sensors in collapsible groups (name · count · live max; channel and charted groups open), System glance, Recent alerts |
| `04-header.png` | Header crop | `?mock=1&user=1` | title, status chip, uptime, version (no badge on a release), live, user, *Sign out* (1280×60) |
| `05-sidebar-rail.png` | Sidebar collapsed | the panel-left toggle in the brand row | 56 px icon rail: the logo alone, the toggle directly under it (nothing in the page header); state in `localStorage` (`nav`) |
| `06-system.png` | System | `#system` | Host / Machine / CPU / Fan controller, memory modules, GPU · NPU, Network, Storage; full page |
| `07-fans.png` | Fans | `#fans` | one card per channel: curve editor left, *Live & override* right, the preset badge listing every matching preset (`alternative · n5pro-balanced` on cpu and ssd), the Presets card with the *Active set* line and its Apply · New preset… hint, sticky action bar; the curve editor draws the `crit` line and, dimmer, the daemon's `ceiling` line; full page |
| `08-fans-validation-error.png` | Fans, validation error | critical cleared, stop `300` → *Apply* | red notice, nothing sent |
| `09-fans-restart-required.png` | Fans, restart required | `&restart=1`, critical +1 → *Apply* (the mock answers 202) | restart notice, stays after the editor reload until *Revert* |
| `10-fans-manual-switch.png` | Fans, Manual switch on | hdd channel: *Manual override* switch → on, slider dragged below the minimum | `role="switch"`; the slider stops at the minimum 60, the hint explains why (HDD-like channel) |
| `11-preset-editor.png` | Preset editor dialog | *New preset…* | *Start from* (the daemon — default —, the editor, a preset), name, per-channel fields and point tables, prefilled from the running curves; no description field |
| `12-schedules.png` | Schedules | `#schedules` | editable rows (preset, from / to, days, fallback), ACTIVE, *Add entry*, status block with the daemon clock (*now*); full page |
| `13-schedules-last-switch-failed.png` | Schedules, failed last switch | `&schedfail=1` | warn notice, error in the status block |
| `14-alerts.png` | Alerts | `#alerts` | effective transport, kinds with last delivery, recent alerts, *Send test alert*; full page |
| `15-alerts-test-toast.png` | Test-alert toast | *Send test alert* | crop of the lower right corner (640×240) |
| `16-log.png` | Log | `#log` | filter, auto-scroll, Refresh, Export (Clear is in Settings → Danger zone); full page |
| `17-settings.png` | Settings | `#settings` | Display · Account & sessions · API tokens · Certificate · Alert transport · Backup · Danger zone, sub-navigation left; full page |
| `18a-settings-certificate-auto.png` | Certificate section, automatic | `#settings/st-cert`, "How to trust" opened | fingerprint, SAN chips, downloads |
| `18b-certificate-fallback.png` | Certificate section, fallback | `&tls=fallback` | warn badge *automatic (fallback)* |
| `18c-certificate-soon.png` | Certificate section, expiring | `&tls=soon` | expiry warning |
| `18d-certificate-off.png` | Certificate section, TLS off | `&tls=off` | loopback explanation |
| `19-settings-tokens.png` | Settings → API tokens | `#settings/st-tokens` → *Create token…* → *Create* | tokens table, the secret shown once with *Copy* |
| `20-about-compatibility.png` | About with the Compatibility card | `#about/compat` | licence, links, credits; profiles table with ACTIVE, verified / untested badges; full page |
| `21-connection-lost.png` | Connection banner | banner unhidden by script (same DOM the client shows after two failed polls) | |
| `22-overview-light.png` | Overview, light theme | `localStorage` theme `light`, `?mock=1&user=1` | full page |
| `23a-mobile-overview-anonymous.png` | Mobile Overview, anonymous | 375×812 emulation, `?mock=1` | bottom bar Overview · More (About in the sheet); full page |
| `23b-mobile-overview-signed-in.png` | Mobile Overview, signed in | `?mock=1&user=1` | bottom bar Overview · Fans · Alerts · Settings · More; full page |
| `24-mobile-fans.png` | Mobile Fans | `#fans` at 375 px | channel selector (segmented control) shows one channel at a time |
| `25-mobile-more-sheet.png` | Mobile More sheet | *More* in the bottom bar | the remaining pages with their group |
| `26-overview-24h.png` | Overview, 24 h range | `?mock=1&user=1` → *24 h* | range selector 2 h · 24 h · 7 d, averaged tier, `Www HH:MM` axis, CSV button; the sparklines keep the last 2 h |
| `27-header-cert-warning.png` | Header with the certificate warning chip | `&tls=soon` | crop (1280×60): *certificate expires in 12 d*; the chip leads to Settings → Certificate |
| `28-home-assistant-tiles.png` | Home Assistant, System view with the n5-fangov tiles | not from the mock: a real Home Assistant with the rest sensors of [API and integrations](../12-api.md#home-assistant); labels re-set to the documentation wording | static asset, not touched by `shots.mjs` |

The set is embedded by [Dashboard](../04-dashboard.md), the [README](../../README.md)
and the [API page](../12-api.md) (28).

## Static assets (not from the mock)

Taken on real systems, not touched by `shots.mjs`; host names in them are documentation
values (`n5host`), nothing else identifies the host. The Windows set comes from a
German Windows 11; the layout is the same on an English Windows, the captions in
[HTTPS → Windows walkthrough](../08-https-security.md#windows-walkthrough) give both labels.

| File | Shows |
|---|---|
| `cert-windows-01-dialog.png` | the downloaded `.cer` opened: "not trusted", the *Zertifikat installieren…* (*Install Certificate…*) button |
| `cert-windows-02-wizard.png` | Certificate Import Wizard, store location *Lokaler Computer* (*Local Machine*) |
| `cert-windows-03-store-page.png` | store page with *Alle Zertifikate in folgendem Speicher speichern* (*Place all certificates in the following store*) chosen, not the automatic default |
| `cert-windows-04-browse.png` | *Durchsuchen…* (*Browse…*): *Vertrauenswürdige Stammzertifizierungsstellen* (*Trusted Root Certification Authorities*) |
| `cert-windows-05-store-chosen.png` | the store page with that store in the field |
| `cert-windows-06-finish.png` | the wizard's summary before *Fertig stellen* (*Finish*) |
| `cert-windows-07-imported.png` | *Der Importvorgang war erfolgreich* (*The import was successful*) |
| `cert-windows-08-chrome-secure.png` | Chrome after a full restart: *Verbindung ist sicher* (*Connection is secure*) |
| `28-home-assistant-tiles.png` | Home Assistant System view with the n5-fangov tiles (see the table above) |

Not reproducible from the mock and not included: the browser's own certificate warning
page (step 1 of [The certificate](../08-https-security.md#the-certificate)), the macOS,
Firefox and Android trust dialogs and the PVE notification matcher
([The PVE side](../07-alerts.md#the-pve-side)). Take those on a real box when needed.
