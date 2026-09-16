# Release gate — from a release candidate to a release

Decided 2026-09-16 by the operator: a version drops its `-rc` suffix only after a
**manual installation test performed by the operator, on the real host, following the
documentation alone** — no chat, no shortcuts. If a step needs knowledge that is not in
`docs/`, the documentation is the defect, not the tester. Applies to every 0.3.x release
and to 0.4.0 (the public one).

## Preconditions (prepared by the maintainer)

1. `docs/` split is done (README = landing page, one page per topic, kernel-driver page).
2. Repository hardening merged, history rewritten (done). While the repository is private,
   the tester downloads the assets with `gh release download <tag>` (signed-in `gh`); the
   public curl path from the install page is re-run once at 0.4.0.
3. The release candidate is tagged; the release workflow produced binary, sha256 and
   `.deb`; CI is green.
4. The host's current config is backed up outside `/etc/n5-fangov` (the test starts from
   a clean state).

## The test (operator, on the PVE host, as root)

| # | Step | Expected | Pass |
|---|---|---|---|
| 1 | `./deploy/uninstall.sh --purge` of the running version (config backed up first) | fans fall back to the safe state (CPU/SSD EC automatic, HDD fixed stop duty); `/etc/n5-fangov` gone | ☐ |
| 2 | Kernel driver page: verify `dkms status`, `sensors`, module options — reinstall only if the page says so | page answers every question that comes up | ☐ |
| 3 | Install page, **release path**: clone the tag, download binary + sha256, verify, `./deploy/install.sh` | installer output matches the page; `n5-fangov version` prints the release | ☐ |
| 4 | Install page, **package path** (second run): `apt install ./n5-fangov_<ver>_amd64.deb` | same result; `dpkg -s n5-fangov` ok | ☐ |
| 5 | Setup page: `n5-fangov setup` interactively (`lan`, new user + password) | config written, unit started, `n5-fangov check` all `[ok]` | ☐ |
| 6 | Dashboard page: open `https://<host>:8010`, trust the certificate per the HTTPS page, sign in, apply a built-in preset, set and clear a manual override, send a test alert | every step as described; test alert arrives by mail | ☐ |
| 7 | Updates page: `apt full-upgrade` (or `apt install --reinstall` of the kernel) → the apt hook reports the DKMS state | hook line visible in the apt output | ☐ |
| 8 | Reboot the host | daemon active after boot, DKMS module loaded, curves in effect (the pending reboot proof) | ☐ |
| 9 | Troubleshooting page: provoke one listed symptom (e.g. stop the module → `check` fails) and follow the page | the page's fix works | ☐ |
| 10 | Uninstall page: `uninstall.sh` (keep config), reinstall, config still in effect | as described | ☐ |

Result: every row ☐ → ✔ and no note of the form "had to look elsewhere" → tag the
release. Otherwise: fix the documentation or the code, new `-rc`, repeat the failed rows.

## Notes for the tester

- Step 1 leaves the HDD fans at the fixed stop duty until step 5 — minutes, not hours;
  do it outside scrub and backup windows.
- Keep a copy of the old binary (`/root/n5-fangov-<ver>.bak`) for a rollback per the
  Updates page.
- Record deviations as GitHub issues with the page name and the step number.
