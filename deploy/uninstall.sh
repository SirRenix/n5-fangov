#!/usr/bin/env bash
# Removes ventula (daemon, units, helper, templates). Keeps /etc/ventula.
# The channels are put into their configured safe state before the binary
# goes away. Run as root.
set -u
[[ $EUID -eq 0 ]] || { echo "run as root"; exit 1; }

echo "--- stop (ExecStopPost runs the failsafe) ---"
systemctl disable --now ventula.service 2>/dev/null || true
systemctl reset-failed ventula.service 2>/dev/null || true

echo "--- explicit failsafe with the config still present ---"
if [[ -x /usr/bin/ventula ]]; then
    /usr/bin/ventula failsafe || true
else
    echo "  /usr/bin/ventula missing, skipped"
fi

echo "--- files ---"
rm -f /etc/systemd/system/ventula.service /etc/systemd/system/ventula-onfailure.service
systemctl daemon-reload
rm -f /usr/bin/ventula
rm -rf /usr/libexec/ventula /usr/share/ventula
rm -f /etc/pve/notification-templates/default/ventula-subject.txt.hbs \
      /etc/pve/notification-templates/default/ventula-body.txt.hbs 2>/dev/null || true
rm -rf /run/ventula
echo "  /etc/ventula (config, presets) kept; delete by hand if no longer needed"

echo
echo "Done. Fans are in the configured safe state; on the N5 Pro the HDD channel"
echo "returns to full EC control only after a cold boot."
