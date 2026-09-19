# Updates, rollback, backup, uninstall

## Update by release

Keep a copy of the running binary first, for the [rollback](#rollback):

```
cp /usr/bin/n5-fangov /root/n5-fangov-$(n5-fangov version | cut -d' ' -f2).bak
```

Then repeat the [release install](01-install.md#from-a-github-release) with the new
tag. `install.sh` replaces binary and units and keeps the config. Restart afterwards:
`systemctl restart n5-fangov`. `ExecStopPost=failsafe` covers the fans in between,
`check --quiet` gates the new start.

## Package update

`apt upgrade` of n5-fangov (or `apt install ./n5-fangov_<debver>_amd64.deb`): the
postinst restarts a running daemon and says so —

```
n5-fangov: daemon is running (<version>), restarting it so the new binary takes over
```

The bracket names the version being replaced; `installer deployment` when the package
lands over an `install.sh` install. `ExecStopPost=failsafe` puts the fans into the safe
state between the old and the new process, `check --quiet` gates the new start. The
postinst lines for a box without a running daemon: [The Debian package](01-install.md#the-debian-package).

## What an upgrade can affect

A Proxmox upgrade can affect the kernel (the DKMS driver has to be rebuilt, which the
[kernel-update gate](02-kernel-driver.md#the-kernel-update-gate) watches), `dkms`
itself, and perl/`PVE::Notify` for alerts. It cannot touch the config, presets, TLS
certificate and logs under `/etc/n5-fangov` and `/var/log/n5-fangov`; package scripts
never write there (purge removes them).

The state directory `/var/lib/n5-fangov` is removed with the package (`apt remove` and
`uninstall.sh` alike). Sessions, the alert history and the chart history in it are
disposable. **`tokens.json` is not**: it holds the hashes of the
[API tokens](08-https-security.md#api-tokens) and is the only place they exist; the
settings bundle never carries tokens. `apt remove` / `apt purge` (the package
`postrm`) and `uninstall.sh` copy it to `/var/backups/n5-fangov/tokens.json.<timestamp>`
(0600, root) before the directory goes and print one line naming the copy. After a
reinstall put it back as `/var/lib/n5-fangov/tokens.json` (0600) and restart;
otherwise every script and Home Assistant needs a new token. A package *upgrade* keeps
the directory.

## Rollback

Put the old binary back and restart:

```
install -m0755 /root/n5-fangov-<version>.bak /usr/bin/n5-fangov && systemctl restart n5-fangov
```

A newer daemon reads an older file; an older daemon warns about unknown keys and
ignores them. Going back to a release that predates a key makes it a warning
(`hysteresis`, `min_on`, `ceiling`, `[[schedule]]`, the `[alert]` webhook keys). A
`sensor` **array** is not a string for an older parser: run `n5-fangov check` after
the rollback and keep the single-id form on channels you may roll back with.
`tokens.json` and `history.json` are left alone. Sessions are dropped when the
credential epoch changes. If `setup` replaced the config, the previous one is
`config.toml.bak-<timestamp>` next to it.

## Backup and restore

```
n5-fangov export settings.json       # {"format":1, config: <toml>, presets: {name: <toml>}}
n5-fangov import settings.json       # validates everything, then writes and reloads
```

The export is the config file text (comments included) plus every user preset (the
built-in ones travel with the binary), with the password hash redacted to
`<unchanged>`. The webhook URL travels in full, a receiver key in its query included;
treat the file accordingly. It never carries API tokens, sessions or the chart history.

`import` refuses the whole bundle when any part fails to parse; nothing is written in
that case. `<unchanged>` is resolved from the password stored on the importing machine;
on a fresh box run `n5-fangov passwd` first, or put a real hash into the bundle.
Presets that exist locally but not in the bundle stay. A running daemon reloads curves
and `[daemon]` values; a changed channel set or profile, or any `[web]`/`[log]` change,
needs `systemctl restart n5-fangov` (the command says so). The API offers the same
(`GET /api/config/export`, `POST /api/config/import`), and Settings → *Backup* has both
as buttons ([Backup](04-dashboard.md#backup)).

## Uninstall

`deploy/uninstall.sh` runs from the repository checkout the install ran from:

```
./deploy/uninstall.sh            # stops and removes the unit, binary, apt hook, PVE template, state (tokens.json is backed up first)
./deploy/uninstall.sh --purge    # additionally /etc/n5-fangov (config, presets, certificate) and /var/log/n5-fangov
```

When the **package** is installed (`dpkg -s n5-fangov` says so), use `apt remove
n5-fangov` / `apt purge n5-fangov` instead; `uninstall.sh` refuses on such a host.
`ExecStopPost=failsafe` leaves the fans in the profile's safe state (N5 Pro: CPU/SSD
back to EC automatic, HDD at the fixed stop duty until the next cold boot). The kernel
module is not touched ([Kernel driver: Remove](02-kernel-driver.md#remove)).

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
