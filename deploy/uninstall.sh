#!/usr/bin/env bash
# Removes n5-fangov (daemon, units, helper, templates). Keeps /etc/n5-fangov.
# The channels are put into their configured safe state before the binary
# goes away. Run as root.
set -u
[[ $EUID -eq 0 ]] || { echo "run as root"; exit 1; }

echo "--- stop (ExecStopPost runs the failsafe) ---"
systemctl disable --now n5-fangov.service 2>/dev/null || true
systemctl reset-failed n5-fangov.service 2>/dev/null || true

echo "--- explicit failsafe with the config still present ---"
if [[ -x /usr/bin/n5-fangov ]]; then
    /usr/bin/n5-fangov failsafe || true
else
    echo "  /usr/bin/n5-fangov missing, skipped"
fi

echo "--- files ---"
rm -f /etc/systemd/system/n5-fangov.service /etc/systemd/system/n5-fangov-onfailure.service
systemctl daemon-reload
rm -f /usr/bin/n5-fangov
rm -rf /usr/libexec/n5-fangov /usr/share/n5-fangov
rm -f /etc/pve/notification-templates/default/n5-fangov-subject.txt.hbs \
      /etc/pve/notification-templates/default/n5-fangov-body.txt.hbs 2>/dev/null || true
rm -rf /run/n5-fangov
echo "  /etc/n5-fangov (config, presets) kept; delete by hand if no longer needed"

echo
echo "Done. Fans are in the configured safe state; on the N5 Pro the HDD channel"
echo "returns to full EC control only after a cold boot."
