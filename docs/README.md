# n5-fangov documentation

One page per topic, in the order a new box goes through them. The landing page is the
repository [README](../README.md).

| Page | Covers |
|---|---|
| [01 Install](01-install.md) | prerequisites, release binary or build, the `.deb`, what the installer does, file layout |
| [02 Kernel driver](02-kernel-driver.md) | the N5 Pro EC module: DKMS, install, `experimental_write=1`, verify, kernel-update gate, remove |
| [03 Setup](03-setup.md) | `n5-fangov setup`: profile, scope local/lan, password, non-interactive form, first start, changing credentials |
| [04 Dashboard](04-dashboard.md) | the shell, every page with a screenshot, dialogs, keyboard, deep links, the mock, connection loss |
| [05 CLI reference](05-cli.md) | all subcommands and flags, exit codes, environment, the socket |
| [06 Configuration](06-configuration.md) | every key of `config.toml`, curve rules, hysteresis and min_on, ceilings and the emergency hook, sensor ids, presets, pwm4, schedules, strict writes |
| [07 Alerts and guards](07-alerts.md) | what the daemon guards against, alert kinds, transports incl. webhook, the PVE side, test alert |
| [08 HTTPS and security](08-https-security.md) | certificate and trust recipes, HSTS, who sees what, sessions, API tokens, throttling, hashes, sandbox, reverse proxy |
| [09 Updates](09-updates.md) | update by release or package, rollback, backup and restore, uninstall |
| [10 Troubleshooting](10-troubleshooting.md) | symptom → cause → fix, what `check` reports, logs |
| [11 Development](11-development.md) | build, tests, mock, screenshots, design contract, versioning, releases |
| [12 API and integrations](12-api.md) | tokens and scopes, OpenAPI, endpoints, history and CSV, the Home Assistant recipe |

Also here: [RELEASE-GATE.md](RELEASE-GATE.md) (the manual test before a release),
[screenshots/](screenshots/README.md) (the image set and how to regenerate it),
`openapi/` (the Home Assistant YAML files the API page embeds). For contributors:
`AUDIT.md`, `DESIGN-AUDIT.md`, [REVIEW-TAGS.md](REVIEW-TAGS.md) and
[design/](design/REDESIGN-CONCEPT.md).
