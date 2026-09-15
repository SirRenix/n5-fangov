# Deploying n5-fangov

Two ways: the `.deb` (`make deb`) or the checkout scripts (`deploy/install.sh`).
Both install the same files:

| Path | Purpose |
|---|---|
| `/usr/bin/n5-fangov` | daemon + CLI (single static binary) |
| `/etc/systemd/system/n5-fangov.service` (deb: `/lib/systemd/system/`) | Type=notify, watchdog 60 s, `ExecStartPre=check --quiet`, `ExecStopPost=failsafe`, sandboxed (see README "Hardening") |
| `n5-fangov-onfailure.service` + `/usr/libexec/n5-fangov/n5-fangov-onfailure` | alert with real cause, 30 min cooldown per type |
| `/etc/n5-fangov/config.toml` | **written by `n5-fangov setup`**, never by the installer; an existing file is left alone |
| `/etc/n5-fangov/presets/`, `/etc/n5-fangov/tls/` | presets; auto TLS certificate (created at the first HTTPS start) |
| `/var/log/n5-fangov/` (0750) | rotating log file `n5-fangov.log`, `.1`..`.N`; the journal is unchanged |
| `/etc/apt/apt.conf.d/90n5-fangov` | `DPkg::Post-Invoke` → `n5-fangov check --after-update` (kernel gate, see below) |
| `/usr/share/doc/n5-fangov/config.example.toml` | reference with every key explained |
| `/usr/share/n5-fangov/pve-notification/*.hbs` | copied to `/etc/pve/notification-templates/default/` when `/etc/pve` exists |

## First start

```
n5-fangov setup           # detect profile, write the config, choose local/lan, set the password
n5-fangov check           # what serve will do with this config: pwm writable, sensors, tls, log, dkms
systemctl enable --now n5-fangov
n5-fangov status
```

`install.sh` and the deb postinst **stop and disable `n5-fand.service`** when it
is present. Two regulators must never write the same channels; the unit also
carries `Conflicts=n5-fand.service`.

## Runtime

- `/run/n5-fangov/n5-fangov.sock` (CLI, no auth), `/run/n5-fangov/state.json`
  (fallback for `n5-fangov status` when the socket is down), `alert.<type>` stamps.
- Stop: every channel goes to its configured `stop` (N5 Pro: cpu/ssd `auto`,
  hdd fixed duty). `ExecStopPost` repeats this after crashes and kills.
- Failed unit: `n5-fangov-onfailure` reads `Result`/`ExecMainStatus`, waits 8 s,
  reports `restart` (daemon back) or `failed` (down) via `n5-fangov alert`.

## Kernel updates (N5 Pro)

The EC driver `minisforum_n5_it5571` comes from DKMS. Two gates:

1. `n5-fangov check --after-update` runs from the apt hook after every dpkg run
   and requires `updates/dkms/minisforum_n5_it5571.ko` for **every** kernel under
   `/lib/modules`. A missing one prints
   `kernel X: fan driver module missing — run: dkms install minisforum-n5-it5571/<ver> -k X`
   and sends a `kernel` alert (30 min cooldown). The apt run itself never fails.
2. `n5-fangov check` (ExecStartPre) reports `dkms` for the running kernel; a
   missing module makes the start fail loudly and the onfailure alert names it.

## Remove

`deploy/uninstall.sh` or `apt remove n5-fangov`: stop, failsafe, delete files and
the apt hook. `/etc/n5-fangov` and `/var/log/n5-fangov` stay (`--purge` /
`apt purge` removes them).
