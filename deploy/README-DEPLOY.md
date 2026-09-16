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
  `n5-fangov.service`, and why `ProtectKernelTunables` must stay `no`.

`make verify-deploy` checks the units and the hook ([Development](../docs/11-development.md#build)).
