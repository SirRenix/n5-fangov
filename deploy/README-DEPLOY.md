# deploy/

The files in this directory are what `install.sh` and the `.deb` put on the box: the
systemd units, the onfailure helper, the apt hook, the config example and the PVE
notification templates.

The operator documentation lives in `docs/`:

- [Install](../docs/01-install.md) — the two install paths, what the installer does,
  the file layout on the target.
- [Kernel driver](../docs/02-kernel-driver.md) — the DKMS module and the kernel-update
  gate the apt hook implements.
- [Updates, rollback, backup, uninstall](../docs/09-updates.md).
- [Hardening](../docs/08-https-security.md#hardening) — the sandbox directives of
  `n5-fangov.service` (`ReadWritePaths` covers `/sys/class/hwmon` and
  `/sys/devices/platform`, the rest of `/sys` is read-only), and why
  `ProtectKernelTunables` must stay `no`.
- [File layout](../docs/01-install.md#file-layout) — what the daemon writes at runtime:
  `/var/lib/n5-fangov/` (`sessions.json`, `tokens.json`, `alerts.json`,
  `history.json`) is removed by `uninstall.sh` and the package; `--purge` also removes
  `/etc/n5-fangov` and the logs.

`make verify-deploy` checks the units and the hook ([Development](../docs/11-development.md#build)).
