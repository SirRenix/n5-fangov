# Contributing

n5-fangov regulates fans on a root-level host. Every change goes through the package
contract in [DESIGN.md](DESIGN.md) — read the rules there first; a change that touches
them is written into DESIGN before it is implemented. Security issues: [SECURITY.md](SECURITY.md),
not a public issue.

## Build

- **With Go installed** (version pinned in `go.mod`): `make build` → `dist/n5-fangov`
  (linux/amd64, static, stripped, `CGO_ENABLED=0`). `make check` = `go vet` + `go test`.
  `make deb` builds the Debian package with `dpkg-deb` (no debhelper).
- **Without Go, from Windows:** `tools/remote-go.ps1` (PowerShell 7, `pwsh`) copies the
  tree to a Linux host over ssh, runs the build or tests in a `golang:<version>-alpine`
  container and fetches the artefacts (`-Fetch`). `-Image golang:<version>-bookworm`
  selects a cgo-capable image, `-Cmd "…"` any make target.
- `make verify-deploy` runs `systemd-analyze verify` over the units and `apt-config`
  over the apt hook on a host that has them, and always checks that the PVE template pair
  in `deploy/pve-notification/` matches the embedded copy in `internal/alert/templates/`.

## Tests

- `go test ./...` must pass in the Alpine builder. The fake sysfs `testdata/sysfs/n5pro`
  mirrors the reference host with generic names; sysinfo tests create the PCI directories
  and symlinks (which a Windows checkout cannot hold) in a temp copy. No test writes into
  `testdata/`.
- `make test-race` (`CGO_ENABLED=1 go test -race -count=1 ./...`) needs cgo — run it once
  per release in the bookworm image: `tools/remote-go.ps1 -Image golang:<version>-bookworm
  -Cmd "make test-race"`. Limiter, session store, alert ring, config writers and the TLS
  hot swap are concurrency code; a change there is not done until the race run is clean.
- Controller changes: the safety rules are pinned in `internal/control/safety_test.go`
  (fake `Device`, no hardware). `cmd/n5-fangov/serve_smoke_test.go` starts the real daemon
  in dry-run against the fake sysfs.
- Frontend: `internal/web/web_test.go` checks the JS budget, that every tab has a handler,
  the CSP and the contrast of the primary buttons. Everything else is checked by hand
  against the mock.
- Before a tag: the release is verified on the reference N5 Pro (channel mapping, stop
  behaviour, a multi-hour run) — see README "Compatibility".

## Dashboard work

- `internal/web/static/` is plain HTML/CSS/JS, no build step, CSP `script-src 'self'`,
  no `innerHTML`, no inline handlers. Colours, spacing, radii and the type scale are
  tokens in the `:root` block of `app.css` and the constant block after `cssVar` in
  `app.js`; new values go there, not inline.
- **Mock:** serve the directory with any static server and open
  `index.html?mock=1` — anonymous; `&user=1` signed in (login `admin`/`admin`);
  `&auth=none`; `&tls=off|file|soon|fallback`; `&tab=<id>`; `&syserr=1`. The mock
  implements every endpoint; new endpoints get a mock branch in the same change.
- **JS budget:** `app.js` ≤ 96 KB raw (`web_test.go`). Do not raise the limit to make a
  change fit; move something to `index.html` markup or drop it.
- Screenshots for the docs come from the mock (list and URLs in
  [docs/screenshots/README.md](docs/screenshots/README.md)); regenerate the affected
  ones when a view changes.

## Style

- English everywhere: code, comments, log lines, UI, docs. British spelling is used in
  the UI (`licence`, `colour`); keep it consistent within a file.
- Documentation is written for the operator who runs the box, in the tone of a runbook:
  facts, paths, commands. No host names, addresses or e-mail addresses of real systems in
  code, tests, fixtures, mock or docs — use `n5host`, `192.0.2.x`, `example.test`.
- Review tags in comments and test names (`M1`…`M7`, `H1`…`H4`, `L1`…`L9`, `R-M*`, `R-L*`)
  refer to the review rounds listed in [DESIGN.md](DESIGN.md) "Review tags in the code";
  new findings from `docs/AUDIT.md` are referenced by their table row, not by a new tag
  scheme. Write the fact the comment protects in the same sentence, so the tag is
  optional to read.
- Go: `gofmt`, `go vet`, `staticcheck -checks all` clean; errors wrapped with `%w`; every
  exported symbol has a doc comment that starts with its name.
- Log lines are one line, English, without secrets (hashes, tokens, passwords are
  redacted at the source).

## Commits and pull requests

- One topic per commit; subject `area: what changed` (`web: …`, `control: …`, `deploy: …`,
  `README: …`, `DESIGN: …`), imperative, no trailing period; body says why when the diff
  does not. Reference audit rows or review tags in the body.
- A change in behaviour updates README (operator view), DESIGN (contract) and
  `CHANGELOG.md` (*Unreleased*) in the same commit or PR.
- Never commit `dist/`, host-specific configs or exported bundles.
- Releases: `git tag -a vX.Y.Z` on a verified commit, then `make release NOTES="…"`; the
  version literal in `internal/version/version.go` is bumped in the release commit and
  nowhere else.
