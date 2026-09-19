#!/bin/sh
# n5-fangov emergency hook example (installed to
# /usr/share/doc/n5-fangov/examples/, never run by the package).
#
# The daemon runs exactly one file, at a fixed path, and only when that file
# is a regular file owned by root, executable and neither group- nor
# world-writable (0700, 0750 or 0755); anything else is logged and skipped.
# Nothing the dashboard or the API can write creates or changes that file.
# Install a copy by hand and switch the hook on in the config:
#
#   install -m 0750 -o root -g root \
#       /usr/share/doc/n5-fangov/examples/emergency.example.sh \
#       /etc/n5-fangov/emergency.sh
#
#   [daemon]
#   emergency = true
#   emergency_cycles = 6
#
# `n5-fangov check` prints the hook's state (ok / absent / refused: why).
#
# The daemon runs the hook once per ceiling episode, inside its own systemd
# sandbox, when a channel has stayed at its ceiling (HDD 65 / SSD 85 /
# CPU 100 C, or a lower configured value) for emergency_cycles consecutive
# cycles while the stall detection reports its fan as stopped, or for three
# times as many cycles regardless of RPM. It is bounded to 60 s (the whole
# process group is killed at the timeout); the first 4 KiB of its output go
# to the daemon log, and the "emergency" alert carries the exit status. It
# re-arms only after the channel has left the ceiling state.
#
# Environment set by the daemon (everything else is the daemon's own):
#   N5_CHANNEL  channel name                            (e.g. hdd)
#   N5_SENSOR   the channel's sensor id                 (e.g. drivetemp:max)
#   N5_PART     the part at its ceiling (= N5_SENSOR unless composite)
#   N5_TEMP     that part's reading, degrees C          (e.g. 66.0)
#   N5_CEILING  that part's ceiling, degrees C          (e.g. 65)
#   N5_RPM      fan speed this cycle; -1 without a tachometer
#   N5_CYCLES   consecutive cycles at the ceiling
#
# What is known about the sandbox (deploy/n5-fangov.service): AF_UNIX and the
# @system-service syscall set are allowed, so logger reaches the journal and
# systemctl reaches PID 1 over its private socket; the capability set is
# empty, but a poweroff request is carried out by PID 1, not by the caller.
# ProtectHome=yes hides /root and /home, and the hook may write only under
# the unit's ReadWritePaths (/etc/n5-fangov, /var/lib/n5-fangov, ...).
# The logger line below is exercised by the release-gate re-test; the
# poweroff line has NOT been executed from inside the sandbox on the
# reference host yet -- test it deliberately before you rely on it.
#
# The script exits with the status of its last command (set -e stops it at
# the first failure), so a failed logger shows up in the emergency alert.
set -eu

logger -t n5-fangov-emergency "channel ${N5_CHANNEL:-?} (${N5_PART:-?}) at ${N5_TEMP:-?} C, ceiling ${N5_CEILING:-?} C, ${N5_RPM:-?} rpm, ${N5_CYCLES:-?} cycles at the ceiling"

# Shut the box down. Uncomment once you have tested the line from the daemon
# (set emergency_cycles low and the hdd ceiling to 30 for the test, see
# docs/RELEASE-GATE.md "Update re-test"). systemctl asks PID 1 to power off;
# a clean shutdown stops every VM and container first. The daemon gets
# SIGTERM before it can raise the emergency alert -- the journal line above
# is the lasting evidence, so keep it (with `|| true` in front of the
# poweroff if the journal must never block the shutdown).
# systemctl poweroff
