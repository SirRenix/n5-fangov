# CLI reference

What this page covers: every subcommand of `n5-fangov` with its flags, the exit codes,
the environment variables and how the CLI talks to the running daemon.

- [Subcommands](#subcommands)
- [Exit codes](#exit-codes)
- [Environment](#environment)
- [The socket](#the-socket)

## Subcommands

`n5-fangov <subcommand> [flags]`. Every subcommand accepts `--config PATH` (default
`/etc/n5-fangov/config.toml`) where a config is involved. Commands marked *socket* talk
to the running daemon and fall back to the files or `state.json` when it is down.

| Subcommand | Flags | Does |
|---|---|---|
| `setup` | `--listen local\|lan\|HOST:PORT`, `--user U`, `--password-file F` \| `--password -`, `--profile auto\|n5pro\|nct67xx\|it87xx\|monitor`, `--yes` | write the config for this machine (interactive when flags are missing; `--yes` asks nothing and needs every flag; existing file backed up) — [Setup](03-setup.md) |
| `serve` | `--dry-run`, `--run-dir DIR`, `--state-dir DIR`, `--listen ADDR` (`none` = no TCP listener) | run the daemon (the unit's `ExecStart`); `--dry-run` reads sensors and logs decisions without writing pwm |
| `status` | — | channels, temperatures, duty, RPM, mode (*socket*, else `state.json`) |
| `set <ch> <duty\|NN%>` | — | manual override for one channel (*socket*; limits and stall guard still apply) |
| `auto <ch\|all>` | — | back to the curve (*socket*) |
| `curve` | — | print the configured curves |
| `log` | `-n N` (default 50), `--export FILE` (`-` = stdout), `--clear` | log file; journal when no file is configured (`--clear` then impossible) — [Logs](10-troubleshooting.md#logs) |
| `check` | `--quiet` (failures only), `--after-update` | self-check (`ExecStartPre`): config, profile, pwm writable, sensors, tls, log, dkms; `--after-update` is the apt hook's [kernel gate](02-kernel-driver.md#the-kernel-update-gate) |
| `detect` | — | list profiles with their detection result (read-only) |
| `system` | `--json` | hardware inventory (*socket*, else local collection) — [System](04-dashboard.md#system) |
| `test <ch>` | `--force`, `--hold D`, `--sample D` (fixtures only) | channel verification run: writes duty steps and samples the tach; refuses while the daemon is running unless `--force`; restores the channel's stop value |
| `failsafe` | — | put every configured channel into its safe state (`ExecStopPost`; works without the daemon) |
| `passwd` | `--user U`, `--password-file F` \| `--password -` | set the web user/password (`auth = basic`), restart to apply; the dashboard changes both live |
| `cert` | `info` \| `export [--der] [FILE]` \| `regen [--new-key]` \| `upload CERT KEY` \| `reset` | dashboard certificate (*socket* = hot swap, else on the files plus a restart hint) — [HTTPS](08-https-security.md#cli-equivalents) |
| `alerts` | `status` \| `test` \| `template` | alert transport, test alert, PVE notification template (*socket*, else on the files) — [Alerts](07-alerts.md#from-the-shell) |
| `export [FILE]` | `--presets DIR` | settings bundle (config + presets, hash redacted) as JSON; `-`/no file = stdout — [Backup and restore](09-updates.md#backup-and-restore) |
| `import FILE` | `--presets DIR` | restore a bundle (everything validated first), reload the daemon |
| `version` | (also `-v`, `--version`) | print the version |
| `help` | (also `-h`, `--help`) | usage |

## Exit codes

`0` ok, `1` failure (`check`: serve could not run with this config; `test`: channel not
verified), `2` usage error (unknown subcommand, bad flag).

## Environment

| Variable | Default | Holds |
|---|---|---|
| `N5FANGOV_RUN_DIR` | `/run/n5-fangov` | socket, `state.json`, override and alert stamps |
| `N5FANGOV_STATE_DIR` | `$STATE_DIRECTORY` from systemd, else `/var/lib/n5-fangov` | `sessions.json`, `alerts.json` |
| `N5FANGOV_SYSFS` | `/sys` | sysfs root; tests point it at `testdata/sysfs/n5pro` |

Relative paths are taken from the working directory — under the unit that is `/`.

## The socket

`/run/n5-fangov/n5-fangov.sock` is a unix socket without authentication in a directory
only root can enter (`RuntimeDirectoryMode=0750`). *socket* commands go through it when
the daemon runs — same code path as the dashboard, changes take effect at once — and
work on the files directly otherwise, printing the `systemctl restart n5-fangov` that
applies the change. `state.json` in the same directory is the fallback for `status`.
The web API needs credentials instead ([Security](08-https-security.md#what-auth-covers)).

Next: [Configuration](06-configuration.md) · [Alerts](07-alerts.md) ·
[Troubleshooting](10-troubleshooting.md)
