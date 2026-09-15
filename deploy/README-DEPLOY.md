# Deploying n5-fangov

Two ways: the `.deb` (`make deb`) or the checkout scripts (`deploy/install.sh`).
Both install the same files:

| Path | Purpose |
|---|---|
| `/usr/bin/n5-fangov` | daemon + CLI (single static binary) |
| `/etc/systemd/system/n5-fangov.service` (deb: `/lib/systemd/system/`) | Type=notify, watchdog 60 s, `ExecStartPre=check --quiet`, `ExecStopPost=failsafe` |
| `n5-fangov-onfailure.service` + `/usr/libexec/n5-fangov/n5-fangov-onfailure` | alert with real cause, 30 min cooldown per type |
| `/etc/n5-fangov/config.toml` | from `deploy/config.example.toml` if absent; never overwritten |
| `/usr/share/n5-fangov/pve-notification/*.hbs` | copied to `/etc/pve/notification-templates/default/` when `/etc/pve` exists |

## First start

```
n5-fangov detect          # read-only: profiles, channels, sensors
n5-fangov check           # config, pwm writable, sensors, dkms (n5pro)
systemctl start n5-fangov
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

The EC driver `minisforum_n5_it5571` comes from DKMS. `n5-fangov check` reports
`dkms` as advisory; a missing module makes `ExecStartPre` fail loudly and the
onfailure alert names the DKMS state.

## Remove

`deploy/uninstall.sh` or `apt remove n5-fangov`: stop, failsafe, delete files.
`/etc/n5-fangov` stays (`apt purge` removes it).
