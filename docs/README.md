# n5-fangov documentation

One page per topic, in the order a new box goes through them. The landing page is the
repository [README](../README.md).

| Page | Covers |
|---|---|
| [01 Install](01-install.md) | prerequisites, release binary or build, `.deb`, what the installer does, file layout |
| [02 Kernel driver](02-kernel-driver.md) | the N5 Pro EC module: why out-of-tree, DKMS, install, `experimental_write=1`, verify, kernel-update gate, remove |
| [03 Setup](03-setup.md) | `n5-fangov setup`: profile, scope local/lan, password rules, non-interactive, first start, changing credentials |
| [04 Dashboard](04-dashboard.md) | every tab with a screenshot, settings gear, lock, account, extra sensors, phone layout |
| [05 CLI reference](05-cli.md) | all subcommands and flags, exit codes, environment, the socket |
| [06 Configuration](06-configuration.md) | every key of `config.toml`, curve rules, presets and the built-in sets, strict writes |
| [07 Alerts and guards](07-alerts.md) | what the daemon guards against, alert kinds, transports, the PVE side, test alert |
| [08 HTTPS and security](08-https-security.md) | certificate and trust recipes, HSTS, visibility, sessions, throttling, hashes, sandbox, reverse proxy |
| [09 Updates](09-updates.md) | update by release or package, rollback, backup and restore, uninstall |
| [10 Troubleshooting](10-troubleshooting.md) | symptom → cause → fix, what `check` reports, logs |
| [11 Development](11-development.md) | build, tests, mock, screenshots, design contract, versioning, releases |

Also here: [RELEASE-GATE.md](RELEASE-GATE.md) (the manual test before a release),
[screenshots/](screenshots/README.md) (the image set and how to regenerate it),
`AUDIT.md` and `DESIGN-AUDIT.md` (the audits behind the current state),
[design/REDESIGN-CONCEPT.md](design/REDESIGN-CONCEPT.md) (planned dashboard redesign).
