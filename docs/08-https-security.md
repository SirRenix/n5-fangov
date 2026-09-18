# HTTPS and security

What this page covers: the dashboard certificate (automatic, own pair, trust recipes
per OS), TLS transport and HSTS, what a visitor sees with and without a login, sessions
and cookies, API tokens for scripts, login throttling, password hashes, the systemd
sandbox, and running behind a reverse proxy. Reporting a vulnerability:
[SECURITY.md](../SECURITY.md).

- [Defaults](#defaults)
- [The certificate](#the-certificate)
- [Modes and actions](#modes-and-actions)
- [CLI equivalents](#cli-equivalents)
- [Transport and HSTS](#transport-and-hsts)
- [Who sees what](#who-sees-what)
- [Sessions](#sessions)
- [API tokens](#api-tokens)
- [What auth covers](#what-auth-covers)
- [Login throttling](#login-throttling)
- [Password hashes](#password-hashes)
- [Host header and CSRF](#host-header-and-csrf)
- [Behind a reverse proxy](#behind-a-reverse-proxy)
- [Hardening](#hardening)
- [Alert delivery under the sandbox](#alert-delivery-under-the-sandbox)

## Defaults

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
  allowed_hosts = ["fans.example.test"]                # names a proxy passes in Host
  ```

- **Fail closed.** `auth = "basic"` with a missing or unusable `password_hash`, or a
  typo in `auth`, never degrades to an open LAN listener: the daemon forces `listen` to
  `127.0.0.1:8010` and logs `auth misconfigured — web bound to loopback`.
  `auth = "none"` on a non-loopback address is allowed (still HTTPS) but logged as a
  warning at every start and by `n5-fangov check`.
- `[web].tls` is `auto` | `off` | `file`. The default follows the listener: `off` on
  loopback, `auto` everywhere else, and **a non-loopback listener never runs plain
  HTTP** — `tls = "off"` there is replaced by `auto` with a warning (basic auth would
  otherwise cross the LAN in clear text).

## The certificate

The lock icon in the header shows the transport; its tooltip carries the certificate
mode and expiry, and clicking it (or *Settings → Certificate…*) opens the certificate
panel: subject, issuer, SANs, validity (highlighted below 30 days), key type, SHA-256
fingerprint with a copy button, and the actions below
([screenshots](04-dashboard.md#lock-and-certificate-panel)). Making the browser warning
go away takes three steps:

1. **Download** — *Download .crt* (PEM: Firefox, macOS, Linux) or *Download .cer* (DER:
   Windows, Android). Both are the certificate only, never the key; like the rest of the
   panel they need a signed-in session (`n5-fangov cert export` writes the same file from
   the shell). So the first visit goes through the browser's warning page once —
   *Advanced → proceed* (the wording differs per browser) —, then sign in, then
   download.
2. **Trust** — the panel's *How to trust this certificate* lists the recipes.
   - Windows: double-click the `.cer` → *Install Certificate…* → *Local Machine* → on
     the store page **do not** leave the default *Automatically select the certificate
     store based on the type of certificate* (it lands in the wrong store and the
     warning stays) — choose *Place all certificates in the following store* →
     *Browse…* → *Trusted Root Certification Authorities* → *Next* → *Finish*. Then
     close every browser window and reopen; an open browser keeps its old view of the
     store.
   - Firefox keeps its own store on every OS: Settings → Certificates → Authorities →
     Import (the `.crt`), tick *Trust this CA to identify websites*.
   - macOS: Keychain Access → System → import → Always Trust.
   - Android: Settings → Security → Install a certificate → CA certificate.
   - Linux CLI: copy the `.crt` to `/usr/local/share/ca-certificates/` and run
     `update-ca-certificates`.
3. **Reload** — the connection is now verified; the fingerprint in the panel is the one
   to compare against the browser's certificate viewer. Still a warning after the
   Windows import: [Troubleshooting](10-troubleshooting.md#symptoms).

The certificate is marked as a CA (browser stores accept a self-signed anchor only in
that form) but carries **name constraints** limited to exactly its own names and
`pathlen 0`: even with the key, nothing signed by it is valid for any other host.

## Modes and actions

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
  readable inside the unit's sandbox (see [Hardening](#hardening) — `/etc/n5-fangov/` is
  the simple place). A key file readable by group or others is logged as a warning at
  start (`chmod 0600`).
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
    start alerts) and reports mode `auto (fallback from file)` — the lock turns
    warn-coloured, the panel shows an *automatic (fallback)* badge, `GET /api/tls` carries
    `"fallback": true`. The config keeps `tls = "file"` and its paths. Repair from the panel
    (upload again or *Back to auto*) or with `n5-fangov cert upload CERT KEY` /
    `cert reset`, which work offline on a broken pair too (`cert info`/`cert export` need
    a loadable one).
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
- **`off`** — plain HTTP, loopback only. A reverse proxy terminating TLS in front of
  `127.0.0.1:8010` is the alternative to `auto` ([below](#behind-a-reverse-proxy)). The
  panel then only says so; the certificate endpoints answer `409 tls is off`.

Every change from the panel is **hot-swapped**: the new certificate serves the next
handshake, open connections and the fan controller are untouched, no restart. The
listener issues no TLS session tickets, so a browser that reconnects sees the new
certificate at once instead of resuming an old session. Changes are logged as
`web: tls <regenerate|upload|reset> by <ip>` and need the same login as any other write
(`auth = "basic"`). The panel's notice lists what the server finds worth knowing about
the active certificate (`warnings` in `GET /api/tls`): an expiry within 30 days, no
SANs, and listen hosts the SAN list does not cover — under HSTS the browser will refuse
such a name, so a certificate for the LAN name should carry every name you use.

## CLI equivalents

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

## Transport and HSTS

TLS 1.2 minimum, modern cipher suites, HSTS header (`max-age=31536000`, no
`includeSubDomains`, no preload). **HSTS scope:** browsers apply it to the whole host
*name*, all ports — after one visit to `https://n5host.example.test:8010` the browser
also rewrites `http://n5host.example.test/` (port 80) to HTTPS for a year. Browsers
ignore HSTS for IP literals, so `https://192.0.2.10:8010` affects nothing else. Reach
the UI by IP, or make sure every service on that name speaks HTTPS; a reverse proxy in
front of `tls = "off"` sets its own policy (n5-fangov sends the header only on its own
TLS listener). `listen`, `auth` and `allowed_hosts` still need a restart; the
certificate does not.

## Who sees what

With `auth = "basic"` the dashboard has two faces, enforced by the server (the UI only
mirrors it):

| | Anonymous | Signed in |
|---|---|---|
| Overview | channel cards and the charts (`GET /api/state` and `/api/history` in a **reduced** form: name, pwm, sensor, temp, duty, target, rpm, mode — no hwmon path, no EC temperatures, no alert stamps, no extra sensors, no held temperature or hold) | full: plus the Sensors card, the extra-sensor chart, the CSV export, System details, recent alerts |
| About tab, version, `GET /api/openapi.json` | full | full |
| Curves, Manual, Presets (with the Schedules card), Alerts, System, Log, Compatibility, certificate panel, settings gear | hidden; the API answers 401 | full |

Nothing pops up for an anonymous visitor: the reduced Overview is the landing page, the
**Sign in** button in the header opens the form. With `auth = "none"` every visitor
counts as signed in — a Bearer header is then ignored. A script with an API token
counts as signed in for what its scope allows ([API tokens](#api-tokens)); the endpoint
table with the scope of each is on the [API page](12-api.md#endpoints).

## Sessions

Basic auth stays accepted on every protected request (CLI, curl, scripts); the cookie
session is for browsers.

- **Remember me** keeps the session for 30 days on that browser, otherwise 12 hours.
  Sessions survive a daemon restart — including the restart a config change may
  require — because they are mirrored to `/var/lib/n5-fangov/sessions.json` (0600, tokens
  stored hashed; at most 50, oldest dropped).
- **Sign out** revokes the session and clears the cookie. The Account dialog lists the
  active sessions and offers *Sign out other sessions*
  ([Account](04-dashboard.md#account)). A browser that still holds Basic credentials
  sends them with every request and counts as signed in ("via basic"); *Sign out* cannot
  clear those — close the browser or clear the site data.
- A password or user change made outside the dashboard (`n5-fangov passwd`, editing the
  file) drops every persisted session at the next start; changes made through the
  Account dialog keep the session that made them and sign every other one out.
- The cookie is `HttpOnly; SameSite=Strict` (`Secure` over TLS or with
  `behind_tls_proxy = true`).
- Sessions are for browsers and the dashboard. Scripts, Home Assistant and agents use
  an API token instead of the password ([below](#api-tokens)).

## API tokens

An API token replaces the admin password in anything that is not a browser: Home
Assistant, monitoring, scripts, a local LLM agent. It is sent as
`Authorization: Bearer n5t_...` on every request ([usage and curl examples](12-api.md#authentication)).
Tokens exist only with `auth = "basic"`; with `auth = "none"` everyone is signed in
and a Bearer header is ignored.

**Scopes** are cumulative; `read` is the default:

| Scope | Allows |
|---|---|
| `read` | `GET` on `version`, `about`, `session`, `openapi.json`, `state`, `history`, `history.csv`, `system`, `sensors`, `profiles`, `presets`, `presets/{name}`, `alerts`, `dashboard`, `schedules`, `tls` (the info only, not the certificate downloads) |
| `control` | `read` + `PUT`/`DELETE /api/override/{name}`, `POST /api/presets/{name}/apply`, `PUT /api/dashboard` |
| `admin` | everything the dashboard can do **except** token and account management |

**What no token can do:** `/api/tokens*`, `/api/account/*`, `/api/login` and
`/api/logout` answer 403 to every token, `admin` included — a leaked admin token
cannot mint new tokens, change the password or rename the user. Those need a browser
session or Basic auth. A request outside the scope answers 403
`{"error":"token scope read does not allow PUT /api/override/cpu","scope":"read","required":"control"}`.
`GET /api/alerts` is within `read`; a token caller gets `webhook_url` with query and
userinfo redacted (the full URL is for browser sessions and Basic auth only), so a
`read` token never reveals a Gotify key ([Webhook](07-alerts.md#webhook)).

- **Create** — settings gear → *Account…* → *API tokens* → *Create token…* (name,
  scope, expiry `30 d · 90 d · 1 y · never`), or from the shell
  `n5-fangov token create NAME [--scope read|control|admin] [--ttl DAYS]`
  ([CLI](05-cli.md#subcommands)). The secret — `n5t_` + 43 characters — is shown **once**;
  the daemon stores only its sha256. Names are `[A-Za-z0-9][A-Za-z0-9 ._-]{0,31}` and
  unique (409); at most 50 tokens (409). The default expiry is 90 days; *never* (0) is
  allowed and flagged with a warning at creation.
- **Storage** — `/var/lib/n5-fangov/tokens.json` (0600, atomic writes), next to the
  sessions. The settings bundle (`export`/`import`) never carries tokens; a purge or
  a reinstall removes the file and every client needs a new token
  ([Updates](09-updates.md#what-an-upgrade-can-affect)).
- **List** — the same dialog and `n5-fangov token list`: id (8 hex), name, scope,
  created, expires, last used, last address (the last two refreshed at most once a
  minute). An expired token stays in the list, marked, until it is revoked.
- **Revoke** — *Revoke* in the dialog (with confirmation) or `n5-fangov token revoke ID`;
  takes effect on the next request. Revocation is the **only** way to end a token
  early: a password change, *Sign out other sessions* and a user rename leave tokens
  valid, unlike browser sessions.
- **Rate limit** — 20 requests per second sustained, burst 40, per token; above that
  429 `{"error":"token rate limit"}`. Cookie and Basic callers are not limited this way.
- **Failures** — an unknown (revoked) or expired token is a 401 for the client and one
  journal line `web: bearer token rejected from <ip>: unknown|expired`; it counts
  towards the [login throttling](#login-throttling) of that address like a wrong
  password (one sha256, no PBKDF2 cost).
- **CSRF** — a Bearer caller does not need the `X-N5-Fangov-Csrf: 1` header
  ([below](#host-header-and-csrf)); the Host header check still applies.

## What auth covers

With `auth = "basic"`, everything under `/api/` needs credentials (cookie session,
Basic or a Bearer token within its scope) except `GET /api/version`, `/api/about`,
`/api/session`, `/api/openapi.json`, the login/logout endpoints and the **reduced**
`GET /api/state` / `/api/history` (channel temperatures, duties, RPM and modes —
[Who sees what](#who-sees-what)). Config, sensors, presets, schedules, the history CSV,
profiles, log, certificate panel and downloads, alerts, account, tokens, system and
every write are protected.

**The hash never leaves the daemon.** `GET /api/config` and the settings export show
`password_hash = "<unchanged>"`; sending that text back keeps the stored hash.

## Login throttling

Failed logins — form and Basic alike, a rejected Bearer token, and a wrong current
password in the account forms — are throttled per client address (IPv4 address or IPv6
/64): 5 free, then 250 ms doubling to 2 s, reset after 10 min or a success. They are
logged with user name (or the token verdict) and IP, but only when a credential was
actually presented; the anonymous 401 the UI gets before login is not a failure. At
most 4 delayed attempts per address are in flight at once and at most 4 password
verifications run process-wide; further ones get an immediate `429` without a hash
computation, so parallel requests cannot side-step the delay or burn CPU on PBKDF2.
Token lookups are one sha256 and never wait for the PBKDF2 slot.

## Password hashes

`n5-fangov passwd` and `setup` store salted PBKDF2-HMAC-SHA256 (210 000 iterations,
16-byte random salt) as `pbkdf2$<iter>$<salt>$<key>`. The earlier form — the plain
`sha256` hex of `user:password` (64 characters, as produced by
`printf 'admin:password' | sha256sum`) — **keeps working**; the daemon accepts both. Run
`n5-fangov passwd` once to upgrade an old hash (a restart applies it). A config file
that carries a hash is written `0600`; an existing wider mode is tightened and logged.

## Host header and CSRF

- **Host header check (DNS rebinding).** Requests are only served for IP literals,
  `localhost`, the listen host and `allowed_hosts`; anything else gets 421. `"*"`
  disables the check.
- **CSRF.** State-changing requests need the header `X-N5-Fangov-Csrf: 1`, which the UI
  always sends; a browser form or cross-site fetch cannot add it without CORS, which the
  API does not offer. This is the CSRF defence for the cookie session and for Basic
  credentials a browser has cached. **A Bearer token is exempt**: a browser cannot attach
  that header cross-site, so there is nothing to forge — scripts send the token and
  nothing else ([API](12-api.md#authentication)).

## Behind a reverse proxy

A reverse proxy (Caddy, nginx, the PVE proxy) terminating TLS in front of
`tls = "off"` on `127.0.0.1:8010`:

- It must either rewrite `Host` to the upstream (nginx does by default, Caddy:
  `header_up Host {upstream_hostport}`) or its public name must be listed in
  `[web].allowed_hosts` — otherwise `421`.
- Set `behind_tls_proxy = true` so the session cookie carries `Secure` although the
  listener speaks plain HTTP.
- HSTS is the proxy's business; n5-fangov sends the header only on its own TLS listener.
- n5-fangov does not evaluate `X-Forwarded-For` (or `Forwarded`): the client address it
  sees is the proxy's. The [login throttling](#login-throttling) buckets, the cap of 4
  in-flight delayed attempts per address, a token's `last_ip` and the addresses in the
  log all carry that one address — so one attacker behind the proxy fills the shared
  bucket and locks every proxy user into `429`. Put the proxy's own rate limiting in
  front (per real client address) and read client addresses from the proxy's log.

## Hardening

The unit runs sandboxed (`deploy/n5-fangov.service`); the list is the contract, verify
on real hardware after every change — `/sys` writes are what most sandboxes forbid.
`make verify-deploy` checks the units ([Development](11-development.md#build)).

| Setting | Effect |
|---|---|
| `NoNewPrivileges=yes`, `LockPersonality=yes`, `RestrictRealtime=yes` | no privilege escalation from the daemon |
| `ProtectSystem=strict` + `ReadWritePaths=-/etc/n5-fangov -/run/n5-fangov -/var/log/n5-fangov -/var/lib/n5-fangov -/sys/class/hwmon -/sys/devices/platform -/var/spool/postfix/maildrop -/etc/pve/notification-templates` | whole file system read-only except config/presets/tls, runtime dir, logs, state dir, the hwmon attributes (the pwm files of the supported chips live under `/sys/devices/platform/<driver>/hwmon/hwmonN`, `/sys/class/hwmon` holds the symlinks; the rest of `/sys` — sensors, DMI, disk temperatures — is read), the postfix maildrop (alerts via `mail`) and the PVE template directory (the Alerts tab's install button; verified on the reference host — the daemon may write into it but cannot create it); `-` = a missing path does not fail the start |
| `ProtectKernelTunables=no` | **must stay `no`**: the pwm files are kernel tunables |
| `ProtectHome=yes`, `PrivateTmp=yes`, `PrivateDevices=yes`, `ProtectControlGroups=yes` | no access to home, private /tmp, no physical devices in /dev, cgroups read-only |
| `RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6 AF_NETLINK`, `RestrictNamespaces=yes` | socket, TCP/HTTP(S), netlink for the interface list (`net.Interfaces()` — the fallback when the TLS certificate needs the box's addresses), nothing else |
| `MemoryDenyWriteExecute=yes`, `SystemCallArchitectures=native`, `SystemCallFilter=@system-service` | no JIT/exec tricks, native syscalls only, no module loading |
| `CapabilityBoundingSet=` (empty) | the daemon runs as uid 0 but holds no capability: it never loads modules (modules-load.d does), never chowns, and root's own files and the root-owned sysfs attributes need none |
| `UMask=0077`, `LogsDirectory=n5-fangov` (`0750`), `RuntimeDirectory=n5-fangov` (`0750`), `StateDirectory=n5-fangov` (`0700`) | files private by default; `/var/lib/n5-fangov` holds `sessions.json` and `tokens.json` (hashed session and API tokens — secrets, hence 0700), `alerts.json` and `history.json` (all 0600). `serve --state-dir DIR` / `N5FANGOV_STATE_DIR` move it; an unwritable one is logged once and the daemon runs without persistence (sessions, tokens, alerts and history in memory) |

## Alert delivery under the sandbox

Alerts run `perl -MPVE::Notify` or `mail(1)` from inside this sandbox. Verified: **PVE
notification targets of type SMTP** (the Proxmox stack talks to the mail server itself;
nothing on the local file system is written). The `mail(1)`/sendmail path on non-PVE
hosts, and a PVE *sendmail* target, hand the message to postfix's `postdrop`, which
writes into `/var/spool/postfix/maildrop` — that directory is in `ReadWritePaths`, but
it is `0730 postfix:postdrop` and `NoNewPrivileges` suppresses the setgid bit
`postdrop` relies on, so this path additionally needs `CAP_DAC_OVERRIDE`. Not verified;
if you need it, add a drop-in (`systemctl edit n5-fangov`) with
`CapabilityBoundingSet=CAP_DAC_OVERRIDE` and test with the Alerts tab's *Send test
alert* — that one is sent by the daemon from inside the sandbox and reports the
delivery error (`n5-fangov alert` or `alerts test` from a shell with the daemon stopped
run outside the sandbox and prove nothing). A failed delivery is logged
(`alert: ... failed`), the alert text is always in the journal.

Next: [API and integrations](12-api.md) · [Alerts](07-alerts.md) ·
[Troubleshooting](10-troubleshooting.md) · [Dashboard](04-dashboard.md)
