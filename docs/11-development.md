# Development

What this page covers: building with or without a local Go toolchain, the tests and
the race run, the dashboard mock, the JS budget, regenerating the screenshots, the
design contract, versioning and the release workflow. Conventions for contributions
(style, commits, pull requests) are in [CONTRIBUTING.md](../CONTRIBUTING.md); the
manual test before a release is [RELEASE-GATE.md](RELEASE-GATE.md).

- [Build](#build)
- [Tests](#tests)
- [Dashboard mock](#dashboard-mock)
- [Screenshots](#screenshots)
- [Design contract](#design-contract)
- [Versioning](#versioning)
- [Release workflow](#release-workflow)
- [Licence](#licence)

## Build

- **With Go installed** (version pinned in `go.mod`): `make build` → `dist/n5-fangov`
  (linux/amd64, static, stripped, `CGO_ENABLED=0`); plainly:
  `CGO_ENABLED=0 go build ./cmd/n5-fangov`. `make check` = `go vet` + `go test`.
  `make deb` builds the Debian package with `dpkg-deb` (no debhelper).
- **Without Go, from Windows:** `tools/remote-go.ps1` (PowerShell 7, `pwsh`) copies the
  tree to a Linux host over ssh, runs the build or tests in a `golang:<version>-alpine`
  container and fetches the artefacts (`-Fetch` puts the binary at `dist/n5-fangov`).
  `-Image golang:<version>-bookworm` selects a cgo-capable image, `-Cmd "…"` any make
  target.
- `make verify-deploy` runs `systemd-analyze verify` over the units and `apt-config`
  over the apt hook on a host that has them (the Docker build container skips both with
  a note), and always checks that the PVE template pair in `deploy/pve-notification/`
  matches the embedded copy in `internal/alert/templates/`. Run it on the target box
  after editing the unit.

## Tests

- `go test ./...` must pass in the Alpine builder. The fake sysfs `testdata/sysfs/n5pro`
  mirrors the reference host with generic names (`N5FANGOV_SYSFS` points there);
  sysinfo tests create the PCI directories and symlinks (which a Windows checkout
  cannot hold) in a temp copy. No test writes into `testdata/`.
- `make test-race` (`CGO_ENABLED=1 go test -race -count=1 ./...`) needs cgo — run it
  once per release in the bookworm image:
  `tools/remote-go.ps1 -Image golang:<version>-bookworm -Cmd "make test-race"`.
  Limiter, session store, alert ring, config writers and the TLS hot swap are
  concurrency code; a change there is not done until the race run is clean.
- Controller changes: the safety rules are pinned in `internal/control/safety_test.go`
  (fake `Device`, no hardware). `cmd/n5-fangov/serve_smoke_test.go` starts the real
  daemon in dry-run against the fake sysfs (TCP + socket, override, SIGTERM).
- Frontend: `internal/web/web_test.go` checks the JS budget, that every tab has a
  handler, the CSP and the contrast of the primary buttons. Everything else is checked
  by hand against the mock.
- Before a tag: the release is verified on the reference N5 Pro (channel mapping, stop
  behaviour, a multi-hour run) — the README's
  [Tested hardware](../README.md#tested-hardware) — and, before an `-rc` becomes a
  release, the operator runs [RELEASE-GATE.md](RELEASE-GATE.md).

## Dashboard mock

`internal/web/static/` is plain HTML/CSS/JS, no build step, CSP `script-src 'self'`, no
`innerHTML`, no inline handlers. Colours, spacing, radii and the type scale are tokens
in the `:root` block of `app.css` and the constant block after `cssVar` in `app.js`;
new values go there, not inline.

Serve the directory with any static server and open `index.html?mock=1` — anonymous;
`&user=1` signed in (login `admin`/`admin`); `&auth=none`; `&tls=off|file|soon|fallback`;
`&tab=<id>`; `&syserr=1`. The mock implements every endpoint; new endpoints get a mock
branch in the same change.

**JS budget:** `app.js` ≤ 96 KB raw (`web_test.go`). Do not raise the limit to make a
change fit; move something to `index.html` markup or drop it.

## Screenshots

The images in `docs/screenshots/` come from the mock; the list of views, the mock
state behind each one and the regeneration command (`shots.mjs`, Node ≥ 22, headless
Chrome over the DevTools protocol) are in [screenshots/README.md](screenshots/README.md).
Regenerate the affected ones when a view changes; [Dashboard](04-dashboard.md) embeds
them.

## Design contract

[DESIGN.md](../DESIGN.md) is the package contract: non-negotiable safety rules, the
layout, every package's responsibilities, the API, the deploy files and the review tags
used in code comments. A change that touches a rule is written into DESIGN before it is
implemented. The audits that shaped the current state are `AUDIT.md` and
`DESIGN-AUDIT.md` in this directory; the planned dashboard redesign is in
[design/REDESIGN-CONCEPT.md](design/REDESIGN-CONCEPT.md).

## Versioning

`internal/version/version.go` holds the only version literal — the current pre-release,
shown by `n5-fangov version`, in the dashboard header (`beta` badge until a release tag
drops the suffix; `GET /api/version` and `/api/about` carry it as `prerelease`) and
listed in [CHANGELOG.md](../CHANGELOG.md). `make` overrides it with `git describe`
(`vX.Y.Z-3-gabcdef` on commits after a tag). The Debian package version maps
`-alpha`/`-beta`/`-rc` to `~` (so `X.Y.Z~beta.1` sorts before `X.Y.Z`) and every other
`-` to `+` (`X.Y.Z~beta.1+3+gabcdef` sorts after the tag it is based on).

## Release workflow

`git tag -a vX.Y.Z` on a verified commit, then `make release NOTES="…"` (on a clean tag,
gh CLI signed in) creates the GitHub release with the static binary and its sha256 —
the releases page linked from the About tab is maintained this way, one entry per tag.
The version literal is bumped in the release commit and nowhere else. The
[release install](01-install.md#from-a-github-release) consumes exactly these assets.

## Licence

GPL-2.0-only; the full text is in [LICENSE](../LICENSE). The only dependency,
[`BurntSushi/toml`](https://github.com/BurntSushi/toml), is MIT-licensed; its text is in
`deploy/debian/copyright` (DEP-5). The About tab lists the credits
([Dashboard](04-dashboard.md#compatibility-and-about)).

Next: [CONTRIBUTING.md](../CONTRIBUTING.md) · [RELEASE-GATE.md](RELEASE-GATE.md) ·
[DESIGN.md](../DESIGN.md)
