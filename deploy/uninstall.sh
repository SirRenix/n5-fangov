#!/usr/bin/env bash
# Removes pvefand (daemon, units, helper, templates). Keeps /etc/pvefand.
# The channels are put into their configured safe state before the binary
# goes away. Run as root.
set -u
[[ $EUID -eq 0 ]] || { echo "run as root"; exit 1; }

echo "--- stop (ExecStopPost runs the failsafe) ---"
systemctl disable --now pvefand.service 2>/dev/null || true
systemctl reset-failed pvefand.service 2>/dev/null || true

echo "--- explicit failsafe with the config still present ---"
if [[ -x /usr/bin/pvefand ]]; then
    /usr/bin/pvefand failsafe || true
else
    echo "  /usr/bin/pvefand missing, skipped"
fi

echo "--- files ---"
rm -f /etc/systemd/system/pvefand.service /etc/systemd/system/pvefand-onfailure.service
systemctl daemon-reload
rm -f /usr/bin/pvefand
rm -rf /usr/libexec/pvefand /usr/share/pvefand
rm -f /etc/pve/notification-templates/default/pvefand-subject.txt.hbs \
      /etc/pve/notification-templates/default/pvefand-body.txt.hbs 2>/dev/null || true
rm -rf /run/pvefand
echo "  /etc/pvefand (config, presets) kept; delete by hand if no longer needed"

echo
echo "Done. Fans are in the configured safe state; on the N5 Pro the HDD channel"
echo "returns to full EC control only after a cold boot."
