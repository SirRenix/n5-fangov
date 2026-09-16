#!/usr/bin/env bash
# Installs n5-fangov from a checkout (binary in dist/n5-fangov or ./n5-fangov) on
# the local host. Idempotent. Run as root: ./deploy/install.sh
#
# Does NOT write the config and does NOT start the daemon: `n5-fangov setup`
# writes /etc/n5-fangov/config.toml for this machine (an existing file is kept
# untouched here and backed up by setup). A running n5-fand.service (the Bash
# predecessor) is stopped and disabled, because two regulators must never
# write the same channels.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DEPLOY="$ROOT/deploy"
APT_HOOK=/etc/apt/apt.conf.d/90n5-fangov
LOG_DIR=/var/log/n5-fangov

[[ $EUID -eq 0 ]] || { echo "run as root"; exit 1; }

BIN=""
for c in "$ROOT/dist/n5-fangov" "$ROOT/n5-fangov"; do
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
        echo "  n5-fand.service was enabled: disabling it. n5-fangov replaces it."
        systemctl disable n5-fand.service
    fi
    echo "  n5-fand files stay installed; remove with n5pro-ec/deploy/uninstall.sh when n5-fangov is proven."
else
    echo "  not installed, nothing to do"
fi

echo "--- files ---"
install -m 0755 "$BIN" /usr/bin/n5-fangov
install -d -m 0755 /usr/libexec/n5-fangov
install -m 0755 "$DEPLOY/n5-fangov-onfailure" /usr/libexec/n5-fangov/n5-fangov-onfailure
install -m 0644 "$DEPLOY/n5-fangov.service"             /etc/systemd/system/n5-fangov.service
install -m 0644 "$DEPLOY/n5-fangov-onfailure.service"   /etc/systemd/system/n5-fangov-onfailure.service
install -d -m 0755 /etc/n5-fangov /etc/n5-fangov/presets
install -d -m 0750 "$LOG_DIR"
install -d -m 0755 /usr/share/doc/n5-fangov
install -m 0644 "$DEPLOY/config.example.toml" /usr/share/doc/n5-fangov/config.example.toml
if [[ -f /etc/n5-fangov/config.toml ]]; then
    echo "  /etc/n5-fangov/config.toml exists, not touched"
else
    echo "  /etc/n5-fangov/config.toml absent: n5-fangov setup writes it (reference: /usr/share/doc/n5-fangov/config.example.toml)"
fi
install -d -m 0755 /usr/share/n5-fangov/pve-notification
install -m 0644 "$DEPLOY"/pve-notification/*.hbs /usr/share/n5-fangov/pve-notification/
install -m 0644 "$DEPLOY/apt-90n5-fangov.conf" /usr/share/n5-fangov/apt-90n5-fangov.conf
if [[ -d /etc/pve ]]; then
    # pmxcfs does not allow chmod -> cp instead of install. The same two files
    # are embedded in the binary (internal/alert/templates, kept identical by
    # `make verify-deploy`): the dashboard's "install template" button and
    # `n5-fangov alerts template` write them too; this copy only spares the
    # first click on a fresh box and creates the directory, which the daemon
    # cannot from inside its sandbox.
    # pmxcfs is read-only without quorum: under `set -e` a failing cp would
    # abort before daemon-reload/enable, so the template is best effort.
    if mkdir -p /etc/pve/notification-templates/default 2>/dev/null \
       && cp "$DEPLOY/pve-notification/n5-fangov-subject.txt.hbs" /etc/pve/notification-templates/default/ 2>/dev/null \
       && cp "$DEPLOY/pve-notification/n5-fangov-body.txt.hbs"    /etc/pve/notification-templates/default/ 2>/dev/null; then
        echo "  PVE notification template installed (alerts -> Proxmox notifications, template 'n5-fangov')"
    else
        echo "  warning: /etc/pve not writable (no quorum?), template skipped; later: n5-fangov alerts template"
    fi
fi
# Built-in presets (n5pro-quiet/-balanced/-cool) are embedded in the binary;
# /etc/n5-fangov/presets holds the operator's own ones only.

echo "--- apt hook (kernel gate) ---"
if [[ -d /etc/apt/apt.conf.d ]]; then
    install -m 0644 "$DEPLOY/apt-90n5-fangov.conf" "$APT_HOOK"
    echo "  $APT_HOOK: 'n5-fangov check --after-update' runs after every dpkg run"
else
    echo "  /etc/apt/apt.conf.d missing (no apt): hook skipped"
fi

echo "--- systemd ---"
# daemon-reload after the unit copy: StateDirectory=/ReadWritePaths= changed in v0.3
systemctl daemon-reload
systemctl enable n5-fangov.service
echo "  enabled (not started)"

cat <<EOF

Installed $("$BIN" version). Log file: $LOG_DIR/n5-fangov.log (rotating; journal unchanged).
State (sessions, alert history): /var/lib/n5-fangov (created by systemd at the first start).

run: n5-fangov setup

Dashboard (after setup + start): the Overview is visible without signing in; everything
else — curves, presets, alerts, certificate, account — needs the login from setup.
Alerts tab: transport, PVE template, test alert. Presets tab: built-in n5pro-quiet /
n5pro-balanced (recommended) / n5pro-cool.
EOF
