#!/usr/bin/env bash
# Removes n5-fangov (daemon, units, helper, templates, apt hook, state dir). Keeps
# /etc/n5-fangov (config, presets, TLS certificate) and /var/log/n5-fangov
# unless --purge is given. The channels are put into their configured safe
# state before the binary goes away. Run as root.
set -u
[[ $EUID -eq 0 ]] || { echo "run as root"; exit 1; }
# The package is removed by apt; this script would delete its files while dpkg
# still lists it as installed.
if dpkg-query -W -f '${Status}' n5-fangov 2>/dev/null | grep -q 'install ok installed'; then
    echo "n5-fangov is installed as a package (dpkg): use apt remove n5-fangov (or apt purge n5-fangov)"; exit 1
fi
PURGE=0
[[ "${1:-}" == "--purge" ]] && PURGE=1

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
rm -rf /usr/libexec/n5-fangov /usr/share/n5-fangov /usr/share/doc/n5-fangov
rm -f /etc/apt/apt.conf.d/90n5-fangov
rm -f /etc/pve/notification-templates/default/n5-fangov-subject.txt.hbs \
      /etc/pve/notification-templates/default/n5-fangov-body.txt.hbs 2>/dev/null || true
rm -rf /run/n5-fangov
# sessions.json (dashboard cookies) and alerts.json: nothing worth keeping
rm -rf /var/lib/n5-fangov
if [[ $PURGE -eq 1 ]]; then
    rm -rf /etc/n5-fangov /var/log/n5-fangov
    echo "  --purge: /etc/n5-fangov and /var/log/n5-fangov removed"
else
    echo "  /etc/n5-fangov (config, presets, tls/) and /var/log/n5-fangov kept; --purge removes them"
fi

echo
echo "Done. Fans are in the configured safe state; on the N5 Pro the HDD channel"
echo "returns to full EC control only after a cold boot."
