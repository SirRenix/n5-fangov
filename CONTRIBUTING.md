# Contributing

n5-fangov regulates fans on a root-level host. Every change goes through the package
contract in [DESIGN.md](DESIGN.md); read the rules there first. A change that touches
them is written into DESIGN before it is implemented. Security issues go to
[SECURITY.md](SECURITY.md), not a public issue.

Build, tests, race run, dashboard mock, JS budget, screenshots:
[docs/11-development.md](docs/11-development.md). The manual test an `-rc` has to pass
before it becomes a release: [docs/RELEASE-GATE.md](docs/RELEASE-GATE.md).

## Style

- English everywhere: code, comments, log lines, UI, docs. British spelling in the UI
  (`licence`, `colour`); keep it consistent within a file.
- Documentation is written for the operator who runs the box: facts, paths, commands,
  short sentences. One page per topic under `docs/`; a fact lives on one page and is
  linked from the others, not copied. No history in the docs; that is `CHANGELOG.md`.
- No host names, addresses or e-mail addresses of real systems in code, tests,
  fixtures, mock or docs. Use `n5host`, `192.0.2.x`, `example.test`. No n5-fangov
  version literals in the docs: `<version>` / `vX.Y.Z`. Exceptions: the results
  table in `docs/RELEASE-GATE.md`, `CHANGELOG.md`, and the tested-combination line
  (README, kernel driver page). The literal the build uses is in
  `internal/version/version.go`.
- No finding tags (`M1`…`M7`, `H1`…`H4`, `L1`…`L9`, `R-M*`, `R-L*`) in comments or test
  names. A comment states the fact it protects; a test is named after what it pins.
  The legend of those tags is [docs/REVIEW-TAGS.md](docs/REVIEW-TAGS.md); findings from
  `docs/AUDIT.md` are referenced by their table row.
- Go: `gofmt`, `go vet`, `staticcheck -checks all` clean; errors wrapped with `%w`;
  every exported symbol has a doc comment that starts with its name.
- Log lines are one line, English, without secrets (hashes, tokens, passwords are
  redacted at the source).

## Commits and pull requests

- One topic per commit; subject `area: what changed` (`web: …`, `control: …`,
  `deploy: …`, `docs: …`, `DESIGN: …`), imperative, no trailing period; the body says
  why when the diff does not. Reference audit rows or tag ids in the body.
- A change in behaviour updates the affected page under `docs/`, DESIGN and
  `CHANGELOG.md` (*Unreleased*) in the same commit or PR; a changed view gets its
  screenshot regenerated.
- Never commit `dist/`, host-specific configs or exported bundles.
- Releases: `git tag -a vX.Y.Z` on a verified commit, then `make release NOTES="…"`;
  the version literal is bumped in the release commit and nowhere else
  ([release workflow](docs/11-development.md#release-workflow)).
