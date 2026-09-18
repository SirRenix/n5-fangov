# 0.4.0 dashboard prototype

A throw-away prototype of the redesigned shell from `DESIGN.md` §11a, rendered against
the mock only. Nothing here is served by the daemon: `proto.html` loads the production
`app.css` and `mock.js` by relative path and draws the new structure with its own
`proto.js` / `proto.css`. The operator decided on its screenshots (2026-09-18) and the real
`index.html` / `app.js` / `app.css` were rebuilt as 0.4.0-rc1. The prototype is kept for
reference until 0.4.0 final and is deleted then; it is not maintained.

## Run it

Serve the repository root (any static server), open

    /docs/design/proto/proto.html?mock=1&user=1

Flags (in addition to the mock's own `&user=1 &tls=… &pwm4=1 &schedfail=1`):

| Flag | Values | Open decision |
|---|---|---|
| `nav` | `side` (default) · `rail` · `top` | sidebar always visible, collapsed rail, or a top bar |
| `spark` | `1` (default) · `0` | sparklines on the channel tiles |
| `fans` | `stack` (default) · `tabs` | all channels stacked or one at a time with a selector |
| `compat` | `page` (default) · `about` | Compatibility as its own page or folded into About |
| `page` | page id | `overview system fans schedules alerts log settings compat about` |
| `theme` | `dark` (default) · `light` | |
| `dlg` | `login` · `preset` · `more` | open the sign-in dialog, the preset editor, the phone *More* sheet |

The sidebar collapse button and the *Navigation* select on the Settings page switch
`nav` live (stored under `n5-proto-nav`). The curve canvases are drawn, not draggable;
the switches, sliders and buttons only toast — no state changes, no API writes.

## Screenshots and comparison page

`shots.mjs` captures the current dashboard and the prototype at 1920 / 1280 / 375 px in
both themes, every page, plus the variants; `compare.mjs` renders them as one page with
old and new side by side. Same harness as `docs/screenshots/shots.mjs`:

    node scratch/serve.js <parent of the repo> 8797            # any static server
    chrome --headless=new --remote-debugging-port=9223 --lang=en-US --hide-scrollbars about:blank
    node docs/design/proto/shots.mjs ../n5-fangov-redesign/shots http://127.0.0.1:8797/n5-fangov 9223
    node docs/design/proto/compare.mjs ../n5-fangov-redesign/shots ../n5-fangov-redesign/compare.html

The output stays outside the repository (≈ 160 PNG, 16 MB).
