# n5-fangov — guarded fan control for Proxmox VE and Debian

One static binary: regulation daemon, CLI and embedded web dashboard.

n5-fangov — a fan governor for the Minisforum N5 / N5 Pro: it takes over the PWM
channels the kernel driver already exposes and adds curves, guards and a dashboard;
generic NCT67xx/IT87xx profiles included (untested)
(formerly pvefand/ventula during development).

**Hardware-verified on the Minisforum N5 Pro** (ITE IT5571 embedded controller via the
community driver [`ltdstudio/minisforum-n5-it5571`](https://github.com/ltdstudio/minisforum-n5-it5571)).
Generic hwmon profiles for Nuvoton NCT67xx and ITE IT87xx ship as *from documentation,
untested* — the dashboard says so, per profile.

> Status: **pre-release** (`0.3.0-beta.2`; the dashboard header shows the `beta` badge
> until a release tag drops the suffix — `n5-fangov version`, `GET /api/version` and
> `/api/about` carry it as `prerelease`). Validation data, the Bash predecessor `n5-fand`
> and the measurement scripts live in
> [`minisforum-n5pro-fan-proxmox`](https://github.com/SirRenix/minisforum-n5pro-fan-proxmox).

## Why

- The N5 Pro exposes no fan control to Linux at all; the BIOS "silent" curve lets the
  drives sit at 40–42 °C and offers only fixed PWM.
- `fancontrol` regulates one channel from one sensor. The HDD group needs the hottest
  of four drives, the SSD fan the hottest of three NVMe.
- The IT5571 EC **does not resume automatic regulation of the HDD channel after any
  write** (measured 2026-09-14). A controller that hands control back to the EC on exit
  leaves the drives unregulated. n5-fangov knows that and stops to a fixed safe duty instead.

## What it guards against

| Failure | Response |
|---|---|
| Controller hangs | systemd watchdog (60 s) → kill → failsafe → restart |
| Controller dies (crash, OOM, kill) | `ExecStopPost=n5-fangov failsafe` → profile-defined safe state |
| Kernel update without the DKMS module | apt hook `check --after-update` alerts before the reboot; `ExecStartPre=n5-fangov check` fails loudly after it; fans stay in EC/BIOS mode |
| Sensor unreadable, implausible, frozen or absent | that channel at its safe duty (fixed stop duty, else 255), alert naming it, re-resolve; the other channels keep regulating |
| Fan stalls (0 RPM at duty ≥ threshold) | channel 255, alert, auto-recovery |
| Write fails or read-back differs | 255, alert |
| Somebody else writes to `/sys` | set-point re-asserted every minute, warning |
| Broken config | built-in defaults + warning + alert; daemon still starts |
| Critical temperature | 255 immediately, also in manual mode |

Alerts go to the Proxmox notification stack (`PVE::Notify`, template `n5-fangov`) when
running on PVE, otherwise `mail(1)`; always to the journal. The transport is configurable
and testable from the dashboard (see [Alerts](#alerts)).

## Install

```
# Proxmox VE 9 / Debian 13, as root
./deploy/install.sh           # or: apt install ./dist/n5-fangov_<version>_amd64.deb
n5-fangov setup
```

The installer puts the binary, the units, the apt hook, the log directory and (on PVE)
the notification template pair in place and enables the unit. It writes **no** config and starts nothing — that is
`setup`. N5 Pro only: the kernel module must be installed first (DKMS package from
the sibling repo, `experimental_write=1`); `setup` refuses without a detected profile.

## Setup

`n5-fangov setup` writes `/etc/n5-fangov/config.toml` for this machine:

1. **Profile**: detected on the box (`n5pro` → the verified three-channel set;
   `nct67xx`/`it87xx` → one conservative channel per PWM output, sensor `k10temp` /
   `coretemp` / first hwmon temperature; `monitor` → no channels).
2. **Scope** of the web UI:
   - `local` — `127.0.0.1:8010`, no auth, plain HTTP. Reach it with `ssh -L 8010:127.0.0.1:8010 <host>`
     or put a reverse proxy in front.
   - `lan` — the primary LAN address, **basic auth** (user + password asked twice
     without echo) and **HTTPS** with an automatically created self-signed certificate.
   - `HOST:PORT` — explicit address; non-loopback implies auth + HTTPS like `lan`.
3. An existing config is backed up (`config.toml.bak-<timestamp>`) before it is replaced.

Non-interactive: `n5-fangov setup --yes --listen lan --user admin --password-file /root/pw`
(all needed flags must be present). The password comes from `--password-file F` (first
line) or `--password -` (one line on stdin); `--password 'literal'` still works but is
visible in `ps`, the shell history and the journal. Afterwards:

```
n5-fangov check                  # what serve will do with this config
systemctl enable --now n5-fangov
n5-fangov status
```

Change the user or password later in the dashboard (settings gear → *Change password…* /
*Change user…*, takes effect at once) or with `n5-fangov passwd` (edits the file in place,
restart to apply). The reference with every key explained is
`/usr/share/doc/n5-fangov/config.example.toml`.

## Use

```
n5-fangov status                 temperatures, duty, RPM, mode per channel
n5-fangov set hdd 70%            manual override (limits and stall guard still apply)
n5-fangov auto hdd               back to the curve
n5-fangov curve                  active curves
n5-fangov log -n 50              log file (journal when no file is configured)
n5-fangov test 3                 channel verification run (daemon must be stopped)
n5-fangov export settings.json   config + presets as one JSON bundle
n5-fangov cert info              dashboard certificate (see HTTPS)
n5-fangov alerts status          alert transport, PVE template, recent alerts (see Alerts)
```

Web dashboard: `http://127.0.0.1:8010` (`local`) or `https://<host>:8010` (`lan`). Tabs:
Overview, Curves, Manual, Presets, Alerts, Log, Compatibility, About; the lock icon in the
header opens the certificate panel (download, trust, regenerate, upload), the gear the
settings with the account forms.

## Dashboard access

With `auth = "basic"` the dashboard has two faces, enforced by the server (the UI only
mirrors it):

| | Anonymous | Signed in |
|---|---|---|
| Overview | channel cards and the two charts (`GET /api/state` and `/api/history` in a **reduced** form: name, pwm, sensor, temp, duty, target, rpm, mode — no hwmon path, no EC temperatures, no alert stamps, no extra sensors) | full: plus the Sensors card, the extra-sensor chart, System details, recent alerts |
| About tab, version | full | full |
| Curves, Manual, Presets, Alerts, Log, Compatibility, certificate panel, settings gear | hidden; the API answers 401 | full |

Nothing pops up for an anonymous visitor: the reduced Overview is the landing page, the
**Sign in** button in the header opens the form. Basic auth stays accepted on every
protected request (CLI, curl, scripts), the cookie session is for browsers:

- **Remember me** keeps the session for 30 days on that browser, otherwise 12 hours.
  Sessions survive a daemon restart — including the restart a config change may
  require — because they are mirrored to `/var/lib/n5-fangov/sessions.json` (0600, tokens
  stored hashed; at most 50, oldest dropped).
- **Sign out** revokes the session and clears the cookie. The settings gear lists the
  active sessions (id, created, last seen, IP, remember) and offers *Sign out other
  sessions*. A browser that still holds Basic credentials from a v0.2 session sends them
  with every request and counts as signed in ("via basic"); *Sign out* cannot clear
  those — close the browser or clear the site data.
- A password or user change made outside the dashboard (`n5-fangov passwd`, editing the
  file) drops every persisted session at the next start; changes made through the
  settings gear keep the session that made them.
- **Change password… / Change user…** (settings gear) ask for the current password, write
  the new `password_hash` (or `user`) into the config file in place — comments and every
  other key untouched — apply it at once and sign every *other* session out. User names
  are `[A-Za-z0-9_.-]{1,32}`, passwords 8..128 characters. A wrong current password
  counts as a failed login for the rate limiter. The in-place edit handles the `[web]`
  header and the dotted `web.user = …` layout; an inline table `web = { … }` is refused
  with a message (edit the file by hand) — the same applies to `[alert]` and `[dashboard]`. `n5-fangov passwd` still works from the
  shell (restart to apply) — for a forgotten password, for instance.
- With `auth = "none"` every visitor counts as signed in; the account forms answer
  `409 auth is none`.

The cookie is `HttpOnly; SameSite=Strict` (`Secure` over TLS); state-changing requests
additionally need the `X-N5-Fangov-Csrf: 1` header the UI always sends, which is the
CSRF defence for the cookie session. Failed logins throttle exactly like failed basic
auth (5 free, then 250 ms doubling to 2 s per client IP).

## HTTPS

`[web].tls` is `auto` | `off` | `file`. The default follows the listener: `off` on
loopback, `auto` everywhere else, and **a non-loopback listener never runs plain HTTP** —
`tls = "off"` there is replaced by `auto` with a warning (basic auth would otherwise
cross the LAN in clear text).

### The dashboard flow

The lock icon in the header shows the transport; its tooltip carries the certificate
mode and expiry, and clicking it (or *Settings → Certificate…*) opens the certificate
panel: subject, issuer, SANs, validity (highlighted below 30 days), key type, SHA-256
fingerprint with a copy button, and the actions below. Making the browser warning go
away takes three steps:

1. **Download** — *Download .crt* (PEM: Firefox, macOS, Linux) or *Download .cer* (DER:
   Windows, Android). Both are the certificate only, never the key, and need no login.
2. **Trust** — the panel's *How to trust this certificate* lists the four recipes:
   Windows: double-click the .cer → Local Machine → Trusted Root Certification
   Authorities; macOS: Keychain Access → System → Always Trust; Firefox: Settings →
   Certificates → Authorities → Import; Android: Settings → Security → Install a
   certificate → CA certificate. Linux CLI: copy the .crt to
   `/usr/local/share/ca-certificates/` and run `update-ca-certificates`.
3. **Reload** — the connection is now verified; the fingerprint in the panel is the one
   to compare against the browser's certificate viewer.

The certificate is marked as a CA (browser stores accept a self-signed anchor only in
that form) but carries **name constraints** limited to exactly its own names and
`pathlen 0`: even with the key, nothing signed by it is valid for any other host.

### Modes and actions

- **`auto`** — the daemon creates an ECDSA P-256 self-signed certificate (10 years) in
  `/etc/n5-fangov/tls/` at the first start and reuses it. SANs: the listen host (for
  `0.0.0.0`/`[::]`: the primary IPv4 and IPv6 address, i.e. the source address of the
  default route — not every interface), the host name, `localhost`, and
  `[web].allowed_hosts`. When the names change it is reissued **with the same key**, so a
  certificate you trusted stays trusted (log: `certificate regenerated (SANs changed),
  key unchanged`).
  - **Regenerate** reissues it for the current names. The private key is kept by default;
    the checkbox *generate a new key* makes a fresh pair — every store that trusts the old
    certificate then has to import the new one, and the response says so. The same
    warning appears when the key was meant to be kept but could not be read
    (`"kept": false` in the response): a new pair was generated.
- **`file`** — your own certificate. **Upload own certificate…** takes a PEM certificate
  (chain allowed: leaf first, intermediates after it; other PEM blocks such as
  `EC PARAMETERS` are skipped) and its key (PKCS#8, PKCS#1 `RSA PRIVATE KEY` or SEC 1
  `EC PRIVATE KEY`), as files or pasted text (64 KiB max). The pair is validated first:
  PEM, key matches, not expired, a key type this server can actually sign with (ECDSA
  P-256/P-384/P-521, RSA ≥ 1024, Ed25519 — anything else is refused, as is an encrypted
  key: decrypt it with `openssl pkey -in key.pem -out key-plain.pem` first), and one test
  handshake against the listener's own TLS config, so a pair that is accepted is a pair
  the listener serves. Warnings (not errors) for a SAN list that misses a listen host, an
  expiry within 30 days, a weak key (RSA < 2048). The pair is stored as
  `/etc/n5-fangov/tls/custom-cert.pem` / `custom-key.pem` (0600), and the config is set to
  `tls = "file"` with the two paths (comments and everything else untouched). Pointing
  `cert_file`/`key_file` at files of your own by hand works the same way; they must be
  readable inside the unit's sandbox (see Hardening — `/etc/n5-fangov/` is the simple
  place). A key file readable by group or others is logged as a warning at start
  (`chmod 0600`).
  - **Lock-out guard.** The upload is refused (400) when the certificate does not cover
    the name your browser session uses (SNI, else the Host header): after the swap the
    browser would see a name mismatch and, **under HSTS, refuse the connection** — no
    warning page, no "proceed anyway", and this panel would be out of reach. The panel
    then offers an *install anyway* checkbox (`force=true` in the API); use it only when
    you can reach the dashboard by another covered name or by IP (browsers ignore HSTS
    for IP literals). Uploads through the CLI socket are not guarded.
  - **Unreadable pair at start.** When `tls = "file"` and the pair cannot be loaded or
    served (file gone, key/cert mismatch, unusable key type), the daemon does **not**
    take the dashboard down: it serves the automatic certificate instead, logs
    `FALLBACK to the automatic certificate`, sends the alert `tls` (cooled like the other
    start alerts) and reports mode `auto (fallback from file)` — the panel shows a
    warn-coloured *automatic (fallback)* badge, `GET /api/tls` carries `"fallback": true`.
    The config keeps `tls = "file"` and its paths. Repair from the panel (upload again or
    *Back to auto*) or with `n5-fangov cert upload CERT KEY` / `cert reset`, which work
    offline on a broken pair too (`cert info`/`cert export` need a loadable one).
  - **Back to auto** returns to the automatic certificate (the auto pair is kept on disk,
    so this is instant), sets `tls = "auto"` and deletes the uploaded pair; a
    `cert_file`/`key_file` of your own outside `/etc/n5-fangov/tls/` is left where it is.
  - **The daemon owns `[web] tls`, `cert_file` and `key_file` while it runs.** Every
    config write that goes through the API — curves applied from the editor, a settings
    import, a preset — gets the three keys re-applied from the certificate manager, so an
    editor that still holds the pre-upload text cannot silently revert an upload or a
    reset. Change the mode through the panel or `n5-fangov cert …`; a hand edit of the
    file takes effect at the next start (with `--listen` overriding the file, the keys are
    left as they are).
- **`off`** — plain HTTP, loopback only. A reverse proxy (Caddy, nginx, the PVE proxy)
  terminating TLS in front of `127.0.0.1:8010` is the alternative to `auto`; list its
  public name in `allowed_hosts` or let it rewrite `Host`. The panel then only says so;
  the certificate endpoints answer `409 tls is off`.

Every change from the panel is **hot-swapped**: the new certificate serves the next
handshake, open connections and the fan controller are untouched, no restart. The
listener issues no TLS session tickets, so a browser that reconnects sees the new
certificate at once instead of resuming an old session. Changes are logged as
`web: tls <regenerate|upload|reset> by <ip>` and need the same login as any other write
(`auth = "basic"`). The panel's notice lists what the server finds worth knowing about
the active certificate (`warnings` in `GET /api/tls`): an expiry within 30 days, no
SANs, and listen hosts the SAN list does not cover — under HSTS the browser will refuse
such a name, so a certificate for the LAN name should carry every name you use.

### CLI equivalents

```
n5-fangov cert info                    mode, subject, SANs, validity, fingerprint
n5-fangov cert export [--der] [FILE]   certificate only, PEM (or DER with --der); "-" = stdout
n5-fangov cert regen [--new-key]       reissue; key kept unless --new-key
n5-fangov cert upload CERT KEY         install an own PEM pair (tls = "file")
n5-fangov cert reset                   back to the automatic certificate
```

With the daemon running the commands go through the unix socket and take effect at once
(same code path as the panel). Without it they work on the files and the config directly
and print the `systemctl restart n5-fangov` that applies the change.

### Transport

TLS 1.2 minimum, modern cipher suites, HSTS header (`max-age=31536000`, no
`includeSubDomains`, no preload). **HSTS scope:** browsers apply it to the whole host
*name*, all ports — after one visit to `https://n5.lan:8010` the browser also
rewrites `http://n5.lan/` (port 80) to HTTPS for a year. Browsers ignore HSTS for
IP literals, so `https://192.0.2.10:8010` affects nothing else. Reach the UI by IP, or
make sure every service on that name speaks HTTPS; a reverse proxy in front of
`tls = "off"` sets its own policy (n5-fangov sends the header only on its own TLS
listener). `listen`, `auth` and `allowed_hosts` still need a restart; the certificate
does not.

## Security

The API changes fan duties, so treat the port like a management interface.

- **Default is loopback only** (`[web].listen = "127.0.0.1:8010"`, no auth). The CLI
  uses the unix socket in `/run/n5-fangov` (root only, `RuntimeDirectoryMode=0750`).
- **LAN access means auth + TLS.** `setup --listen lan` configures both; by hand:

  ```toml
  [web]
  listen = "192.0.2.10:8010"
  auth = "basic"
  user = "admin"
  password_hash = "pbkdf2$210000$<salt hex>$<key hex>"   # written by: n5-fangov passwd
  tls = "auto"                                         # or "file" with cert_file/key_file
  allowed_hosts = ["fans.example.internal"]            # names a proxy passes in Host
  ```

- **Password hashes.** `n5-fangov passwd` and `setup` store salted PBKDF2-HMAC-SHA256
  (210 000 iterations, 16-byte random salt) as `pbkdf2$<iter>$<salt>$<key>`. The
  earlier form — the plain `sha256` hex of `user:password` (64 characters, as produced
  by `printf 'admin:password' | sha256sum`) — **keeps working**; the daemon accepts
  both. Run `n5-fangov passwd` once to upgrade an old hash (a restart applies it). A
  config file that carries a hash is written `0600`; an existing wider mode is
  tightened and logged.

- **Fail closed.** `auth = "basic"` with a missing or unusable `password_hash`, or a
  typo in `auth`, never degrades to an open LAN listener: the daemon forces
  `listen` to `127.0.0.1:8010` and logs `auth misconfigured — web bound to loopback`.
  `auth = "none"` on a non-loopback address is allowed (still HTTPS) but logged as a
  warning at every start and by `n5-fangov check`.
- **What auth covers.** With `auth = "basic"`, everything under `/api/` needs
  credentials (cookie session or Basic) except `GET /api/version`, `/api/about`,
  `/api/session`, the login/logout endpoints and the **reduced** `GET /api/state` /
  `/api/history` (channel temperatures, duties, RPM and modes — see Dashboard access).
  Config, sensors, presets, profiles, log, certificate downloads, alerts, account and
  every write are protected. Failed logins are throttled per client IP (5 free, then
  250 ms doubling to 2 s, reset after 10 min or a success) and logged with user name and
  IP — only when a credential was actually presented; the anonymous 401 the UI gets
  before login is not a failure. At most 4 delayed attempts per IP are in flight at
  once; further ones get an immediate `429` without a hash computation, so parallel
  requests cannot side-step the delay or burn CPU on PBKDF2.
- **The hash never leaves the daemon.** `GET /api/config` and the settings export show
  `password_hash = "<unchanged>"`; sending that text back keeps the stored hash.
- **Host header check (DNS rebinding).** Requests are only served for IP literals,
  `localhost`, the listen host and `allowed_hosts`; anything else gets 421. A reverse
  proxy must either rewrite `Host` to the upstream (nginx does by default, Caddy:
  `header_up Host {upstream_hostport}`) or its public name must be listed in
  `allowed_hosts`. `"*"` disables the check.
- **CSRF.** Every write needs the header `X-N5-Fangov-Csrf: 1`; a browser form or
  cross-site fetch cannot add it without CORS, which the API does not offer.
- Changing `[web]` or `[log]` settings takes a restart (except user/password through the
  account forms); `PUT /api/config` reloads curves, sensors, `[daemon]`, `[alert]` and
  `[dashboard]` values.

## Configure

`/etc/n5-fangov/config.toml` — curves as point lists, sensor source per channel:

```toml
[[channel]]
name = "hdd"
pwm = 3
sensor = "drivetemp:max"
curve = [[36,105],[46,255]]
critical = 56
stop = 140            # fixed stop duty: the EC won't regulate this channel after a write
```

Sensor sources: `k10temp`, `coretemp`, `nvme:max`, `drivetemp:max`, `hwmon:<name>:tempN`,
`ec:<label>` (N5 Pro EC temperatures). Invalid values fall back to defaults with a warning.

In the Curves tab, *+ add point* inserts a point at the middle of the widest temperature
gap (duty interpolated) and keeps the table sorted; editing a temperature re-sorts the
rows when the field loses focus.

### Presets

`/etc/n5-fangov/presets/<name>.toml` holds only `[[channel]]` tables; *Save current
curves as…* writes one, *Apply* replaces the channel set of the config file with it and
reloads (a changed channel set or profile answers "restart required"), *Delete* removes a
user preset. Three N5 Pro sets are **built in** (embedded in the binary, listed and
applicable for the `n5pro` profile only — on another profile the API answers 404 —, never
saved over or deleted — the API answers 409):

| Preset | cpu (`k10temp`, critical 88) | ssd (`nvme:max`, critical 72) | hdd (`drivetemp:max`, stop 140) |
|---|---|---|---|
| `n5pro-quiet` — lowest noise, HDD group settles around 45 °C | `[[30,25],[61,163],[85,255]]` | `[[35,55],[65,255]]` | `[[26,63],[55,92],[56,255]]`, critical 66 |
| `n5pro-balanced` — **recommended**: HDD group held near 40 °C, audible under load only | `[[35,60],[60,150],[80,255]]` | `[[35,74],[55,160],[68,255]]` | `[[30,87],[42,140],[50,200],[55,255]]`, critical 60 |
| `n5pro-cool` — drives first, noise second | `[[30,85],[55,170],[75,255]]` | `[[30,90],[50,180],[65,255]]` | `[[28,105],[38,150],[45,210],[50,255]]`, critical 58 |

Duty → RPM on the N5 Pro (measured): CPU 85→2000, 140→3120, 255→5073; SSD 74→2130,
255→4687; HDD 87→1237, 105→1650, 140→2250, 179→2725, 255→3540. The HDD channel keeps
`stop = 140` in every set because the EC does not regulate it after a write. A user
preset file with a built-in name is shadowed by the built-in (logged when listing).

### Extra sensors on the dashboard

```toml
[dashboard]
sensors = ["hwmon:amdgpu:temp1", "ec:system"]   # 0..8 ids, same forms as channel sensors
```

The signed-in Overview lists every readable temperature (`GET /api/sensors`, grouped
CPU / SSD / HDD / GPU / NIC / EC / other); the *chart* toggle per row adds or removes the id
here (`PUT /api/dashboard`). Watched sensors are read once per cycle after the channel
sensors, recorded in the history (`history[].extra`) and drawn in the *Extra sensors*
chart card; they never influence regulation. An id whose device is absent right now is
kept with a warning and charted once it appears; an id that is not a known form is
refused. The list applies without a restart, also when edited in the config file.

## Alerts

```toml
[alert]
transport = "auto"     # auto | pve | mail | log | off
mail_to = "root"       # mail transport only: local user or address
```

`auto` (the default) takes `PVE::Notify` when `/usr/share/perl5/PVE/Notify.pm` and perl
are present, else `mail(1)` to `mail_to`, else the journal only. `pve` or `mail` without
their tool degrade in that same order and `n5-fangov check` says so; `off` drops every
alert but still writes a `suppressed (transport off)` line to the journal. Cooldown per
kind is `[daemon].alert_cooldown` (30 min default) for the daemon's alerts; the
onfailure unit and the apt hook keep their own 30-min stamps.

The **Alerts tab** shows the configured and the effective transport, which tools the box
has, the PVE template state, the cooldown, every alert kind with its last delivery, and
the recent alerts (newest first, the last 50, kept across restarts in
`/var/lib/n5-fangov/alerts.json`). Actions:

- **Save** transport and `mail_to` — written to the config file in place and hot-applied;
  no restart. A `PUT /api/config`, a settings import or a preset apply re-applies whatever
  `[alert]` the written file contains.
- **Send test alert** — kind `test`, no cooldown, through the real transport; the response
  carries the delivery error when perl/mail fail. It also lands in the recent list. One
  test at a time (a second click while one runs answers `409 test in progress`), bounded
  to 20 s. `mail_to` is a local user or an address without spaces or quotes and never
  starts with `-`; the recipient is passed to `mail(1)` after `--`.
- **Install / Update template** — writes the two PVE notification template files
  (`n5-fangov-subject.txt.hbs`, `n5-fangov-body.txt.hbs`, embedded in the binary) to
  `/etc/pve/notification-templates/default/`. The button is disabled with the reason when
  the directory is missing or not writable: the daemon's sandbox may write *into* that
  directory but cannot create it, so on a fresh box `install.sh` or `n5-fangov alerts
  template` (root, outside the sandbox) create it. *Current* compares the installed files
  with the embedded ones after an upgrade. The writable probe (a temp file created and
  removed in the directory, a write on pmxcfs) runs at most every 10 minutes and right
  after *Install* or *Save*, not on every poll of the panel.

**The PVE side.** Alerts arrive as severity *warning* with the fields `type = n5-fangov`,
`hostname` and `kind = <alert kind>`. Without a matcher they follow the default matcher
(mail to root). To route them: *Datacenter → Notifications → Notification Matchers →
Add*, match field `type` = `n5-fangov` (or `kind` = `stall`, `temp`, …) and pick the
target (SMTP, Gotify, webhook). The template gives the mail its subject
`[<host>] n5-fangov: <kind>` and body.

Alert kinds: `sensor` (channel sensor unusable → safe duty), `stall` (0 RPM → 255), `temp`
(critical temperature → 255), `write` (write errors → failsafe), `config` (parse
warnings, defaults in effect), `config-channels` (channel set corrected), `restart` /
`failed` (onfailure unit), `kernel` (DKMS module missing for a bootable kernel), `tls`
(custom certificate unreadable, automatic one served), `test`.

```
n5-fangov alerts status          transport, tools, template, last alert per kind, recent alerts
n5-fangov alerts test            send a test alert now
n5-fangov alerts template        install/update the PVE template pair
```

`status` and `test` go through the daemon's socket when it runs (the test then shows in
the dashboard), otherwise they work on the config file. `template` asks the daemon first
and writes the files itself when that fails for anything but "not a PVE host".

## Logs

The journal (`journalctl -u n5-fangov`) is always written and never touched by
n5-fangov. In addition the daemon keeps its own file:

```toml
[log]
file = "/var/log/n5-fangov/n5-fangov.log"   # "" = journal only
max_size_mb = 5                             # rotate above this size (1..100)
max_files = 5                               # keep .1 .. .5 (1..20)
```

Lines in the file carry their own timestamp; rotation renames `n5-fangov.log` to `.1`,
shifts older files up and drops the oldest. Directory `0750`, file `0640` (the unit
runs with `UMask=0077`). `file` must be a plain absolute path under `/var/log/` (no
`..`); anything else falls back to the default with a warning, and an existing target
that is not a regular file (symlink, device, directory) is refused — the daemon appends
as root and must not be pointed at `/dev/sda` or its own config.

```
n5-fangov log -n 200                 newest lines of the current file
n5-fangov log --export today.log     whole current file (rotated files not included)
n5-fangov log --clear                truncate the current file; rotated files and the journal stay
```

The dashboard's Log tab uses the same file (`GET /api/log`, `/api/log/export`,
`DELETE /api/log`); without a file it falls back to the journal and cannot clear.

## Backup / Restore

```
n5-fangov export settings.json       # {"format":1, config: <toml>, presets: {name: <toml>}}
n5-fangov import settings.json       # validates everything, then writes and reloads
```

The export is the config file text (comments included) plus every user preset (the
built-in ones travel with the binary), with the password hash redacted to `<unchanged>`. `import` refuses the whole bundle when any
part fails to parse — nothing is written in that case. `<unchanged>` is resolved from
the password stored on the importing machine; on a fresh box run `n5-fangov passwd`
first (or put a real hash into the bundle). Presets that exist locally but not in the
bundle stay. A running daemon reloads curves and `[daemon]` values; a changed channel
set or profile, or any `[web]`/`[log]` change, needs `systemctl restart n5-fangov`
(the command says so). The API offers the same (`GET /api/config/export`,
`POST /api/config/import`, auth + CSRF).

## Updates

**Package update** (`apt upgrade` of n5-fangov): postinst restarts a running daemon;
`ExecStopPost=failsafe` puts the fans into the safe state between the old and the new
process, `check --quiet` gates the new start.

**Kernel update** (N5 Pro): the EC driver is a DKMS module. If DKMS did not build it for
the new kernel, the next boot has no `pwm` files, `check` fails and the fans stay under
BIOS/EC control (safe, but unregulated for the drives). Two gates catch this:

1. `/etc/apt/apt.conf.d/90n5-fangov` runs `n5-fangov check --after-update` after every
   dpkg run. It requires `updates/dkms/minisforum_n5_it5571.ko` for every kernel the
   box can **boot into**: the running one plus what `proxmox-boot-tool kernel list`
   selects (manually, automatically, pinned); without that tool the running one plus
   the newest installed. Missing → `kernel X: fan driver module missing — run: dkms
   install minisforum-n5-it5571/<ver> -k X` on the apt output plus a `kernel` alert
   (30 min cooldown). Older kernels that are merely still installed (apt keeps two)
   get an `info only` line and no alert. The apt run never fails because of it.
   Non-N5-Pro boxes: no-op.
2. `ExecStartPre=n5-fangov check` reports `dkms` for the running kernel at every start.

What a Proxmox upgrade **can** affect: the kernel (above), `dkms` itself, perl/
`PVE::Notify` for alerts. What it **cannot**: the config, presets, TLS certificate and
logs live under `/etc/n5-fangov` and `/var/log/n5-fangov` and are never touched by
package scripts (purge removes them). The state directory `/var/lib/n5-fangov`
(sessions, alert history) is removed with the package; nothing in it is worth keeping.

## Hardening

The unit runs sandboxed (`deploy/n5-fangov.service`); the list is the contract, verify
on real hardware after every change — `/sys` writes are what most sandboxes forbid.

| Setting | Effect |
|---|---|
| `NoNewPrivileges=yes`, `LockPersonality=yes`, `RestrictRealtime=yes` | no privilege escalation from the daemon |
| `ProtectSystem=strict` + `ReadWritePaths=-/etc/n5-fangov -/run/n5-fangov -/var/log/n5-fangov -/var/lib/n5-fangov -/sys/class/hwmon -/sys/devices -/var/spool/postfix/maildrop -/etc/pve/notification-templates` | whole file system read-only except config/presets/tls, runtime dir, logs, state dir, the hwmon attributes, the postfix maildrop (alerts via `mail`) and the PVE template directory (the Alerts tab's install button; verified on the reference host — the daemon may write into it but cannot create it); `-` = a missing path does not fail the start |
| `ProtectKernelTunables=no` | **must stay `no`**: the pwm files are kernel tunables |
| `ProtectHome=yes`, `PrivateTmp=yes`, `PrivateDevices=yes`, `ProtectControlGroups=yes` | no access to home, private /tmp, no physical devices in /dev, cgroups read-only |
| `RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6 AF_NETLINK`, `RestrictNamespaces=yes` | socket, TCP/HTTP(S), netlink for the interface list (`net.Interfaces()` — the fallback when the TLS certificate needs the box's addresses), nothing else |
| `MemoryDenyWriteExecute=yes`, `SystemCallArchitectures=native`, `SystemCallFilter=@system-service` | no JIT/exec tricks, native syscalls only, no module loading |
| `CapabilityBoundingSet=` (empty) | the daemon runs as uid 0 but holds no capability: it never loads modules (modules-load.d does), never chowns, and root's own files and the root-owned sysfs attributes need none |
| `UMask=0077`, `LogsDirectory=n5-fangov` (`0750`), `RuntimeDirectory=n5-fangov` (`0750`), `StateDirectory=n5-fangov` (`0700`) | files private by default; `/var/lib/n5-fangov` holds `sessions.json` (hashed session tokens — secrets, hence 0700) and `alerts.json`. `serve --state-dir DIR` / `N5FANGOV_STATE_DIR` move it; an unwritable one is logged once and the daemon runs without persistence (sessions and history in memory) |

**Alert delivery under the sandbox.** Alerts run `perl -MPVE::Notify` or `mail(1)`
from inside this sandbox. Verified: **PVE notification targets of type SMTP** (the
Proxmox stack talks to the mail server itself; nothing on the local file system is
written). The `mail(1)`/sendmail path on non-PVE hosts, and a PVE *sendmail* target,
hand the message to postfix's `postdrop`, which writes into
`/var/spool/postfix/maildrop` — that directory is in `ReadWritePaths`, but it is
`0730 postfix:postdrop` and `NoNewPrivileges` suppresses the setgid bit `postdrop`
relies on, so this path additionally needs `CAP_DAC_OVERRIDE`. Not verified; if you
need it, add a drop-in (`systemctl edit n5-fangov`) with
`CapabilityBoundingSet=CAP_DAC_OVERRIDE` and test with the Alerts tab's *Send test
alert* — that one is sent by the daemon from inside the sandbox and reports the
delivery error (`n5-fangov alert` or `alerts test` from a shell with the daemon stopped
run outside the sandbox and prove nothing). A failed delivery is logged
(`alert: ... failed`), the alert text is always in the journal.

`make verify-deploy` runs `systemd-analyze verify` over the units and `apt-config`
over the apt hook (on a host that has them; the Docker build container skips both
with a note). Run it on the target box after editing the unit.

## Compatibility

| Platform | Status |
|---|---|
| Proxmox VE 9.2 / Debian 13 (trixie), kernel 7.0.12-1-pve (DKMS module also built for 7.0.0-2/7.0.0-3-pve), driver `minisforum-n5-it5571` 0.2.0, Minisforum N5 Pro BIOS 1.05 | **verified** (channel mapping, stop behaviour, load tests, multi-hour runs; every release is verified on this box before it is tagged) |
| Debian/Ubuntu with NCT67xx (`nct6775`) or IT87xx (`it87`) | from documentation, untested — please report |
| Any Linux with hwmon, no PWM | monitoring only |
| Unraid, TrueNAS, non-systemd | binary runs; the guard chain relies on systemd |

## Build

No Go toolchain needed locally: `tools/remote-go.ps1` builds in a `golang:1.25-alpine`
container (static, `CGO_ENABLED=0`). Or plainly: `CGO_ENABLED=0 go build ./cmd/n5-fangov`.
`make deb` builds the Debian package.

## About and license

GPL-2.0-only ([text](https://www.gnu.org/licenses/old-licenses/gpl-2.0.html)). The
dashboard's About tab (public, `GET /api/about`) carries name, version with the
pre-release tag, license, repository and author links, the Go version the binary was
built with, and the credits: [`ltdstudio/minisforum-n5-it5571`](https://github.com/ltdstudio/minisforum-n5-it5571)
(the kernel driver for the IT5571 EC) and [`Sl0thC0der/proxfansx`](https://github.com/Sl0thC0der/proxfansx)
(the dashboard idea and the generic NCT67xx/IT87xx handling the `nct67xx`/`it87xx` profiles follow; no code shared). A UI mock for screenshots and layout work runs with
`?mock=1` (`&auth=none`, `&user=1` for the signed-in variants; login `admin`/`admin`).

Versioning: `internal/version.Version` is `0.3.0-beta.2`; `make` overrides it with
`git describe` (`v0.3.0-beta.1-3-gabcdef` on commits after a tag). The Debian package
version maps `-alpha`/`-beta`/`-rc` to `~` (so `0.3.0~beta.1` sorts before `0.3.0`) and
every other `-` to `+` (`0.3.0~beta.1+3+gabcdef` sorts after the tag it is based on).
