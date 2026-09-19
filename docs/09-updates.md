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
postinst restarts a running daemon and says so —

```
n5-fangov: daemon is running (0.3.1), restarting it so the new binary takes over
```

(the bracket names the version being replaced; `installer deployment` when the package
lands over an `install.sh` install). `ExecStopPost=failsafe` puts the fans into the safe
state between the old and the new process, `check --quiet` gates the new start. The two
other closing lines of the postinst, for a box without a running daemon, are on the
[install page](01-install.md#the-debian-package).

## What an upgrade can affect

A Proxmox upgrade **can** affect the kernel — the DKMS driver has to be rebuilt, which
the [kernel-update gate](02-kernel-driver.md#the-kernel-update-gate) watches —, `dkms`
itself, and perl/`PVE::Notify` for alerts. What it **cannot**: the config, presets, TLS
certificate and logs live under `/etc/n5-fangov` and `/var/log/n5-fangov` and are never
touched by package scripts (purge removes them). The state directory
`/var/lib/n5-fangov` is removed with the package (`apt remove` and `uninstall.sh`
alike): sessions, the alert history and the chart history in it are disposable —
**`tokens.json` is not**. It holds the hashes of the [API tokens](08-https-security.md#api-tokens)
and is the only place they exist; the settings bundle never carries tokens. After a
remove/reinstall every script and Home Assistant needs a new token, or copy
`tokens.json` (0600) aside before and back afterwards. A package *upgrade* keeps the
directory.

## Rollback

Put the old binary back and restart:

```
install -m0755 /root/n5-fangov-<version>.bak /usr/bin/n5-fangov && systemctl restart n5-fangov
```

The config is forward-compatible: a newer daemon reads an older file; an older daemon
warns about unknown keys and ignores them (rule: config errors never prevent a start).
Going back to a release that predates them: `hysteresis`, `min_on`, `[[schedule]]` and the `[alert]`
webhook keys become warnings; a `sensor` **array** is not a string for the old parser —
run `n5-fangov check` after the rollback and keep the single-id form on channels you
may roll back with; `tokens.json` and `history.json` are left alone but unused (no
Bearer support, empty charts). Sessions are dropped when the credential epoch changes,
nothing else is versioned. If `setup` replaced the config, the previous one is
`config.toml.bak-<timestamp>` next to it.

## Backup and restore

```
n5-fangov export settings.json       # {"format":1, config: <toml>, presets: {name: <toml>}}
n5-fangov import settings.json       # validates everything, then writes and reloads
```

The export is the config file text (comments included — so also `[[schedule]]` and the
`[alert]` webhook keys) plus every user preset (the built-in ones travel with the
binary), with the password hash redacted to `<unchanged>`. The webhook URL travels in
full (a receiver key in its query or path included — treat the file accordingly). It
never carries API tokens, sessions or the chart history.
`import` refuses the whole bundle when any part fails to parse — nothing is written in
that case. `<unchanged>` is resolved from the password stored on the importing machine;
on a fresh box run `n5-fangov passwd` first (or put a real hash into the bundle). Presets
that exist locally but not in the bundle stay. A running daemon reloads curves and
`[daemon]` values; a changed channel set or profile, or any `[web]`/`[log]` change, needs
`systemctl restart n5-fangov` (the command says so). The API offers the same
(`GET /api/config/export`, `POST /api/config/import`, auth + CSRF), and Settings → *Backup*
has both as buttons ([Backup](04-dashboard.md#backup)).

## Uninstall

`deploy/uninstall.sh` runs from the repository checkout — the same directory the
install ran from:

```
./deploy/uninstall.sh            # stops and removes the unit, binary, apt hook, PVE template, state (tokens, history)
./deploy/uninstall.sh --purge    # additionally /etc/n5-fangov (config, presets, certificate) and /var/log/n5-fangov
```

When the **package** is installed (`dpkg -s n5-fangov` says so), use `apt remove
n5-fangov` / `apt purge n5-fangov` instead — since 0.3.1 `uninstall.sh` refuses on such
a host (it would remove the files while dpkg still lists the package as installed). `ExecStopPost=failsafe`
leaves the fans in the profile's safe state (N5 Pro: CPU/SSD back to EC automatic, HDD at
the fixed stop duty until the next boot). The kernel module is not touched — it belongs
to the DKMS package, see [Kernel driver: Remove](02-kernel-driver.md#remove).

What `./deploy/uninstall.sh --purge` prints on the N5 Pro:

```
--- stop (ExecStopPost runs the failsafe) ---
--- explicit failsafe with the config still present ---
failsafe: cpu: pwm1 stop=auto ok
failsafe: ssd: pwm2 stop=auto ok
failsafe: hdd: pwm3 stop=140 ok
failsafe: safe state set on n5pro
--- files ---
  --purge: /etc/n5-fangov and /var/log/n5-fangov removed

Done. Fans are in the configured safe state; on the N5 Pro the HDD channel
returns to full EC control only after a cold boot.
```

Verify: `systemctl status n5-fangov` → `Unit n5-fangov.service could not be found.`;
after `--purge` also `ls /etc/n5-fangov` → `No such file or directory`.

Next: [Kernel driver](02-kernel-driver.md) · [Troubleshooting](10-troubleshooting.md) ·
[Install](01-install.md)
