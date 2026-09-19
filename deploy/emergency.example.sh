#!/bin/sh
# n5-fangov emergency action example (installed to /usr/share/doc/n5-fangov/,
# never run by the package). Copy it somewhere root-owned and executable,
# e.g. /usr/local/sbin/n5-fangov-emergency, and point the daemon at it:
#
#   [daemon]
#   emergency_command = "/usr/local/sbin/n5-fangov-emergency"
#   emergency_cycles = 6
#
# The daemon runs the command once per ceiling episode through /bin/sh -c,
# inside its own systemd sandbox, when a channel has stayed at its ceiling
# (HDD 65 / SSD 85 / CPU 100 C, or a lower configured value) for
# emergency_cycles consecutive cycles while its fan reports 0 RPM, or for
# three times as many cycles regardless of RPM. It is bounded to 60 s;
# its output goes to the daemon log, and the "emergency" alert carries the
# exit status. It re-arms only after the channel has left the ceiling state.
#
# Environment set by the daemon (everything else is the daemon's own):
#   N5_CHANNEL  channel name            (e.g. hdd)
#   N5_SENSOR   its sensor id           (e.g. drivetemp:max)
#   N5_TEMP     raw reading, degrees C  (e.g. 66.0)
#   N5_CEILING  the ceiling, degrees C  (e.g. 65)
#   N5_RPM      fan speed this cycle; -1 without a tachometer
#   N5_CYCLES   consecutive cycles at the ceiling
#
# What is known about the sandbox (deploy/n5-fangov.service): AF_UNIX and the
# @system-service syscall set are allowed, so logger reaches the journal and
# systemctl reaches PID 1 over its private socket; the capability set is
# empty, but a poweroff request is carried out by PID 1, not by the caller.
# The logger line below is exercised by the release-gate re-test; the
# poweroff line has NOT been executed from inside the sandbox on the
# reference host yet -- test it deliberately before you rely on it.
set -u

logger -t n5-fangov-emergency "channel ${N5_CHANNEL:-?} (${N5_SENSOR:-?}) at ${N5_TEMP:-?} C, ceiling ${N5_CEILING:-?} C, ${N5_RPM:-?} rpm, ${N5_CYCLES:-?} cycles at the ceiling"

# Shut the box down. Uncomment once you have tested the line from the daemon
# (set emergency_cycles low and the hdd ceiling to 30 for the test, see
# docs/RELEASE-GATE.md "Update re-test"). systemctl asks PID 1 to power off;
# a clean shutdown stops every VM and container first.
# systemctl poweroff

exit 0
