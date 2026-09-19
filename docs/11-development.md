# Development

Building, tests, the dashboard mock, screenshots, the design contract, versioning and
releases. Conventions for contributions: [CONTRIBUTING.md](../CONTRIBUTING.md). The
manual test before a release: [RELEASE-GATE.md](RELEASE-GATE.md).

## Build

- **With Go installed** (version pinned in `go.mod`): `make build` → `dist/n5-fangov`
  (linux/amd64, static, stripped, `CGO_ENABLED=0`); plainly
  `CGO_ENABLED=0 go build ./cmd/n5-fangov`. `make check` = `go vet` + `go test`.
  `make deb` builds the Debian package with `dpkg-deb` (no debhelper).
- **Without Go, from Windows:** `tools/remote-go.ps1` (PowerShell 7, `pwsh`) copies the
  tree to a Linux host over ssh, runs the build or tests in a `golang:<version>-alpine`
  container and fetches the artefacts (`-Fetch` puts the binary at `dist/n5-fangov`).
  `-Image golang:<version>-bookworm` selects a cgo-capable image, `-Cmd "…"` any make
  target.
- `make verify-deploy` runs `systemd-analyze verify` over the units and `apt-config`
  over the apt hook on a host that has them (the Docker build container skips both with
  a note), and checks that the PVE template pair in `deploy/pve-notification/` matches
  the embedded copy in `internal/alert/templates/`. Run it on the target box after
  editing the unit.

## Tests

- `go test ./...` must pass in the Alpine builder. The fake sysfs `testdata/sysfs/n5pro`
  mirrors the reference host with generic names (`N5FANGOV_SYSFS` points there);
  sysinfo tests create the PCI directories and symlinks, which a Windows checkout
  cannot hold, in a temp copy. No test writes into `testdata/`.
- `make test-race` (`CGO_ENABLED=1 go test -race -count=1 ./...`) needs cgo; run it
  once per release in the bookworm image:
  `tools/remote-go.ps1 -Image golang:<version>-bookworm -Cmd "make test-race"`.
  Limiter, session store, alert ring, config writers and the TLS hot swap are
  concurrency code; a change there is not done until the race run is clean.
- The safety rules are pinned in `internal/control/safety_test.go` (fake `Device`, no
  hardware). `cmd/n5-fangov/serve_smoke_test.go` starts the real daemon in dry-run
  against the fake sysfs (TCP + socket, override, SIGTERM). `internal/history` and
  `internal/schedule` are tested with a fake clock and fixed times (tiers, bucket
  means, corrupt file; midnight crossing, days, fallback, next switch, a DST day).
  Disk sensors and the sysinfo disk temperatures run in a temp copy of the fake sysfs
  with `block/<dev>/device/hwmon/hwmonN/temp1_input` (plain directories, the Windows
  checkout cannot hold the symlinks).
- The API route table in `internal/web/openapi.go` is the only place a route is
  declared: the mux, the scope guard and `GET /api/openapi.json` come from it.
  `TestOpenAPICoversRoutes` pins every registered pattern to the document and vice
  versa, `TestOpenAPIShape` the version, summaries, responses and scope values. A new
  endpoint is one row plus its schema; the [endpoint table](12-api.md#endpoints)
  follows the row.
- Frontend: `internal/web/web_test.go` checks the JS budgets, that `index.html` never
  references `mock.js`, that every page has a nav entry and a handler
  (`TestNavHasPages`), the CSP and the contrast of the primary buttons. Everything else is checked by hand against the mock.
- Before a tag the release is verified on the reference N5 Pro
  ([Tested hardware](../README.md#tested-hardware)); before an `-rc` becomes a
  release, the operator runs [RELEASE-GATE.md](RELEASE-GATE.md).

## Dashboard mock

`internal/web/static/` is plain HTML/CSS/JS, no build step, CSP `script-src 'self'`, no
`innerHTML`, no inline handlers. Colours, spacing, radii and the type scale are tokens
in the `:root` block of `app.css` and the constant block after `cssVar` in `app.js`;
new values go there, not inline.

The mock is its own file, `mock.js`, served by the static handler but never referenced
by `index.html`. With `?mock=1` `app.js` inserts the script tag and routes every `api()`
call to `window.n5mock(path, opt)` instead of `fetch`. Serve the directory with any
static server and open `index.html?mock=1`; the flags (`&user=1`, `&auth=none`,
`&tls=…`, `&reject=1`, `&restart=1`, `&expire=1`, `&schedfail=1`, `&pwm4=1`, `&lag=1`,
`&down=1`, …) and the pages by hash are listed in
[Dashboard: The mock](04-dashboard.md#the-mock). The mock implements every endpoint;
new endpoints get a mock branch in the same change. Its version string is one constant
in `mock.js`, bumped with the release.

**Budgets:** `app.js` ≤ 136 KiB, `mock.js` ≤ 48 KiB, `app.css` ≤ 48 KiB raw
(`web_test.go`); `index.html` carries the SVG sprite and has no budget. Do not raise a
limit to make a change fit; a raise is a CHANGELOG entry. Move something to
`index.html` markup or drop it.

## Screenshots

The images in `docs/screenshots/` come from the mock. The list of views, the mock state
behind each one and the regeneration command (`shots.mjs`, Node ≥ 22, headless Chrome
over the DevTools protocol) are in [screenshots/README.md](screenshots/README.md).
Regenerate the affected ones when a view changes; [Dashboard](04-dashboard.md) embeds
them.

## Fans scenario matrix

`tools/fans-matrix.mjs` drives the Fans page of the mock (`?mock=1&user=1&lag=1`)
through the control paths around curves, presets and the override switch: preset
apply (shared channels → badge lists both), edit → Revert / Apply / *Save as preset…*,
*New preset…*, override on → *Set* → off with the daemon's lag, override kept across
an apply, the HDD minimum, client validation, sign-out and session loss with dirty
edits, the 375 px selector, delete and rename of the active preset, `&restart=1`, a
preset applied from elsewhere. It prints PASS/FAIL per scenario and exits 1 on a FAIL;
run it after a change to the Fans logic, before the screenshots. Same setup as the
screenshots: serve `internal/web/static/`, start headless Chrome with a DevTools port,
then `node tools/fans-matrix.mjs http://127.0.0.1:8806/index.html 9237` (a few
minutes; every lagged transition waits for the poll).

## Design contract

[DESIGN.md](../DESIGN.md) is the package contract: non-negotiable safety rules, the
layout, every package's responsibilities, the API and the deploy files. A change that
touches a rule is written into DESIGN before it is implemented. Files are named by
topic, never by version or finding round, and a test file carries the name of the file
or topic it tests. `AUDIT.md` and `DESIGN-AUDIT.md` in this directory are the audits
behind the current state; [REVIEW-TAGS.md](REVIEW-TAGS.md) is the legend of the finding
tags (the code carries none); [design/REDESIGN-CONCEPT.md](design/REDESIGN-CONCEPT.md)
is the concept behind the dashboard.

## Versioning

`internal/version/version.go` holds the only version literal. It is shown by
`n5-fangov version`, in the dashboard header (a pre-release suffix as a badge until a
release tag drops it; `GET /api/version` and `/api/about` carry it as `prerelease`) and
listed in [CHANGELOG.md](../CHANGELOG.md). `make` overrides it with `git describe`
(`vX.Y.Z-3-gabcdef` on commits after a tag). The Debian package version maps
`-alpha`/`-beta`/`-rc` to `~` (so `X.Y.Z~beta.1` sorts before `X.Y.Z`) and every other
`-` to `+` (`X.Y.Z~beta.1+3+gabcdef` sorts after the tag it is based on).

## Release workflow

`git tag -a vX.Y.Z` on a verified commit, then `make release NOTES="…"` (on a clean tag,
gh CLI signed in) creates the GitHub release with the static binary and its sha256; the
releases page linked from the About page is maintained this way, one entry per tag. The
version literal is bumped in the release commit and nowhere else. The
[release install](01-install.md#from-a-github-release) consumes exactly these assets.

## Licence

GPL-2.0-only; the full text is in [LICENSE](../LICENSE). The only dependency,
[`BurntSushi/toml`](https://github.com/BurntSushi/toml), is MIT-licensed; its text is in
`deploy/debian/copyright` (DEP-5). The About page lists the credits
([About](04-dashboard.md#about)).
