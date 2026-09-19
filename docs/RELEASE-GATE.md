# Release gate — from a release candidate to a release

Decided 2026-09-16 by the operator: a version drops its `-rc` suffix only after a
**manual installation test performed by the operator, on the real host, following the
documentation alone** — no chat, no shortcuts. If a step needs knowledge that is not in
`docs/`, the documentation is the defect, not the tester. Applies to every 0.3.x release
and to 0.4.0 (the public one).

## Preconditions (prepared by the maintainer)

1. `docs/` split is done (README = landing page, one page per topic, kernel-driver page).
2. Repository hardening merged, history rewritten (done). The repository is public since
   0.4.0; the release assets (binary, `.sha256`, `.deb`) and the source of the tag come
   from the public URLs on the install page ([release path](01-install.md#from-a-github-release)).
   Before 0.4.0 they were downloaded on a signed-in client and copied to the host with
   `scp` — the host has no `gh` and gets none.
3. The release candidate is tagged; the release workflow produced binary, sha256 and
   `.deb`; CI is green.
4. The host's current config is backed up outside `/etc/n5-fangov` (the test starts from
   a clean state).

## The test (operator, on the PVE host, as root)

| # | Step | Expected | Pass |
|---|---|---|---|
| 1 | `./deploy/uninstall.sh --purge` of the running version (config backed up first) | fans fall back to the safe state (CPU/SSD EC automatic, HDD fixed stop duty); `/etc/n5-fangov` gone | ☐ |
| 2 | Kernel driver page: verify `dkms status`, `sensors`, module options — reinstall only if the page says so | page answers every question that comes up | ☐ |
| 3 | Install page, **release path**: clone of the tag, binary + sha256 by `curl`, verify, `./deploy/install.sh` | installer output matches the page; `n5-fangov version` prints the release | ☐ |
| 4 | Install page, **package path** (second run): the `.deb` release asset, `apt install ./n5-fangov_<debver>_amd64.deb` | same result; `dpkg -s n5-fangov` ok; the postinst line and the apt-hook line match the page | ☐ |
| 5 | Setup page: `n5-fangov setup` interactively (`lan`, new user + password) | config written, unit started, `n5-fangov check` all `[ok]` | ☐ |
| 6 | Dashboard page: open `https://<host>:8010`, trust the certificate per the HTTPS page (Settings → Certificate → *Download*, *How to trust*), sign in; **Fans**: apply a built-in preset from the Presets row, switch *Manual override* on for one channel, *Set* another duty, switch it off; *New preset…* → a user preset from *the daemon (running curves)*; edit a curve → *Save as preset…* (the editor stays dirty) → *Apply to daemon*; collapse the sidebar with the toggle and with `[`, expand it from the rail (the icon under the logo); **Schedules**: add a window with that preset, *Save*, check the status card, remove it, *Save*; **Alerts**: *Send test alert*; **Settings**: *Export settings*, *Create token…* and *Revoke* it; on a phone or a narrow window: bottom bar, *More*, the Fans channel selector | every step as described by the Dashboard page alone; the preset badge (every matching preset), the *Active set* line, the mode badge and the status card follow each action; test alert arrives by mail | ☐ |
| 7 | Kernel driver page: `apt install --reinstall` of any small installed package (e.g. `lm-sensors`) — the hook runs after every dpkg run, a kernel reinstall is not needed | the hook line `n5-fangov: fan driver module present for N kernel(s): …` visible in the apt output ([the kernel-update gate](02-kernel-driver.md#the-kernel-update-gate)) | ☐ |
| 8 | Reboot the host | daemon active after boot, DKMS module loaded, curves in effect (the pending reboot proof) | ☐ |
| 9 | Troubleshooting page: provoke one listed symptom (e.g. stop the module → `check` fails) and follow the page | the page's fix works | ☐ |
| 10 | Uninstall page: `uninstall.sh` (keep config), reinstall, config still in effect | as described | ☐ |

Result: every row ☐ → ✔ and no note of the form "had to look elsewhere" → tag the
release. Otherwise: fix the documentation or the code, new `-rc`, repeat the failed rows.

## Notes for the tester

- Report every row in one of three forms: ✔, "had to look elsewhere at <page>" (the
  page that was missing the fact, or "chat"), or the error output verbatim.
- Step 1 leaves the HDD fans at the fixed stop duty until step 5 — minutes, not hours;
  do it outside scrub and backup windows.
- Keep a copy of the old binary (`/root/n5-fangov-<ver>.bak`) for a rollback per the
  Updates page.
- The hwmon number (`hwmonN` in the setup dialog, `status` and `check`) may change after
  the reboot in step 8; that is not a deviation
  ([Verify](02-kernel-driver.md#verify)).
- A preset applied in step 6 rewrites the `[[channel]]` tables, the schedule test the
  `[[schedule]]` tables of the config file; the user preset is a file in
  `/etc/n5-fangov/presets/`. Afterwards either
  restore the config backed up in precondition 4 or keep the new curves deliberately —
  step 10 checks "config still in effect" against whichever you chose.
- Record deviations as GitHub issues with the page name and the step number.

## Result 2026-09-18, v0.3.1-rc1

All 10 rows passed functionally, including the reboot proof in row 8. Every deviation
was a documentation finding (no page for the private download path, no reference
outputs, the certificate recipe too short, the kernel gate exercised by the wrong
command, a placeholder copied with its angle brackets); they were fixed in 0.3.1-rc2 and rc3 and re-verified; 0.3.1 was tagged from rc3.
Rows 3, 4, 6 (certificate recipe) and 10 are re-run on rc2 before the tag drops the
suffix.

## Result 2026-09-18/19, v0.4.0-rc1

Rows 1–7, 9 and 10 passed; row 8 (reboot) skipped — nothing in the boot chain changed
since 0.3.1, whose reboot proof stands. Findings G1–G11 (documentation, dashboard polish,
one preset data-format defect) are fixed in 0.4.0-rc2 (CHANGELOG); rows 3, 6 and 9 are
re-run on rc2 before the tag drops the suffix.

## Result 2026-09-19, v0.4.0-rc2 (re-run of rows 3, 6 and 9)

Row 6 came back with two dashboard findings: the sidebar toggle "must be integrated more
nicely and be unmistakable; like modern layouts", and the preset badge is ambiguous when a
user preset shares a channel with a built-in (the badge named one of them). Both are
fixed in 0.4.0-rc3 (CHANGELOG: the panel-left toggle pattern with the `[` shortcut and the
header icon; badges that list every matching preset plus the *Active set* line; *Save as
preset…* from the action bar). The Fans control logic is now covered by the scenario
matrix `tools/fans-matrix.mjs` (18 scenarios against the mock, all PASS). Row 6 is re-run
on rc3 before the tag drops the suffix; rows 3 and 9 passed on rc2 and stand.

## Result 2026-09-19, v0.4.0

Tagged from 0.4.0-rc5. Rows 1–7, 9 and 10 passed on rc1 (2026-09-18); row 8 was skipped
because nothing in the boot chain changed since 0.3.1, whose reboot proof stands. rc2 to
rc5 (2026-09-19) carried the operator's dashboard findings from the row 6 re-tests —
sidebar toggle placement (brand row, then the panel-left pattern, then sidebar-only),
preset badges that list every match plus the *Active set* line, *Save as preset…* — each
re-tested on the host; rows 3 and 9 passed on rc2 and stand. 0.4.0 is rc5 without the
suffix. Open from this gate: the public `curl` path of row 3 has run only in the
private-phase form (assets copied to the host); it is exercised once after publication
against the released assets.

## Update re-test (rc iterations, from 0.4.1-rc1)

For an rc that changes daemon behaviour without touching the boot chain or the install
path, the full ten rows are not needed: a **package update without purge** plus the
rows the change touches. Operator, on the PVE host, as root; the old binary kept as
`/root/n5-fangov-<ver>.bak` for a rollback per the Updates page.

| # | Step | Expected | Pass |
|---|---|---|---|
| U1 | `apt install ./n5-fangov_<debver>_amd64.deb` over the running version (no remove, no purge) | postinst restarts the unit; `n5-fangov version` prints the rc; config, presets, `tokens.json`, `history.json` untouched (`ls -l /var/lib/n5-fangov`); dashboard sign-in still works with the old cookie or after a fresh sign-in | ☐ |
| U2 | `n5-fangov check` with the HDD channel's `critical` raised above 65 in the config (e.g. `critical = 70`), then back | the advisory line `channel hdd: critical 70 above the built-in ceiling 65 — the ceiling acts first`, check still says `all good`; without it the line is gone. The line `[ok  ] emergency hook   /etc/n5-fangov/emergency.sh (absent)` is there in both runs (`emergency` is still `false`) | ☐ |
| U3 | **Ceiling, live:** set `ceiling = 30` on the HDD channel (`[[channel]] name = "hdd"`) — edit the file, then Fans → *Revert* (re-reads the file, `ceiling` is carried) → *Apply to daemon*; or `systemctl restart n5-fangov` | within one cycle the channel runs at 255 in mode `critical`, `ceiling_hit: true` in `GET /api/state`, the Alerts page shows kind `ceiling` with the reading and "ceiling 30C"; the curve editor draws the `ceiling` line at 30 °C; the tile colours by the ceiling | ☐ |
| U4 | **Emergency hook, logger form:** with U3 still in effect install the template — `install -m 0750 -o root -g root /usr/share/doc/n5-fangov/examples/emergency.example.sh /etc/n5-fangov/emergency.sh` (the `logger` line is active, the poweroff line stays commented) —, `n5-fangov check`, then set `[daemon] emergency = true` and `emergency_cycles = 2`, reload | `check` prints `[ok  ] emergency hook   /etc/n5-fangov/emergency.sh (ok)`; after 6 cycles in the ceiling state (3 × 2 — the fan spins, so the 3N rule fires) the daemon log shows `hdd: emergency action after 6 cycles at the ceiling (cooling ineffective): running /etc/n5-fangov/emergency.sh`, `journalctl -t n5-fangov-emergency` shows `channel hdd (drivetemp:max) at NN.N C, ceiling 30 C, … 6 cycles at the ceiling`, the Alerts page kind `emergency` with `hook exited 0` — this proves that the sandbox lets the hook reach the journal. Then `chmod 0777 /etc/n5-fangov/emergency.sh` and `n5-fangov check`: `[warn] emergency hook   … (refused: world-writable (mode 0777)); emergency = true but nothing would run`; `chmod 0750` back | ☐ |
| U5 | Values back (`ceiling` key removed, `emergency = false`, `rm /etc/n5-fangov/emergency.sh` — or keep the hook installed if it is wanted), `systemctl restart n5-fangov` | channel back on its curve within a few cycles, `ceiling` reads 65, `ceiling_hit` false, no further alerts; `n5-fangov check` all `[ok]` (the hook line reads `(absent)` or `(ok)`) | ☐ |

Not part of the re-test: the tokens backup on `apt purge` / `uninstall.sh --purge`
(no purge on the host in an rc iteration — covered by the deploy tests only) and the
`systemctl poweroff` form of the emergency hook (to be verified on the host
deliberately, see the configuration page). Also not on the host: `PUT /api/config` with
`emergency_command = "…"` — the daemon answers with the unknown-key warning and nothing
runs (config tests cover the parser; the deploy tests that no installer touches
`/etc/n5-fangov/emergency.sh`).

## Result 2026-09-19, v0.4.1

U1–U5 passed on 0.4.1-rc1 on the reference host (package update over 0.4.0; `check`
advisory with `critical = 70`; `ceiling = 30` on the HDD channel → 255, mode `critical`,
alert `ceiling` within one cycle; emergency hook in the `logger` form fired after 6 cycles
from inside the unit's sandbox — `logger` reached the journal, hook exit 0; values
reverted). Two findings, fixed in 0.4.1: the transport log line was identical to the
daemon's raise line, so every daemon alert stood twice in the log since 0.2 (now
`ALERT[kind] sent via <transport>: …`); row U3 named the reload path imprecisely (now the
*Revert* → *Apply to daemon* path or a restart). 0.4.1 is rc1 plus these two fixes. Still
not exercised on the host: the `systemctl poweroff` form of the hook and the tokens
backup on purge.
