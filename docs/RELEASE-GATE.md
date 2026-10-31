# Release gate — from a release candidate to a release

A version drops its `-rc` suffix only after a **manual installation test by the
operator, on the real host, following the documentation alone**: no chat, no
shortcuts. If a step needs knowledge that is not in `docs/`, the documentation is the
defect, not the tester.

## Two components

n5-fangov and the driver package `minisforum-n5-it5571-dkms` (repository
[`minisforum-n5pro-fan-proxmox`](https://github.com/SirRenix/minisforum-n5pro-fan-proxmox))
are released through one gate: neither drops its `-rc` / `~rc` until both pass on the
reference host.

| Component | Release candidate | Release | Before the host test |
|---|---|---|---|
| n5-fangov | `v0.5.0-rc2` (Debian `0.5.0~rc2`) | `v0.5.0` | CI green, `go test ./...` incl. the replay test |
| Driver package | `v0.2.1-sr1-rc2` (Debian `0.2.1+sr1~rc2-1`) | `v0.2.1-sr1` (`0.2.1+sr1-1`) | delta-review report of the shipped source that meets the exit criterion of `docs/DRIVER-REVIEW-CHECKLIST.md` §6; `packaging/test/test-deb.sh` PASS; DKMS build for the running and the fallback kernel |

Driver stability is part of the gate (driver repository `docs/TESTSPEC.md` §5a):

- **F8a**, maintenance window, operator at the device: load/unload loop 50× with
  parallel reads and writes, `kill -9` of n5-fangov, one cold and one warm boot; kernel
  log and fan state after every step.
- **F8b**, 7 days of real operation with the watcher (`tools/validation-watch/`):
  run 1 = baseline with the unpatched driver (from 2026-10-08), run 2 = 7 days with the
  release candidate; the result compares both per criterion K1–K7. Wording of the
  result: "no error observed in 7 days under these conditions", never "error-free".
- **TESTSPEC** T0, T1, T6 and T2/T3 (3 runs per channel) with `n5-fangov validate`, or with
  `n5-ectest` (bundle records it in `spec_deviations`, see below); the bundles go to
  `befunde/runs/`, the hardware status to `befunde/RESULTS.md`, then
  `tools/gen-hwstatus.sh` and a new n5-fangov build.

## Preconditions (maintainer)

1. The release candidate is tagged; the release workflow produced binary, sha256 and
   `.deb`; CI is green. The assets and the source of the tag come from the public URLs
   on the install page ([release path](01-install.md#from-a-github-release)). The host
   has no `gh` and gets none.
2. The host's current config is backed up outside `/etc/n5-fangov`. The test starts
   from a clean state.

## The test (operator, on the PVE host, as root)

| # | Step | Expected | Pass |
|---|---|---|---|
| 1 | `./deploy/uninstall.sh --purge` of the running version (config backed up first) | fans fall back to the safe state (CPU/SSD EC automatic, HDD fixed stop duty); `/etc/n5-fangov` gone | ☐ |
| 2 | Kernel driver page: verify `dkms status`, `sensors`, module options; reinstall only if the page says so | the page answers every question that comes up | ☐ |
| 3 | Install page, **release path**: clone of the tag, binary + sha256 by `curl`, verify, `./deploy/install.sh` | installer output matches the page; `n5-fangov version` prints the release | ☐ |
| 4 | Install page, **package path** (second run): the `.deb` release asset, `apt install ./n5-fangov_<debver>_amd64.deb` | same result; `dpkg -s n5-fangov` ok; the postinst line and the apt-hook line match the page | ☐ |
| 5 | Setup page: `n5-fangov setup` interactively (`lan`, new user + password) | config written, unit started, `n5-fangov check` all `[ok]` except the `hardware status` warning on a board below `tested` | ☐ |
| 6 | Dashboard page: open `https://<host>:8010`, trust the certificate per the HTTPS page (Settings → Certificate → *Download*, *How to trust*), sign in. **Fans**: apply a built-in preset from the Presets row, switch *Manual override* on for one channel, *Set* another duty, switch it off; *New preset…* → a user preset from *the daemon (running curves)*; edit a curve → *Save as preset…* (the editor stays dirty) → *Apply to daemon*; collapse the sidebar with the toggle and with `[`, expand it from the rail. **Schedules**: add a window with that preset, *Save*, check the status card, remove it, *Save*. **Alerts**: *Send test alert*. **Settings**: *Export settings*, *Create token…* and *Revoke* it. On a phone or a narrow window: bottom bar, *More*, the Fans channel selector | every step as described by the Dashboard page alone; the preset badge, the *Active set* line, the mode badge and the status card follow each action; the test alert arrives by mail | ☐ |
| 7 | Kernel driver page: `apt install --reinstall` of any small installed package (e.g. `lm-sensors`); the hook runs after every dpkg run, a kernel reinstall is not needed | the hook line `n5-fangov: fan driver module present for N kernel(s): …` in the apt output ([kernel-update gate](02-kernel-driver.md#the-kernel-update-gate)) | ☐ |
| 8 | Reboot the host | daemon active after boot, DKMS module loaded, curves in effect | ☐ |
| 9 | Troubleshooting page: provoke one listed symptom (e.g. stop the module → `check` fails) and follow the page | the page's fix works | ☐ |
| 10 | Uninstall page: `uninstall.sh` (keep config), reinstall, config still in effect | as described | ☐ |

Result: every row ☐ → ✔ and no note of the form "had to look elsewhere" → tag the
release. Otherwise: fix the documentation or the code, new `-rc`, repeat the failed rows.

## Notes for the tester

- Report every row in one of three forms: ✔, "had to look elsewhere at <page>" (the
  page that was missing the fact, or "chat"), or the error output verbatim.
- Step 1 leaves the HDD fans at the fixed stop duty until step 5: minutes, not hours;
  do it outside scrub and backup windows.
- Keep a copy of the old binary (`/root/n5-fangov-<ver>.bak`) for a rollback per the
  Updates page.
- The hwmon number (`hwmonN` in the setup dialog, `status` and `check`) may change
  after the reboot in step 8; that is not a deviation ([Verify](02-kernel-driver.md#verify)).
- A preset applied in step 6 rewrites the `[[channel]]` tables, the schedule test the
  `[[schedule]]` tables of the config file; the user preset is a file in
  `/etc/n5-fangov/presets/`. Afterwards either restore the config backed up in the
  preconditions or keep the new curves deliberately; step 10 checks "config still in
  effect" against whichever you chose.
- Record deviations as GitHub issues with the page name and the step number.

## Update re-test (rc iterations)

For an rc that changes daemon behaviour without touching the boot chain or the install
path, the full ten rows are not needed: a **package update without purge**, the rows
the change touches, and the closing rows. Operator, on the PVE host, as root; the old
binary kept as `/root/n5-fangov-<ver>.bak` for a rollback per the Updates page.

### Every update

| # | Step | Expected | Pass |
|---|---|---|---|
| A1 | `cp -p /etc/n5-fangov/config.toml /root/config.toml.pre-<rc>.bak`, then `apt install ./n5-fangov_<debver>_amd64.deb` over the running version (no remove, no purge) | postinst restarts the unit; `n5-fangov version` prints the rc; config, presets, `tokens.json`, `history.json` untouched (`ls -l /etc/n5-fangov /var/lib/n5-fangov`); dashboard sign-in works with the old cookie or after a fresh sign-in | ☐ |
| Z1 | After the release rows: `n5-fangov check`, `n5-fangov status`, `n5-fangov curve` | `check: all good`, or only the `hardware status` warning on a board below `tested`; every channel `auto` (or the override you left on purpose); `curve` prints no `warning:` line and shows the live config (`hysteresis` / `min_on` where set) | ☐ |
| Z2 | `journalctl -u n5-fangov --since "<time of A1>" -p warning` | nothing but lines the release rows provoked on purpose | ☐ |
| Z3 | `diff /root/config.toml.pre-<rc>.bak /etc/n5-fangov/config.toml` | no difference, unless a row changed the config on purpose and you reverted it by hand | ☐ |

### Rows for 0.4.2

| # | Step | Expected | Pass |
|---|---|---|---|
| U1 | **Setup keeps**, on a copy: `cp /etc/n5-fangov/config.toml /root/gate.toml`, a throw-away password in `/root/gate-pw` (mode 0600), `n5-fangov setup --config /root/gate.toml --yes --listen lan --user admin --password-file /root/gate-pw` | `backup: /root/gate.toml.bak-…`, `kept:` lines for what the live config sets (on the reference host: `[dashboard] sensors`, `channel hdd: hysteresis 2, min_on 1m0s`, and `[alert] …` if the transport is not `auto`), `written: /root/gate.toml`; `n5-fangov check --config /root/gate.toml` all `[ok]`; the live daemon is not touched | ☐ |
| U2 | Same command with `--fresh` added | no `kept:` line; `grep -c hysteresis /root/gate.toml` prints `0`. Afterwards delete `/root/gate.toml*` and `/root/gate-pw` | ☐ |
| U3 | **Check summary**: `cp /etc/n5-fangov/config.toml /root/gate.toml; echo '[web' >> /root/gate.toml; n5-fangov check --config /root/gate.toml; echo $?` | the `[warn] config … syntax error` line, last line `check: passed with N warning(s), serve starts — read the [warn] lines`, exit `0`; `n5-fangov check` on the live file still ends with `check: all good`. Afterwards delete `/root/gate.toml` | ☐ |
| U4 | **Dashboard, navigation**: Settings → Navigation *icon rail*; drag the browser window across 1100 px width both ways | the rail keeps its height and position at the crossing; above 1100 px the preference applies, below it the rail is forced | ☐ |
| U5 | **Dashboard, schedules**: add a window, *Save*; compare the status card's *next* and *last switch* with its clock; remove the window, *Save* | *next* / *last switch* read in the same zone as the clock; after the removal the card shows no next switch | ☐ |
| U6 | **Dashboard, look**: Overview, Fans, Schedules, Alerts, Settings, About in dark and light, wide window and phone width | nothing looks different from 0.4.1 (0.4.2 only removed unused CSS) | ☐ |

### Rows for 0.5.0 (with driver `0.2.1+sr1~rc2`)

Maintenance window, operator at the device. Order: driver first, then n5-fangov.

| # | Step | Expected | Pass |
|---|---|---|---|
| V1 | `systemctl stop n5-fangov`; `apt install ./minisforum-n5-it5571-dkms_0.2.1+sr1~rc2-1_all.deb`; `modprobe -r minisforum_n5_it5571; modprobe minisforum_n5_it5571` | `n5-fangov identify`: driver `0.2.1+sr1~rc2`, srcversion of the build protocol, `write-enabled`, hardware status `experimental` | ☐ |
| V2 | A1 with the n5-fangov `.deb`; then `n5-fangov check` | `[warn] hardware status experimental (N5 PRO / F8NAA / BIOS 1.05 / EC 0.14 …`, `[ok] driver … 0.2.1+sr1~rc1 …, write-enabled`, nothing else above `ok` | ☐ |
| V3 | `n5-fangov check --after-update` | `fan driver module present for N kernel(s), version 0.2.1+sr1~rc2: …` | ☐ |
| V4 | **Gate (a)**: stop the daemon, `modprobe -r minisforum_n5_it5571; modprobe minisforum_n5_it5571 experimental_write=0`, `n5-fangov check` with `profile = "auto"` in a copy of the config; reload with the package options | `[FAIL] profile … driver loaded read-only … experimental_write=N`; `systemctl start n5-fangov` fails and an alert `driver` arrives; after the reload it starts again | ☐ |
| V5 | `n5-fangov validate --dry-run pwm3 --out /root/runs` | bundle `…-validate-pwm3-dryrun/` with five files, `sha256sum -c SHA256SUMS` ok | ☐ |
| V6 | Daemon stopped, module with `debug_regs=1`: `n5-fangov validate pwm3 --tests T0,T6,T1 --boot warm --out /root/runs` at the console | T0 PASS, T6 reported, T1 PASS or a FAIL that names the step; every touched channel restored (`events.log`: `restore pwm3 ok`) | ☐ |
| V7 | Module reloaded without `debug_regs`, `systemctl start n5-fangov`; `journalctl -u n5-fangov --since "<V1>" -p warning` | only the `hardware status` warning; no `driver read error` | ☐ |

With n5-ectest instead of V5/V6 (it has no `--tests`): `n5-ectest map --dry-run --out
/root/runs` for V5; for V6 `n5-ectest snapshot v6 --boot warm --out /root/runs` (T6
reported) and `n5-ectest map pwm3 --boot warm --out /root/runs` at the console
(T0, T1), then `n5-ectest report` on both bundles. Same expectations.

T2/T3 (3 runs per channel), F8a and the F8b run 2 belong to the same window series
([Two components](#two-components)); their results go to the driver repository, not to
this table.

Not part of the re-test: the `systemctl poweroff` form of the emergency hook (operator
decision 2026-09-24: documented, not executed on the reference host; see the
configuration page); the tokens backup on `apt purge` / `uninstall.sh --purge` (covered
by the deploy tests).

## Results

| Date | Version | Outcome |
|---|---|---|
| 2026-09-18 | v0.3.1-rc1 | all 10 rows passed, including the reboot proof; every deviation was a documentation finding, fixed in rc2/rc3 and re-verified; 0.3.1 tagged from rc3 |
| 2026-09-18/19 | v0.4.0-rc1 … rc5 | rows 1–7, 9, 10 passed on rc1; row 8 skipped (boot chain unchanged since 0.3.1); rc2–rc5 carried the dashboard findings from the row 6 re-tests, each re-tested on the host; 0.4.0 tagged from rc5 |
| 2026-09-19 | v0.4.1-rc1 | U1–U5 passed (package update over 0.4.0; `check` advisory; `ceiling = 30` → 255, mode `critical`, alert `ceiling`; emergency hook in the `logger` form fired from inside the sandbox, exit 0); two findings fixed in 0.4.1 (see CHANGELOG) |
| 2026-09-25 | v0.4.2 (rc1 build) | A1, U1–U6, Z1–Z3 passed (package update over 0.4.1; `setup` keeps on a copy of the live config, `--fresh` keeps nothing; `check` summary with warnings, exit 0; rail, Schedules card and pages in the dashboard); tagged as 0.4.2 without a published rc |

Still not exercised on the host: the public `curl` path of row 3 against the released
assets, the `systemctl poweroff` form of the hook, the tokens backup on purge.
