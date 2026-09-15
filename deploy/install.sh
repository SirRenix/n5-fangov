#!/usr/bin/env bash
# Installs pvefand from a checkout (binary in dist/pvefand or ./pvefand) on
# the local host. Idempotent. Run as root: ./deploy/install.sh
#
# Does NOT start the daemon: run `pvefand check` first, then
# `systemctl start pvefand`. A running n5-fand.service (the Bash predecessor)
# is stopped and disabled here, because two regulators must never write the
# same channels.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DEPLOY="$ROOT/deploy"

[[ $EUID -eq 0 ]] || { echo "run as root"; exit 1; }

BIN=""
for c in "$ROOT/dist/pvefand" "$ROOT/pvefand"; do
    [[ -x "$c" ]] && { BIN="$c"; break; }
done
[[ -n "$BIN" ]] || { echo "binary missing: build first (make build / tools/remote-go.ps1 -Fetch)"; exit 1; }
if ! "$BIN" version >/dev/null 2>&1; then
    echo "$BIN does not run here (wrong architecture?)"; exit 1
fi

echo "--- predecessor n5-fand ---"
if systemctl list-unit-files n5-fand.service >/dev/null 2>&1 \
   && systemctl list-unit-files n5-fand.service 2>/dev/null | grep -q '^n5-fand.service'; then
    if systemctl is-active --quiet n5-fand.service; then
        echo "  n5-fand.service is running: stopping it (its ExecStopPost sets the safe state)"
        systemctl stop n5-fand.service
    fi
    if systemctl is-enabled --quiet n5-fand.service 2>/dev/null; then
        echo "  n5-fand.service was enabled: disabling it. pvefand replaces it."
        systemctl disable n5-fand.service
    fi
    echo "  n5-fand files stay installed; remove with n5pro-ec/deploy/uninstall.sh when pvefand is proven."
else
    echo "  not installed, nothing to do"
fi

echo "--- files ---"
install -m 0755 "$BIN" /usr/bin/pvefand
install -d -m 0755 /usr/libexec/pvefand
install -m 0755 "$DEPLOY/pvefand-onfailure" /usr/libexec/pvefand/pvefand-onfailure
install -m 0644 "$DEPLOY/pvefand.service"           /etc/systemd/system/pvefand.service
install -m 0644 "$DEPLOY/pvefand-onfailure.service" /etc/systemd/system/pvefand-onfailure.service
install -d -m 0755 /etc/pvefand /etc/pvefand/presets
if [[ -f /etc/pvefand/config.toml ]]; then
    echo "  /etc/pvefand/config.toml exists, not overwritten (template: deploy/config.example.toml)"
else
    install -m 0644 "$DEPLOY/config.example.toml" /etc/pvefand/config.toml
    echo "  /etc/pvefand/config.toml created from the example (N5 Pro layout)"
fi
install -d -m 0755 /usr/share/pvefand/pve-notification
install -m 0644 "$DEPLOY"/pve-notification/*.hbs /usr/share/pvefand/pve-notification/
if [[ -d /etc/pve ]]; then
    # pmxcfs does not allow chmod -> cp instead of install
    mkdir -p /etc/pve/notification-templates/default
    cp "$DEPLOY/pve-notification/pvefand-subject.txt.hbs" /etc/pve/notification-templates/default/
    cp "$DEPLOY/pve-notification/pvefand-body.txt.hbs"    /etc/pve/notification-templates/default/
    echo "  PVE notification template installed (alerts -> Proxmox notifications, template 'pvefand')"
fi

echo "--- systemd ---"
systemctl daemon-reload
systemctl enable pvefand.service
echo "  enabled (not started)"

cat <<EOF

Installed $("$BIN" version). Next steps:
  pvefand detect                  # which profile/channels are seen (read-only)
  pvefand check                   # config, sysfs, sensors, dkms
  systemctl start pvefand && pvefand status
  pvefand log 30
Web UI: [web].listen in /etc/pvefand/config.toml (default 127.0.0.1:8010).
EOF
