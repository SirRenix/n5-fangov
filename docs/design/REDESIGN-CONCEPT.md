# Dashboard redesign — concept (built in 0.4.0)

Status: **built in 0.4.0-rc1** (2026-09-18, branch `feat/redesign`): the running dashboard
is described in `docs/04-dashboard.md` and DESIGN §11, the decisions and rules in DESIGN
§11a; this page is the concept and the record of the decisions. Written 2026-09-16.
Owner: operator. It assumed the 0.3.x features (tokens, webhook transport, schedules,
longer history), which shipped in 0.3.1, because the new structure has a place for each
of them; panels marked *(0.4.0)* below were written before that.

## Why a structural change, not a facelift

The 0.3 dashboard is a sound operator console: status tiles, two charts, tabs, dark-first
tokens, keyboard and screen-reader support, AA contrast. What makes it feel dated next to
current tools (Pulse, Beszel, Home Assistant) is structure, not colour:

1. **Nine top tabs** do not scale; on a phone four of them are off-screen.
2. **Function-centric, not channel-centric.** To tune one fan the operator hops between
   Curves, Manual and Presets.
3. **Settings are scattered** across the gear popover, the account dialog, the certificate
   dialog and the alert transport form — the token panel (0.4.0) would be a fifth place.

Everything else stays: the visual language (tokens in `app.css`), the anonymous overview,
the CSP without inline code, no framework, the mock, the JS budget discipline.

## Target structure

```
┌─────────────┬───────────────────────────────────────────────────────────────┐
│ n5-fangov   │  Overview                                   ● ok  up 5d  admin │
│ ─────────── │ ───────────────────────────────────────────────────────────── │
│ MONITOR     │  [cpu 43.4°]  [ssd 41.8°]  [hdd 36.6°]   ← tiles + sparklines │
│ ▸ Overview  │  Temperature 2h|24h|7d     Fan speed 2h|24h|7d                 │
│   System    │  Sensors (all disks, GPU, NIC, EC)    Recent alerts            │
│ CONTROL     │                                                                │
│   Fans      │                                                                │
│   Schedules │  (0.4.0)                                                       │
│ OPERATE     │                                                                │
│   Alerts    │                                                                │
│   Log       │                                                                │
│ SETTINGS    │                                                                │
│   Settings  │                                                                │
│ INFO        │                                                                │
│   Compat.   │                                                                │
│   About     │                                                                │
│ ─────────── │                                                                │
│ ‹ collapse  │                                                                │
└─────────────┴───────────────────────────────────────────────────────────────┘
```

- **Sidebar** (220 px, collapses to a 56-px icon rail; on phones a bottom bar with
  Overview · Fans · Alerts · Settings · More). Groups: Monitor, Control, Operate,
  Settings, Info. Anonymous visitors see Overview and About only, as today.
- **Page header** replaces the crowded top bar: page title left; status chip, uptime,
  version badge, user and sign-out right; the lock moves into Settings → Certificate and
  only surfaces in the header as a warning chip when the certificate is in fallback or
  about to expire.

### Fans page (merges Curves, Manual, Presets)

```
┌ cpu  · pwm1 · k10temp ─────────────────────────── AUTO ─┐
│  curve editor (canvas + points table)   │ now 43.4 °C → 85   │
│                                          │ override [====]  85 │
│                                          │ [Set] [Back to auto]│
│  hysteresis · min on-time (0.4.0)        │ preset: n5pro-quiet │
└──────────────────────────────────────────┴─────────────────────┘
┌ ssd … ┐  ┌ hdd … ┐
Presets row: [n5pro-quiet ●active] [n5pro-balanced ★] [n5pro-cool] [my-summer ✎ 🗑]  [Apply…]
Sticky action bar: ● unsaved changes   [Revert] [Apply to daemon]
```

One panel per channel: curve left, live value + override right, the preset that matches
the current curves as a badge. Presets become a row of chips with details on hover/click,
built-in and user presets distinguished as today. The dirty indicator and the sticky
action bar stay. Manual override keeps the number field and the HDD minimum.

### Settings page (merges gear popover, account dialog, certificate dialog, alert transport)

Sections in one scrolling page with a sub-navigation on the left (desktop) or an
accordion (phone): **Display** (unit, interval, theme) · **Account & sessions** ·
**API tokens** *(0.4.0)* · **Certificate** (the current panel as a section) ·
**Alert transport** (transport, mail_to, webhook *(0.4.0)*, test alert, PVE template) ·
**Backup** (export/import bundle) · **Danger zone** (clear log, reset certificate).

### Overview refinements

- Channel tiles get a 2-hour **sparkline** (temperature) and keep duty bar + RPM.
- Charts get a range switch 2 h / 24 h / 7 d *(0.4.0 history)*; the extra-sensor chart
  becomes a third chart of the same kind, not a separate card.
- The Sensors card shows **every disk individually** *(0.4.0 per-device sensor ids)*,
  grouped as today, with the "chart" toggle.
- Empty states carry a next step ("No presets yet — save the current curves or apply a
  built-in one").

### Iconography and type

- One inline **SVG icon set** (16 px, `currentColor`): tabs, status, lock, gear, close,
  external link, chart toggle — replaces ⚙ 🔒 × and the text badges where an icon is
  clearer. Every icon has an accessible name.
- Type scale from the token block (`--fs-*`): page title 20/600, card title 13/600
  uppercase tracked, body 13, meta 12, tile value 32/700. No new families.
- Cards keep radius/border/shadow tokens; spacing on the 4-px grid; no glass effects,
  no motion beyond the existing reduced-motion-aware transitions.

## What does not change

- Anonymous view (tiles + charts + About), the API, the mock (`?mock=1` must render every
  page), the CSP, the JS-only build, the visibility model, keyboard and screen-reader
  behaviour (the sidebar is a `nav` with `aria-current`, the bottom bar likewise).
- The controller and every backend package. This is a `internal/web/static` change plus
  `web_test.go` (static checks, JS budget) and the screenshot script.

## Method for the session

1. **Prototype in the mock first**: a branch, `?mock=1` renders the new structure with
   the existing mock data; screenshots at 1920, 1280, 375 px for both themes; the
   operator decides before any test or documentation work.
2. Then build: split the mock out of the production bundle first (it is 22 % of
   `app.js`), so the budget has room; `TestTabsHaveHandlers` becomes "every nav entry has
   a page"; the screenshot script and `docs/screenshots/README.md` follow the new pages.
3. Review with the design checklist (`docs/DESIGN-AUDIT.md` sections 5–7): contrast
   table, keyboard walk of every task, 375-px pass, no console messages.
4. Acceptance: every task of the click-path table in `docs/DESIGN-AUDIT.md` §4 in the
   same or fewer clicks; no protected data on the anonymous pages; JS budget documented.

## Findings from the 0.3.1 release gate (2026-09-18)

UX items the manual gate test surfaced against 0.3.1 ("today" below = 0.3.1). The first
three are built in 0.4.0-rc1 (preset editor, Schedules page, override switch); the
certificate walkthrough with screenshots is still open (CHANGELOG, *Unreleased*):

- **Preset editor.** Compose a set's values before saving. Today *Save current as…*
  stores the curves the daemon runs, so the new preset is the active one at once; other
  values take the detour Curves → Apply → Save.
- **Editable Schedules card.** Today the card is read-only and `[[schedule]]` is edited
  in the config file only.
- **Explicit manual toggle per channel** on the Manual tab. The slider starts at the
  duty the curve is writing and *Set* is the only switch into `MANUAL`; testers expect
  an on/off control next to the slider.
- **Certificate-trust walkthrough with screenshots** of the English Windows wizard: the
  wizard's default store choice (*Automatically select…*) is the step that fails
  silently, and text alone did not prevent it.

## Decisions (operator, 2026-09-18, on the prototype screenshots)

- **Sidebar**, expanded by default, collapsible to the icon rail; the top-bar variant is
  not built (a 2026 admin tool has a grouped left navigation; the top bar degenerates to
  the old tab row at nine entries).
- **Sparklines** on the channel tiles: yes.
- **Fans page:** all channels stacked on desktop; below 700 px a channel selector shows one
  channel at a time (breakpoint, not a setting).
- **Compatibility** folds into About as a card (`#about/compat`); the sidebar has eight
  entries.

The decisions and rules are `DESIGN.md` §11a; the prototype and the comparison page are
described in `proto/README.md` (kept for reference until 0.4.0 final).
