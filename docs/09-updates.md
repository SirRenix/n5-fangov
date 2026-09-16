# Updates, rollback, backup, uninstall

What this page covers: updating n5-fangov by release or package, what a Proxmox
upgrade touches, going back to a previous version, the settings bundle for backup and
restore, and removing n5-fangov.

- [Update by release](#update-by-release)
- [Package update](#package-update)
- [What an upgrade can affect](#what-an-upgrade-can-affect)
- [Rollback](#rollback)
- [Backup and restore](#backup-and-restore)
- [Uninstall](#uninstall)

## Update by release

Keep a copy of the running binary first (for the [rollback](#rollback)):

```
cp /usr/bin/n5-fangov /root/n5-fangov-$(n5-fangov version | cut -d' ' -f2).bak
```

Then the same steps as the [release install](01-install.md#from-a-github-release) with
the new tag: `install.sh` replaces binary and units and keeps the config; restart the
daemon afterwards (`systemctl restart n5-fangov` — `ExecStopPost=failsafe` covers the
fans in between, `check --quiet` gates the new start).

## Package update

`apt upgrade` of n5-fangov (or `apt install ./n5-fangov_<version>_amd64.deb`): the
postinst restarts a running daemon; `ExecStopPost=failsafe` puts the fans into the safe
state between the old and the new process, `check --quiet` gates the new start.

## What an upgrade can affect

A Proxmox upgrade **can** affect the kernel — the DKMS driver has to be rebuilt, which
the [kernel-update gate](02-kernel-driver.md#the-kernel-update-gate) watches —, `dkms`
itself, and perl/`PVE::Notify` for alerts. What it **cannot**: the config, presets, TLS
certificate and logs live under `/etc/n5-fangov` and `/var/log/n5-fangov` and are never
touched by package scripts (purge removes them). The state directory
`/var/lib/n5-fangov` (sessions, alert history) is removed with the package; nothing in
it is worth keeping.

## Rollback

Put the old binary back and restart:

```
install -m0755 /root/n5-fangov-<version>.bak /usr/bin/n5-fangov && systemctl restart n5-fangov
```

The config is forward-compatible: a newer daemon reads an older file; an older daemon
warns about unknown keys and ignores them (rule: config errors never prevent a start).
Sessions are dropped when the credential epoch changes, nothing else is versioned. If
`setup` replaced the config, the previous one is `config.toml.bak-<timestamp>` next to
it.

## Backup and restore

```
n5-fangov export settings.json       # {"format":1, config: <toml>, presets: {name: <toml>}}
n5-fangov import settings.json       # validates everything, then writes and reloads
```

The export is the config file text (comments included) plus every user preset (the
built-in ones travel with the binary), with the password hash redacted to `<unchanged>`.
`import` refuses the whole bundle when any part fails to parse — nothing is written in
that case. `<unchanged>` is resolved from the password stored on the importing machine;
on a fresh box run `n5-fangov passwd` first (or put a real hash into the bundle). Presets
that exist locally but not in the bundle stay. A running daemon reloads curves and
`[daemon]` values; a changed channel set or profile, or any `[web]`/`[log]` change, needs
`systemctl restart n5-fangov` (the command says so). The API offers the same
(`GET /api/config/export`, `POST /api/config/import`, auth + CSRF), and the settings gear
has both as buttons ([Settings gear](04-dashboard.md#settings-gear)).

## Uninstall

```
./deploy/uninstall.sh            # stops and removes the unit, binary, apt hook, PVE template, state
./deploy/uninstall.sh --purge    # additionally /etc/n5-fangov (config, presets, certificate) and /var/log/n5-fangov
```

or `apt remove n5-fangov` / `apt purge n5-fangov` for the package. `ExecStopPost=failsafe`
leaves the fans in the profile's safe state (N5 Pro: CPU/SSD back to EC automatic, HDD at
the fixed stop duty until the next boot). The kernel module is not touched — it belongs
to the DKMS package, see [Kernel driver: Remove](02-kernel-driver.md#remove).

Next: [Kernel driver](02-kernel-driver.md) · [Troubleshooting](10-troubleshooting.md) ·
[Install](01-install.md)
