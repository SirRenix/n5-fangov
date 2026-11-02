# CLI reference

`n5-fangov <subcommand> [flags]`. Every subcommand that involves a config accepts
`--config PATH` (default `/etc/n5-fangov/config.toml`). Commands marked *socket* talk to
the running daemon and fall back to the files or `state.json` when it is down
([The socket](#the-socket)).

## Subcommands

| Subcommand | Flags | Does |
|---|---|---|
| `setup` | `--listen local\|lan\|HOST:PORT`, `--user U`, `--password-file F` \| `--password -`, `--profile auto\|n5pro\|nct67xx\|it87xx\|monitor`, `--yes`, `--fresh` | write the config for this machine; interactive when flags are missing, `--yes` asks nothing and needs every flag; an existing file is backed up and everything setup does not ask for is kept (`--fresh`: nothing) — [Setup](03-setup.md#running-setup-again) |
| `serve` | `--dry-run`, `--run-dir DIR`, `--state-dir DIR`, `--listen ADDR` (`none` = no TCP listener) | run the daemon (the unit's `ExecStart`); `--dry-run` reads sensors and logs decisions without writing pwm |
| `status` | — | channels, temperatures, duty, RPM, mode (*socket*, else `state.json`) |
| `set <ch> <duty\|NN%>` | — | manual override for one channel (*socket*; limits and stall guard still apply) |
| `auto <ch\|all>` | — | back to the curve (*socket*) |
| `curve` | — | print the configured curves |
| `log` | `-n N` (default 50), `--export FILE` (`-` = stdout), `--clear` | log file; journal when no file is configured (`--clear` then impossible) — [Logs](10-troubleshooting.md#logs) |
| `check` | `--quiet` (failures only), `--after-update`, `--unit-start` | self-check (`ExecStartPre`; with `N5FANGOV_UNIT_START=1`, which the unit sets, or `--unit-start` it sends the `driver`/`profile` alert itself): config, profile (fails on a read-only or missing N5 driver on a board the driver writes on; N5 Air and N5 PRO/F8NAB run as `monitor` under `auto`), hardware status, driver version and parameters, pwm writable, sensors, emergency hook, tls, log, dkms; `--after-update` is the apt hook's [kernel gate](02-kernel-driver.md#the-kernel-update-gate) |
| `detect` | — | list profiles with their detection result (read-only) |
| `identify` | `--json` | machine, BIOS, EC firmware, kernel, driver version/parameters/mode, register dump, hardware status (read-only) — [below](#identify) |
| `system` | `--json` | hardware inventory (*socket*, else local collection) — [System](04-dashboard.md#system) |
| `test <ch>` | `--force`, `--hold D`, `--sample D` (fixtures only) | channel verification run: writes duty steps and samples the tach; refuses while the daemon runs unless `--force`, and always while another writer holds the [writer lock](#the-writer-lock); restores the channel's stop value |
| `validate <pwmN>` | `--tests`, `--boot cold\|warm`, `--out DIR`, `--restore pwmN=auto\|D`, `--i-am-at-the-console`, `--dry-run`, `--replay LOG I2EC` | TESTSPEC run with result bundle — [below](#validate) |
| `failsafe` | — | put every configured channel into its safe state (`ExecStopPost`; works without the daemon) |
| `passwd` | `--user U`, `--password-file F` \| `--password -` | set the web user/password (`auth = basic`), restart to apply |
| `cert` | `info` \| `export [--der] [FILE]` \| `regen [--new-key]` \| `upload CERT KEY` \| `reset` | dashboard certificate (*socket* = hot swap, else on the files plus a restart hint) — [CLI equivalents](08-https-security.md#cli-equivalents) |
| `alerts` | `status` \| `test` \| `template` \| `ack ID...` \| `ack --all` | alert transport (`status` shows the webhook URL with its query redacted, the recent alerts with id and acknowledgement), test alert, PVE notification template, acknowledge alerts (*socket*, else on the files) — [From the shell](07-alerts.md#from-the-shell) |
| `token` | `create NAME [--scope read\|control\|admin] [--ttl DAYS]` \| `list` \| `revoke ID` | API tokens (*socket* only: the daemon owns `tokens.json`) — [below](#api-tokens) |
| `export [FILE]` | `--presets DIR` | settings bundle (config + presets, hash redacted) as JSON; `-`/no file = stdout — [Backup and restore](09-updates.md#backup-and-restore) |
| `import FILE` | `--presets DIR` | restore a bundle (everything validated first), reload the daemon |
| `version` | (also `-v`, `--version`) | print the version |
| `help` | (also `-h`, `--help`) | usage |

## identify

```
n5-fangov identify            # summary
n5-fangov identify --json     # for an issue or a validation report
```

Reads DMI (product, board, board version, vendor, BIOS and date, EC firmware from
`/sys/class/dmi/id/ec_firmware_release`), kernel, kernel taint, boot id, uptime, the
driver's `/sys/module` entry (version, srcversion, taint, `force`, `experimental_write`,
`debug_regs`), the hwmon attributes it exposes, the register dump when `debug_regs=1`,
the detected profile and the [hardware status](02-kernel-driver.md#hardware-status-and-validation)
with the source of the built-in table. It writes no EC data and no sysfs attribute; with `debug_regs=1` reading the register dump sets the Super-I/O index and I2EC address registers (driver patch 0003).

## validate

> **n5-ectest.** The test toolset of the driver repository
> ([`ectest/`](https://github.com/SirRenix/minisforum-n5pro-fan-proxmox/tree/main/ectest),
> `n5-ectest map|handback|snapshot|log|report`) is the tool of record of TESTSPEC 0.4
> (§1 rule 4). `validate` is the second implementation for the D4 cross-check (§9, #27);
> every bundle says so in `meta.json` → `result_role`.

Runs the N5 Pro test specification (`docs/TESTSPEC.md` of the driver repository,
version 0.4) on one channel and writes a result bundle. Writing tests need driver
`0.2.1+sr1` loaded with `debug_regs=1`: the verdicts are computed from the EC duty
registers, not from the driver's pwm cache. Without them `validate` writes nothing and
reports T1–T3 as `INCONCLUSIVE`. TESTSPEC 0.4 is satisfiable on a host in productive
operation: guests keep running.

```
systemctl stop n5-fangov
modprobe -r minisforum_n5_it5571; modprobe minisforum_n5_it5571 debug_regs=1
n5-fangov validate pwm3 --boot warm --out /root/runs
modprobe -r minisforum_n5_it5571; modprobe minisforum_n5_it5571
systemctl start n5-fangov
```

| Test | What `validate` does |
|---|---|
| T0 | identity fields of TESTSPEC §2 (incl. `sys_vendor`); `--boot cold\|warm` is required for PASS |
| T6 | boot-state snapshot before any write of this run (reported, no verdict) |
| T1 | mapping: 255 → 217 → 179 → 140, 60 s each, after 60 s at rest. Only samples due 10 s (pwm1–pwm3) or 1 s (pwm4) after the write are judged; the earlier ones are listed in `verdict.json` → `not_judged`. pwm3: DCR3 and DCR4 must both equal the written value. Another tach that follows is `FAIL` only when no table input of another channel (EC 0x09 CPU, EC 0x05 SSD, EC 0x06 HDD) crossed a step by ≥ 2 °C; with a crossing it is `INCONCLUSIVE` ("table input of pwmN crossed a step") |
| T2/T3 | hand-back and settling: auto value A, then sub-runs H = 255 and M = min(A + 48, 200), each held 60 s, auto, 17 min logged (15 min plus the 120-s T3 window); 3 runs. A sub-run whose value lies within 16 counts of A is `INCONCLUSIVE`; so is every sub-run whose table input crossed a step by ≥ 2 °C between the write and the end of its log, and every sub-run of the run when it crossed in the window of A. Hold-phase samples after the settle time that differ from the written value are listed (`hold_deviations`), not judged. pwm3: evaluated on DCR3 and DCR4 (A per register, M from DCR3); different verdicts → `INCONCLUSIVE` ("DCR3/DCR4 disagree"). pwm4: `N/A`, nothing written (no EC automatic on DCR5) |
| C1 | `INCONCLUSIVE`: C1 is decided with the module unloaded (`f8a-snapshot.sh` before and after, TESTSPEC §5.3); the driver's `ecram` file is no C1 path before the byte-compare against f8a |
| T4/T5 | `INCONCLUSIVE`: evaluated by `n5-ectest report` from `samples.csv` (TESTSPEC §5.5); pwm3 "HDD input EC 0x06 below 34 C" when EC 0x06 stayed below the table step in the run; pwm4 `N/A` |

**Refusals** (before anything is written, no bundle): the `--out` directory cannot be
created or written; the daemon runs or is (re)starting (no override); another writer
holds the [writer lock](#the-writer-lock); an earlier run left a channel unrestored
(`/run/n5-fangov/validate-restore.json` of `validate`, or an open
`/run/n5-ectest/restore.json` of n5-ectest; also when the file cannot be read); for a
writing test: standard input is not a terminal (piped input, also with the flag), the
session is remote or not a local console without `--i-am-at-the-console`, or
`VALIDATE START` is not typed. Remote means `SSH_*` variables or an `sshd`,
`sshd-session`, `termproxy` (PVE web console), `dropbear`, `mosh-server`, `ttyd` …
process among the ancestors (sudo and `su -` clear the variables, not the process chain).
Local console means a VT, serial or system console as controlling terminal somewhere in
the chain (login on tty1 or the KVM, also through `sudo`). The flag is your statement
that you sit at the device; the bundle records the session.

`validate-restore.json` is written before the first write and removed when every channel
is back at its restore value. If it is still there (the run was killed with SIGKILL, the
power went, a restore failed), check the channels (`systemctl start n5-fangov`, or
`n5-fangov failsafe`) and delete the file.

**Gates** G1–G7 (TESTSPEC §3) are measured and logged:

| Gate | `validate` |
|---|---|
| G1 | no other pwm writer: systemd state of `n5-fangov`, `n5-fand`, `fancontrol`; processes by `comm`/`argv[0]` (`n5-fangov` only as `serve`); open file descriptors on the driver's `pwm*` files; the `pwmN`/`pwmN_enable` cache against what this run left there; the writer-lock inode. Checked again at every sample; a hit aborts the run |
| G2 | no scrub, resilver, `vzdump` or `proxmox-backup-client` (also as PVE task) |
| G3 | load average (1 min) < 1.0 in the first sample of the gate window |
| G4 | over the 5-min gate window EC 0x04 (system), EC 0x06 (ambient) and HDD each change < 1 °C, and every limit sensor (Tctl, NVMe, HDD) has a value. Tctl is no stability criterion (a limit only); its span is reported |
| G5 | the typed confirmation, see above |
| G6 | duty registers in the first sample; with driver `0.2.1+sr2` (`ecram`) also the curve tables 0x10–0x27, 0x30–0x38, 0x70–0x81 (`meta.json` → `ec_ram_tables`) → `PASS`; without `ecram` `PARTIAL` (the tables then come from an `f8a-snapshot.sh` mode-A snapshot after boot, named in the run record). Not blocking |
| G7 | board in the driver's write table (N5 PRO/F8NAA, N5/F8NAA) with the measured `sys_vendor`, module loaded and not with `force` |

A run without a writing test checks G1, G2, G6; G3, G4, G5, G7 are `SKIPPED`. G3/G4 are
also `SKIPPED` without readable duty registers and after a failed G7. A failed gate
(except G6) reports the writing tests `INCONCLUSIVE` ("not run: gate …"), nothing is
written.

**Sampling**: every 5 s on a grid from the start of each phase (the write of a T1 step,
the auto command of T2/T3). A sample read within ±0.5 s of its due time is judged at
that time; `samples.csv` → `t_s` is the due time, `t_iso` the real read time. A sample
further off, and a slot skipped because the previous read overran it, is a gap: recorded
in `events.log`, not judged, counted in `meta.json` → `grid.gaps`.

**Limits** abort the run: CPU Tctl 85 °C, NVMe 70 °C, HDD 50 °C, a tach at 0 rpm, a limit
sensor unreadable in 3 samples in a row, another writer (G1). After a temperature abort
in a run that wrote, the channel of that sensor (Tctl → pwm1, NVMe → pwm2, HDD → pwm3)
is set to 255 and left there; `restore_values` says so. A duty below 100 is never
written, also not when restoring: a configured stop duty below 100 is restored as 100
(the daemon sets its own values when it runs again). Every channel the run touched goes
back to its restore value at the end, on abort, on a signal and after a panic, before
anything else is printed; a closed output pipe (`| tee` killed by the same Ctrl-C) does
not stop that. The restore value is the daemon's stop value of the channel (`--restore
pwmN=auto|D`, D ≥ 100, overrides it); a fixed stop value is not the EC automatic,
TESTSPEC §4 counts it as equally safe. A fixed restore value is read back from the duty
register until it matches or the settle time (10 s, pwm4 1 s) has passed; a difference
after that is listed in `restore_warnings`. pwm4 goes back to its configured stop value
too; with `auto` (the default) that is `pwm4_enable = 2`, which driver `0.2.1+sr1~rc2` and
later turn into 255 (DCR5 has no EC automatic); with an older driver DCR5 keeps the last
test duty. `test` restores the same way. One channel per run; the gate window and T1
take about 10 minutes, T2/T3 about two hours.

`--out` (default: the current directory) is created with its parents and checked for
writability before any gate; when that fails, `validate` stops there with exit 1 and has
written nothing. The bundle `<out>/<YYYYMMDD-HHMM>-<board>-<bios>-validate-pwmN/` holds
`meta.json` (with `restore_values`, `restore_failed`, `restore_warnings`,
`limit_sensors`, `session`, `settle_time_s`, `grid`, `redaction`, `result_role` and the
`spec_deviations` of this tool, empty under 0.4), `samples.csv`, `events.log`,
`verdict.json` and `SHA256SUMS` (TESTSPEC §8). Every file is redacted before it is
written and hashed: host name → `n5host`, DMI serials and UUID, MAC, IPv4/IPv6 and mail
addresses, user names, the values of `SSH_*`; the rule set is in `meta.json` →
`redaction`. A failed restore is printed on stdout and stderr. Exit code 0 when the run
completed, whatever the verdicts; 1 when it was refused or aborted, when a gate or
missing duty registers kept the writing tests from running, or when a channel could not
be restored.

| Flag | Use |
|---|---|
| `--tests T0,T1,…` | subset (default all) |
| `--restore pwmN=auto\|D,…` | restore values instead of the configured stop values (D ≥ 100) |
| `--dry-run [pwmN]` | the whole procedure against a built-in EC model on a virtual clock; nothing is written; `--hold`, `--sample`, `--observe`, `--gate-window`, `--repeat` only here |
| `--replay PWMTEST I2EC` | T2/T3 from raw logs of the pre-TESTSPEC script (`06-pwmtest-ch*.txt`, `06-i2ec-dcr-*.txt`); prints `verdict.json` |

## The writer lock

`serve`, `test` and `validate` hold an exclusive lock on `/run/n5-fangov/writer.lock`
while they write pwm (`serve` for its lifetime). A second writer refuses to start and
names the holder. `serve` refuses to start only when `validate` or `test` holds the
lock; any other lock error (run directory not writable) lets it start without the lock,
with a journal warning and the alert `start` (cooling before the lock). `failsafe` (the unit's `ExecStopPost`) writes nothing while
`validate` or `test` holds it, because that run restores its own channels. The kernel
drops the lock when the process ends, also on `kill -9`. `serve --dry-run` and
`validate --dry-run` take no lock.

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
verified; `validate`: refused, aborted, writing tests not run, or a channel not restored; `token`: daemon not running, name taken,
unknown id). `2` usage error.

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
