# API and integrations

What this page covers: how a script, Home Assistant or an agent talks to the daemon —
reaching the API, the three ways to authenticate, API tokens and their scopes, the
OpenAPI document, an overview of every endpoint by scope, the history and CSV
parameters, and the Home Assistant recipe (REST sensors, a preset command, an
automation). Request and response bodies are in the OpenAPI document the daemon
serves, not here.

- [Reaching the API](#reaching-the-api)
- [Authentication](#authentication)
- [Scopes](#scopes)
- [OpenAPI](#openapi)
- [Endpoints](#endpoints)
- [History and CSV](#history-and-csv)
- [Home Assistant](#home-assistant)
- [Other clients](#other-clients)

## Reaching the API

Base URL = the dashboard's address: `http://127.0.0.1:8010` (scope `local`) or
`https://n5host:8010` (`lan`, [Setup](03-setup.md)). Everything is under `/api/`;
answers are JSON, errors `{"error": "…"}` (plus `"errors": [...]` where a list
exists). The `Host` header must be an IP literal, `localhost`, the listen host or an
`allowed_hosts` entry — a client that reaches the box by another name gets 421
([Host header](08-https-security.md#host-header-and-csrf)).

The automatic certificate is self-signed. Either trust it on the client (`n5-fangov
cert export n5host.crt`, then `curl --cacert n5host.crt …`;
[The certificate](08-https-security.md#the-certificate)) or skip verification
(`curl -k`, `verify_ssl: false`) on a LAN you trust.

The CLI does not use this port: it talks over the root-only unix socket, without
credentials ([The socket](05-cli.md#the-socket)).

## Authentication

With `auth = "none"` (loopback default) nothing is needed — every caller counts as
signed in. With `auth = "basic"` there are three ways:

| Caller | Sends | For |
|---|---|---|
| Browser session | cookie `n5fangov_session` + header `X-N5-Fangov-Csrf: 1` on writes | the dashboard |
| Basic auth | `Authorization: Basic …` (`curl -u admin`) + `X-N5-Fangov-Csrf: 1` on writes | one-off shell commands with the admin password |
| **API token** | `Authorization: Bearer n5t_…` — **no CSRF header** | scripts, Home Assistant, monitoring, agents |

An API token is the intended way for anything that runs unattended: it has a scope,
an expiry and can be revoked alone, and it never carries the admin password. Create
one in the dashboard (settings gear → *Account…* → *API tokens*) or with
`n5-fangov token create NAME --scope read|control|admin [--ttl DAYS]`; the secret is
shown once. Storage, expiry, revocation, rate limit and what a token can never do:
[API tokens](08-https-security.md#api-tokens).

```
# read: the full snapshot (anonymous callers get the reduced one)
curl -H "Authorization: Bearer n5t_…" https://n5host:8010/api/state

# control: manual override and back to the curve — no CSRF header for a token
curl -X PUT -H "Authorization: Bearer n5t_…" -H "Content-Type: application/json" \
     -d '{"duty":180}' https://n5host:8010/api/override/hdd
curl -X DELETE -H "Authorization: Bearer n5t_…" https://n5host:8010/api/override/hdd

# control: apply a preset
curl -X POST -H "Authorization: Bearer n5t_…" https://n5host:8010/api/presets/n5pro-quiet/apply

# the same with Basic auth needs the CSRF header
curl -u admin -X POST -H "X-N5-Fangov-Csrf: 1" https://n5host:8010/api/presets/n5pro-quiet/apply
```

Answers a client has to expect: 401 (no or rejected credentials — an expired or
revoked token is a 401 too), 403 (scope, or a token on a session-only endpoint),
421 (Host), 429 (login throttling or the per-token rate limit `{"error":"token rate
limit"}`), 202 on a config write that needs a restart. A token that is rejected is
logged and counted like a wrong password ([Login throttling](08-https-security.md#login-throttling)).

## Scopes

Cumulative; `read` is the default when a token is created.

| Scope | Allows |
|---|---|
| `read` | `GET` on `version`, `about`, `session`, `openapi.json`, `state`, `history`, `history.csv`, `system`, `sensors`, `profiles`, `presets`, `presets/{name}`, `alerts`, `dashboard`, `schedules`, `tls` (info only, not the certificate downloads) |
| `control` | `read` + `PUT`/`DELETE /api/override/{name}`, `POST /api/presets/{name}/apply`, `PUT /api/dashboard` |
| `admin` | everything the dashboard can do **except** token and account management |

No token, `admin` included, can reach `/api/tokens*`, `/api/account/*`, `/api/login`
or `/api/logout` (403): tokens are created, listed and revoked by a browser session or
Basic auth only. Out of scope: 403
`{"error":"token scope read does not allow PUT /api/override/cpu","scope":"read","required":"control"}`.

## OpenAPI

`GET /api/openapi.json` (public, no credentials) is an OpenAPI 3.1 document rendered
from the daemon's route table — the one place every route is declared, so the document
cannot drift from the server (a test pins both directions). It carries `info.version`
(the daemon version), the security schemes `bearer`, `basic` and `cookie`, and per
operation the summary, parameters, request body schema, the response codes, `x-scope`
(`read`/`control`/`admin`/session-only) and `x-class` (`public`, `filtered`,
`protected`). Feed it to a generator, an API client or an agent:

```
curl -s https://n5host:8010/api/openapi.json | jq '.paths | keys'
```

## Endpoints

Method, path, scope and purpose; bodies and response shapes are in the OpenAPI
document. *Class* `public` needs no credentials, `filtered` answers anonymous callers
with a reduced document, `protected` needs credentials. *session* = browser session or
Basic auth only, never a token.

**No credentials**

| Method | Path | Class | Purpose |
|---|---|---|---|
| `GET` | `/api/version` | public | name, version, `prerelease`, `tls`, `auth`, the UI's validation `limits` |
| `GET` | `/api/about` | public | licence, repository, author, credits |
| `GET` | `/api/session` | public | who the caller is: `authenticated`, `mode`, `user`, `via` (`cookie`/`basic`/`bearer`/`none`), `scope` and `token_id` for a token |
| `GET` | `/api/openapi.json` | public | the OpenAPI document |
| `GET` | `/api/state` | filtered | snapshot: status, channels (temperature, duty, target, RPM, mode; signed in also `held_temp`, `hold_until`, EC and watched temperatures, alert stamps) |
| `GET` | `/api/history` | filtered | history points (`minutes`, `since`; anonymous: without `extra`) |

**Scope `read`**

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/api/history.csv` | the same points as CSV attachment (`minutes`) |
| `GET` | `/api/system` | hardware inventory with live disk temperatures |
| `GET` | `/api/sensors` | sensor catalogue with live readings and `kind` |
| `GET` | `/api/profiles` | profiles with the active one |
| `GET` | `/api/presets`, `/api/presets/{name}` | preset list, one preset's channel tables |
| `GET` | `/api/schedules` | `[[schedule]]` entries, active, next and last switch, timezone (501 without a scheduler) |
| `GET` | `/api/alerts` | transport status (full `webhook_url`), kinds with last delivery, recent alerts |
| `GET` | `/api/dashboard` | the watched sensor ids |
| `GET` | `/api/tls` | certificate mode, info, warnings, fallback flag |

**Scope `control`**

| Method | Path | Purpose |
|---|---|---|
| `PUT` | `/api/override/{name}` | manual duty (`{duty}` or `{percent}`; HDD-like channels ≥ 60) |
| `DELETE` | `/api/override/{name}` | back to the curve |
| `POST` | `/api/presets/{name}/apply` | apply a preset (merge by pwm; 202 when a restart is needed) |
| `PUT` | `/api/dashboard` | set the watched sensor ids |

**Scope `admin`**

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/api/config` | raw TOML and the parsed config (hash redacted) |
| `PUT` | `/api/config[?strict=1]` | write the config file and reload (200 / 202 / 400) |
| `GET` | `/api/config/export` | settings bundle (config + presets, never tokens) |
| `POST` | `/api/config/import` | restore a bundle |
| `PUT` | `/api/presets/{name}` | save the current channel tables as a preset |
| `POST` | `/api/presets/{name}/rename` | rename a user preset |
| `DELETE` | `/api/presets/{name}` | delete a user preset |
| `PUT` | `/api/alerts` | transport, `mail_to`, `webhook_url`, `webhook_format` |
| `POST` | `/api/alerts/test` | send a test alert through the real transport |
| `POST` | `/api/alerts/template` | install the PVE notification template |
| `GET` | `/api/log`, `/api/log/export` | log lines (`lines`), the whole current file |
| `DELETE` | `/api/log` | truncate the log file |
| `GET` | `/api/tls/cert.crt`, `/api/tls/cert.cer` | certificate download (PEM / DER) |
| `POST` | `/api/tls/regenerate`, `/api/tls/upload`, `/api/tls/reset` | certificate actions (409 with `tls = "off"`) |

**Session only** (403 for every token)

| Method | Path | Purpose |
|---|---|---|
| `POST` | `/api/login`, `/api/logout` | cookie session |
| `GET` | `/api/tokens` | token list (never the secrets) |
| `POST` | `/api/tokens` | create a token (`name`, `scope`, `ttl_days`; 201 with the secret once) |
| `DELETE` | `/api/tokens/{id}` | revoke |
| `GET` | `/api/account` | user, mode, sessions |
| `POST` | `/api/account/password`, `/api/account/user`, `/api/account/sessions/revoke` | credentials and sessions |

Body limits: 256 KiB config/import, 64 KiB certificate upload, 4 KiB for override,
login, account and token JSON (413 above). Every state-changing request from a cookie
or Basic caller needs `X-N5-Fangov-Csrf: 1`.

## History and CSV

`GET /api/history?minutes=N&since=TS` returns `[{ts, temp{}, duty{}, rpm{}, extra{}}]`
— maps by channel name, `extra` by watched sensor id (omitted when empty; anonymous
callers never get it). `minutes` is 1..10080 (7 days; default 120) and selects the
tier: up to 2 h one point per cycle, up to 24 h one-minute means, above that five-minute
means; `since` (unix seconds) returns only newer points — the dashboard's 2 h view polls
this way every 30 s. Points are kept across restarts in
`/var/lib/n5-fangov/history.json`, saved every 10 minutes and at stop
([Dashboard: Overview](04-dashboard.md#overview)).

`GET /api/history.csv?minutes=N` (scope `read`) is the same as an attachment
`n5-fangov-history-<host>-<ts>.csv`: header `ts,time,<ch>_temp,<ch>_duty,<ch>_rpm,…`
followed by one column per watched sensor id (channels in daemon order, then the ids
sorted); `time` is RFC 3339 in the host's local time, an absent value is an empty cell.

```
curl -H "Authorization: Bearer n5t_…" -o week.csv "https://n5host:8010/api/history.csv?minutes=10080"
```

## Home Assistant

Two files hold the complete recipe with documentation values:
[`openapi/home-assistant-rest.yaml`](openapi/home-assistant-rest.yaml) (sensors and the
preset command) and [`openapi/home-assistant-automation.yaml`](openapi/home-assistant-automation.yaml)
(automations). Put the token into `secrets.yaml` as
`n5_fangov_token: "Bearer n5t_…"` — a `control` token when the preset command is used;
the sensors alone need `read` or no token at all, because the reduced `/api/state`
already carries temperature, RPM and mode per channel.

**Sensors** — one `rest` resource polls `/api/state` every 30 s; each sensor picks its
channel by name from `channels[]`:

```yaml
rest:
  - resource: https://n5host:8010/api/state
    scan_interval: 30
    verify_ssl: false
    headers:
      Authorization: !secret n5_fangov_token
    sensor:
      - name: "N5 fan cpu temperature"
        unique_id: n5_fangov_cpu_temp
        value_template: "{{ (value_json.channels | selectattr('name', 'eq', 'cpu') | first).temp }}"
        unit_of_measurement: "°C"
        device_class: temperature
        state_class: measurement
      - name: "N5 fan cpu rpm"
        unique_id: n5_fangov_cpu_rpm
        value_template: "{{ (value_json.channels | selectattr('name', 'eq', 'cpu') | first).rpm }}"
        unit_of_measurement: "rpm"
        state_class: measurement
      - name: "N5 fan cpu mode"
        unique_id: n5_fangov_cpu_mode
        value_template: "{{ (value_json.channels | selectattr('name', 'eq', 'cpu') | first).mode }}"
```

Repeat the three sensors per channel (`ssd`, `hdd`, …); `rpm` is −1 on a channel
without a tachometer, `temp` −999 while the sensor is unknown — filter those in a
template if they should not reach the history.

**Preset command** — a `rest_command` that applies a preset by name; needs a `control`
token. A Bearer caller sends **no** `X-N5-Fangov-Csrf` header: that header guards
cookie and Basic callers only, a token is exempt.

```yaml
rest_command:
  n5_fangov_apply_preset:
    url: "https://n5host:8010/api/presets/{{ preset }}/apply"
    method: POST
    verify_ssl: false
    headers:
      Authorization: !secret n5_fangov_token
```

**Automation** — a plain time-of-day switch belongs into `[[schedule]]` on the box
([Schedules](06-configuration.md#schedules)); the automation is for triggers only
Home Assistant knows about:

```yaml
automation:
  - alias: "N5 fans: quiet while the media room is in use"
    triggers:
      - trigger: state
        entity_id: input_boolean.media_room_in_use
    actions:
      - action: rest_command.n5_fangov_apply_preset
        data:
          preset: "{{ 'n5pro-quiet' if trigger.to_state.state == 'on' else 'n5pro-balanced' }}"
```

**`verify_ssl: false`** skips the check of the daemon's self-signed automatic
certificate — acceptable on a LAN you trust, and the only option on Home Assistant OS,
where the container's CA store is not yours to extend. On a Core or Container install
trust the certificate instead (`n5-fangov cert export`, then the Linux recipe in
[The certificate](08-https-security.md#the-certificate)) and drop the flag; the same
holds when the box serves a certificate from a CA Home Assistant already trusts
(`tls = "file"`).

Alerts in the other direction — the daemon pushing to a Home Assistant webhook — are
the webhook transport ([Alerts](07-alerts.md#webhook)).

## Other clients

- **Monitoring** — poll `/api/state` with a `read` token; `status` is `ok`,
  `sensor-error`, `write-error` or `dry-run`, a channel `mode` other than `auto`/`manual`
  is worth an alarm. `/api/system` adds per-disk temperatures.
- **Agents** — hand them `/api/openapi.json` and a token with the smallest scope that
  does the job; `control` lets an agent apply presets and set overrides, never edit
  curves or credentials.
- **Shell on the box itself** — `n5-fangov status|set|auto|…` over the socket needs no
  token ([CLI](05-cli.md)).

Next: [HTTPS and security](08-https-security.md) · [Configuration](06-configuration.md) ·
[Alerts](07-alerts.md)
