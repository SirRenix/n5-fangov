# Deploying pvefand

Two ways: the `.deb` (`make deb`) or the checkout scripts (`deploy/install.sh`).
Both install the same files:

| Path | Purpose |
|---|---|
| `/usr/bin/pvefand` | daemon + CLI (single static binary) |
| `/etc/systemd/system/pvefand.service` (deb: `/lib/systemd/system/`) | Type=notify, watchdog 60 s, `ExecStartPre=check --quiet`, `ExecStopPost=failsafe` |
| `pvefand-onfailure.service` + `/usr/libexec/pvefand/pvefand-onfailure` | alert with real cause, 30 min cooldown per type |
| `/etc/pvefand/config.toml` | from `deploy/config.example.toml` if absent; never overwritten |
| `/usr/share/pvefand/pve-notification/*.hbs` | copied to `/etc/pve/notification-templates/default/` when `/etc/pve` exists |

## First start

```
pvefand detect          # read-only: profiles, channels, sensors
pvefand check           # config, pwm writable, sensors, dkms (n5pro)
systemctl start pvefand
pvefand status
```

`install.sh` and the deb postinst **stop and disable `n5-fand.service`** when it
is present. Two regulators must never write the same channels; the unit also
carries `Conflicts=n5-fand.service`.

## Runtime

- `/run/pvefand/pvefand.sock` (CLI, no auth), `/run/pvefand/state.json`
  (fallback for `pvefand status` when the socket is down), `alert.<type>` stamps.
- Stop: every channel goes to its configured `stop` (N5 Pro: cpu/ssd `auto`,
  hdd fixed duty). `ExecStopPost` repeats this after crashes and kills.
- Failed unit: `pvefand-onfailure` reads `Result`/`ExecMainStatus`, waits 8 s,
  reports `restart` (daemon back) or `failed` (down) via `pvefand alert`.

## Kernel updates (N5 Pro)

The EC driver `minisforum_n5_it5571` comes from DKMS. `pvefand check` reports
`dkms` as advisory; a missing module makes `ExecStartPre` fail loudly and the
onfailure alert names the DKMS state.

## Remove

`deploy/uninstall.sh` or `apt remove pvefand`: stop, failsafe, delete files.
`/etc/pvefand` stays (`apt purge` removes it).
