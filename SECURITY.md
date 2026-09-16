# Security policy

n5-fangov runs as root on the machine whose fans it controls and exposes a web
dashboard on the LAN. Reports about anything that could let someone change fan
duties, read the configuration, take over the dashboard or break out of the
systemd sandbox are welcome.

## Reporting a vulnerability

- **Preferred:** GitHub private vulnerability reporting — *Security → Report a
  vulnerability* on this repository. The report stays private until a fix is out.
- Do **not** open a public issue or pull request for a security problem.
- Include: version (`n5-fangov version`), how the daemon is reached (loopback /
  LAN, `auth`, `tls` mode), steps to reproduce, and what an attacker gains.
  Please do not include your real addresses, host names or certificates —
  replace them with documentation values (`192.0.2.x`, `n5host`).

Expect an acknowledgement within 7 days and a fix or a workaround within 30 days
for confirmed issues. Fixed versions are listed in `CHANGELOG.md`; the release
notes name the report (credit optional, say if you prefer not to be named).

## Supported versions

| Version | Supported |
|---|---|
| latest release / pre-release on the releases page | yes |
| anything older | no — update |

## Scope notes

- The unix socket in `/run/n5-fangov` is root-only by design and carries no
  authentication; a process that can open it is already root.
- `auth = "none"` is meant for loopback only; the daemon refuses plain HTTP on a
  LAN address. Reports that need `auth = "none"` on a LAN listener are
  configuration, not vulnerabilities.
- The kernel driver (`minisforum_n5_it5571`) is a separate project; report
  driver issues upstream.

## What this repository keeps out

No infrastructure data is accepted in commits: addresses, host or domain names,
serial numbers, MAC addresses, password hashes, certificates or tokens. A
`gitleaks` pre-commit hook (`make hooks`) and a workflow on every push enforce
that with the rules in `.gitleaks.toml`.
