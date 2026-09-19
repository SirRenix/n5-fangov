# CLI reference

`n5-fangov <subcommand> [flags]`. Every subcommand that involves a config accepts
`--config PATH` (default `/etc/n5-fangov/config.toml`). Commands marked *socket* talk to
the running daemon and fall back to the files or `state.json` when it is down
([The socket](#the-socket)).

## Subcommands

| Subcommand | Flags | Does |
|---|---|---|
| `setup` | `--listen local\|lan\|HOST:PORT`, `--user U`, `--password-file F` \| `--password -`, `--profile auto\|n5pro\|nct67xx\|it87xx\|monitor`, `--yes` | write the config for this machine; interactive when flags are missing, `--yes` asks nothing and needs every flag; an existing file is backed up — [Setup](03-setup.md) |
| `serve` | `--dry-run`, `--run-dir DIR`, `--state-dir DIR`, `--listen ADDR` (`none` = no TCP listener) | run the daemon (the unit's `ExecStart`); `--dry-run` reads sensors and logs decisions without writing pwm |
| `status` | — | channels, temperatures, duty, RPM, mode (*socket*, else `state.json`) |
| `set <ch> <duty\|NN%>` | — | manual override for one channel (*socket*; limits and stall guard still apply) |
| `auto <ch\|all>` | — | back to the curve (*socket*) |
| `curve` | — | print the configured curves |
| `log` | `-n N` (default 50), `--export FILE` (`-` = stdout), `--clear` | log file; journal when no file is configured (`--clear` then impossible) — [Logs](10-troubleshooting.md#logs) |
| `check` | `--quiet` (failures only), `--after-update` | self-check (`ExecStartPre`): config, profile, pwm writable, sensors, emergency hook, tls, log, dkms; `--after-update` is the apt hook's [kernel gate](02-kernel-driver.md#the-kernel-update-gate) |
| `detect` | — | list profiles with their detection result (read-only) |
| `system` | `--json` | hardware inventory (*socket*, else local collection) — [System](04-dashboard.md#system) |
| `test <ch>` | `--force`, `--hold D`, `--sample D` (fixtures only) | channel verification run: writes duty steps and samples the tach; refuses while the daemon runs unless `--force`; restores the channel's stop value |
| `failsafe` | — | put every configured channel into its safe state (`ExecStopPost`; works without the daemon) |
| `passwd` | `--user U`, `--password-file F` \| `--password -` | set the web user/password (`auth = basic`), restart to apply |
| `cert` | `info` \| `export [--der] [FILE]` \| `regen [--new-key]` \| `upload CERT KEY` \| `reset` | dashboard certificate (*socket* = hot swap, else on the files plus a restart hint) — [CLI equivalents](08-https-security.md#cli-equivalents) |
| `alerts` | `status` \| `test` \| `template` | alert transport (`status` shows the webhook URL with its query redacted), test alert, PVE notification template (*socket*, else on the files) — [From the shell](07-alerts.md#from-the-shell) |
| `token` | `create NAME [--scope read\|control\|admin] [--ttl DAYS]` \| `list` \| `revoke ID` | API tokens (*socket* only: the daemon owns `tokens.json`) — [below](#api-tokens) |
| `export [FILE]` | `--presets DIR` | settings bundle (config + presets, hash redacted) as JSON; `-`/no file = stdout — [Backup and restore](09-updates.md#backup-and-restore) |
| `import FILE` | `--presets DIR` | restore a bundle (everything validated first), reload the daemon |
| `version` | (also `-v`, `--version`) | print the version |
| `help` | (also `-h`, `--help`) | usage |

## API tokens

```
n5-fangov token create ha-control --scope control --ttl 365 > /root/ha-token   # secret once, stdout only
n5-fangov token create monitor                                                 # scope read, 90 days
n5-fangov token create agent --scope admin --ttl 0                             # never expires (warning on stderr)
n5-fangov token list
n5-fangov token revoke 3f9a1c2e
```

`create` prints the secret (`n5t_…`) once on stdout and nothing else there, so you can
capture it into a file or a variable. Names are `[A-Za-z0-9][A-Za-z0-9 ._-]{0,31}` and
unique; `--scope` defaults to `read`, `--ttl` to 90 days, `0` = never. `list` prints id,
name, scope, created, expires, last used, last address; `revoke ID` takes the 8-hex id
from the list. Without a running daemon all three exit 1 with a hint. Tokens exist only
with `auth = "basic"`. Scopes, storage, limits and what a token can never do:
[API tokens](08-https-security.md#api-tokens). Using one: [API and integrations](12-api.md).

## Exit codes

`0` ok. `1` failure (`check`: serve could not run with this config; `test`: channel not
verified; `token`: daemon not running, name taken, unknown id). `2` usage error.

## Environment

| Variable | Default | Holds |
|---|---|---|
| `N5FANGOV_RUN_DIR` | `/run/n5-fangov` | socket, `state.json`, override and alert stamps |
| `N5FANGOV_STATE_DIR` | `$STATE_DIRECTORY` from systemd, else `/var/lib/n5-fangov` | `sessions.json`, `tokens.json`, `alerts.json`, `history.json` |
| `N5FANGOV_SYSFS` | `/sys` | sysfs root; tests point it at `testdata/sysfs/n5pro` |

Relative paths are taken from the working directory; under the unit that is `/`.

## The socket

`/run/n5-fangov/n5-fangov.sock` is a unix socket without authentication in a directory
only root can enter (`RuntimeDirectoryMode=0750`). *socket* commands go through it when
the daemon runs — the same code path as the dashboard, changes take effect at once —
and work on the files otherwise, printing the `systemctl restart n5-fangov` that applies
the change (`token` has no file fallback). `state.json` in the same directory is the
fallback for `status`. The web API needs credentials instead: a session, Basic auth or
an API token ([Authentication](12-api.md#authentication)).
