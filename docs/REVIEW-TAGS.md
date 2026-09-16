# Review tags — legend

Up to `v0.3.0-rc1` the code comments and some test names carried the tags of the review
rounds that found a problem (`H1`…`H4`, `M1`…`M7`, `L1`…`L9`, `R-M1`…`R-M3`,
`R-L4`…`R-L11`, `R-U8`). Since 0.3.1 the comments keep the *reason* and this page keeps
the tags. Every review round numbered its findings from 1, so a tag alone is ambiguous —
**package + tag** (with the version of the release that lists the round in
[CHANGELOG.md](../CHANGELOG.md)) identifies a finding. The rounds:

| Round | Tags | Release |
|---|---|---|
| Safety review of the regulator (v0.1) | `H1` `H2` `M1`–`M4` `L1`–`L7` | 0.1.0 |
| Security review of the API (v0.1) | `H1`–`H3` `M1`–`M4` `L1`–`L6` | 0.1.0 |
| Security review of v0.2 (auth, log file, sandbox, bundle, hashes) | `H1`–`H4` `M1`–`M7` `L1`–`L9` | 0.2.0 |
| Certificate review (v0.2.1) | `M1`–`M5` `L1`–`L9` | 0.2.1 |
| Two adversarial reviews of the 0.3 dashboard | `R-M1`–`R-M3` `R-L4`–`R-L11` `R-U8` | 0.3.0-beta.1 |
| Acceptance audit ([docs/AUDIT.md](AUDIT.md)) | `M1` `M2` `L3`–`L7` | 0.3.0-rc1 |

The version column below names the release whose changelog entry lists the round; where
the original comment did not name the round, the assignment follows the content of the
finding.

## internal/control

| Tag | Package | Finding | Fixed where | Version |
|---|---|---|---|---|
| `H1` | control | On the N5 Pro the daemon must manage pwm1..3 even when the config lacks a channel | `SanitizeChannels` (adds the missing N5 Pro channels); `safety_test.go` `TestN5ProMissingChannelsAdded` | 0.1.0 |
| `H2` | control | A write that keeps failing with a stable target must still be retried and reach the failsafe; the duty is unknown (`-1`) after a failed write | `writePhase` (cur = -1, rewritten next cycle); `safety_test.go` `TestPersistentWriteFailureStableTarget`, `controller_test.go` `TestWriteErrorFailsafe` | 0.1.0 |
| `M1` | control | `stop = "auto"` on N5 Pro pwm3 is forced to 140 (the EC does not regulate pwm3 after a write); sensor-aware stop default in config | `SanitizeChannels`; `config.DefaultStop`; `safety_test.go` `TestN5ProPwm3AutoForced`, `config_test.go` `TestStopParsing` | 0.1.0 |
| `M2` | control | When the failsafe itself cannot write any channel for 6 cycles, `Run` returns `ErrDeviceLost` so systemd restarts with a fresh detection | `noteFailsafe`; `safety_test.go` `TestDeviceLostEndsRun` | 0.1.0 |
| `M3` | control | The frozen-sensor (stale) check runs only when the first channel's sensor is `k10temp`; `stale_cycles` below 6 falls back to the default | `readSensors` (`staleSensor`); `config` `stale_cycles` bounds; `safety_test.go` `TestStaleOnlyForK10temp`, `config_test.go` `TestLowerBounds` | 0.1.0 |
| `M4` | control | A panic in the loop still ends in SafeStop and `Run` returns an error | `Run` (recover + `Stop`); `safety_test.go` `TestPanicInLoopStillSafeStops` | 0.1.0 |
| `L1` | control | `Stop` concurrent with a cycle: after `Stop` the loop writes nothing | `writePhase` (`hwMu`, `stopped`); `safety_test.go` `TestStopBlocksFurtherWrites` | 0.1.0 |
| `L2` | control | The initial duty comes from the device; stalls count only after the daemon's first successful write | `New` (reads pwm), `checkStall` (`ch.written`); `safety_test.go` `TestInitialDutyAndStallGate` | 0.1.0 |
| `L3` | control | A tachometer read error is logged once, not every cycle | `readRPMs` (`logOnce`); `safety_test.go` `TestTachErrorLoggedOnce` | 0.1.0 |
| `L4` | control | The manual override minimum (60) applies to channels with a fixed stop duty or N5 Pro pwm3 | `SetOverride` / `hddLike`; `safety_test.go` `TestOverrideMinimumKey` | 0.1.0 |
| `L6` | control | `alert_cooldown` below 60 s falls back to the default | `config` `alert_cooldown` bounds; `config_test.go` `TestLowerBounds` | 0.1.0 |
| `L7` | control | The re-assert of manual mode after external interference is logged once | `write` (`logOnce`); `controller_test.go` `TestPeriodicRewrite` | 0.1.0 |

## internal/web

| Tag | Package | Finding | Fixed where | Version |
|---|---|---|---|---|
| `M1` | web | DNS rebinding: a foreign `Host` header is refused (421); IP literals, `localhost` and the configured hosts pass | `guard`, `hostAllowed`; `web_test.go` `TestHostHeader` | 0.1.0 |
| `M2` | web | `PUT /api/config` validates before it saves: a syntax error never reaches the file | `putConfig`; `web_test.go` `TestConfigValidateBeforeSave` | 0.1.0 |
| `L1` | web | The preset name rule of the API equals the config rule (`^[a-z0-9_-]{1,64}$`) | `presetName`; `web_test.go` `TestPresets` | 0.1.0 |
| `L3` | web | Oversized bodies answer 413 on every body endpoint | `isTooLarge`, `http.MaxBytesReader`; `web_test.go` `TestBodyTooLarge` | 0.1.0 |
| `L4` | web | The override response reports what the channel is doing now (critical/stall are not "manual") | `putOverride`; `web_test.go` `TestOverrideModeFromSnapshot` | 0.1.0 |
| `H1` | web | The password hash never leaves the daemon: redacted in every output, restored on PUT; the visibility model refuses protected reads anonymously | `RedactRaw`, `RestoreHash`; `web_test.go` `TestConfigHashRedaction`, `TestRedactRawForms`, `TestProtectedReadsNeedAuth` | 0.2.0 |
| `H3` | web | A listener reachable from the network without basic auth is logged loudly (also under TLS) | `Serve`, `ServeTLSStore`; `web_test.go` `TestServeWarnsNonLoopbackWithoutAuth`, `tls_test.go` `TestServeTLSWarnsNonLoopbackWithoutAuth` | 0.2.0 |
| `M3` | web | Salted PBKDF2-HMAC-SHA256 password hashes; the legacy `sha256("user:password")` form is still verified, never written | `PasswordHash`, `LegacyPasswordHash`, `VerifyPassword`; `web_test.go` `TestVerifyPasswordFormats` | 0.2.0 |
| `M4` | web | Failed basic-auth attempts are throttled per source bucket (delay doubling, concurrency cap) | `ratelimit.go` `authLimiter`; `web_test.go` `TestAuthRateLimit` | 0.2.0 |
| `L2` | web | A log clear leaves a trace naming the client (first line of the new file, journal) | `clearLog`; `log_api_test.go` `TestLogStoreEndpoints` | 0.2.0 |
| `M1` | web (tls) | The TLS listener config is the one `tlscert.ValidatePair` handshakes against, so an accepted pair is one the listener can serve | `ServeTLSStore`; `tls_test.go` `TestServeTLSStoreHotSwap` | 0.2.1 |
| `M3` | web (tls) | While the configured file pair is unusable the automatic certificate is served; `GET /api/tls` exposes `fallback` | `TLSFallback`, `getTLS`; `tls_test.go` `TestTLSInfoAndDownloads` | 0.2.1 |
| `M4` | web (tls) | Over TLS an uploaded leaf that does not cover the host the operator is connected through is refused (400, `force_required`) | `tlsUpload`; `tls_test.go` `TestTLSUploadHostGuard` | 0.2.1 |
| `L1` | web (tls) | No session tickets: a client that connected before a certificate swap sees the new one | `tlscert.ServerConfig`; `tls_test.go` `TestServeTLSStoreHotSwap` | 0.2.1 |
| `L2` | web (tls) | `Regenerate` reports whether the key really was kept (`kept`) | `TLSMgr.Regenerate`; `tls_test.go` `TestTLSRegenerateKeepKey` | 0.2.1 |
| `L4` | web (tls) | Certificate warnings (expiry, SANs, listen hosts) are computed server-side like `ValidatePair`, not in the UI | `getTLS`; `tls_test.go` `TestTLSInfoAndDownloads` | 0.2.1 |
| `L7` | web (tls) | The audit line of an upload quotes the subject | `tlsUpload`; `tls_test.go` `TestTLSUploadHostGuard` | 0.2.1 |
| `R-M1` | web | Sessions are bound to a credential epoch (`sha256(user\nhash)`); a mirror file written under another epoch is dropped at start; account changes move the epoch first | `CredentialEpoch`, `NewSessionStoreEpoch`, `SetEpoch`, `New`, `applyAccount`; `session_test.go` `TestSessionStoreEpoch`, `account_test.go` `TestAccountChangeSurvivesRestart` | 0.3.0-beta.1 |
| `R-L7` | web | A request whose query carries `;` must not produce an `ErrorLog` line (log-flood vector) | `New` (query wrapper); `web_test.go` `TestQuerySemicolonNotLogged` | 0.3.0-beta.1 |
| `R-L8` | web | Applying a preset the store reports as missing (a built-in of another profile counts as missing) answers 404 | `applyPreset`; `presets_test.go` `TestPresetStatusCodes` | 0.3.0-beta.1 |
| `R-L9` | web | A test delivery still running answers 409 `test in progress` | `alertsTest`; `alerts_test.go` `TestAlertsTestBusy` | 0.3.0-beta.1 |
| `R-L10` | web | After a rename the surviving session reports the new user name | `RevokeAllRename`; `session_test.go` `TestSessionStoreRevokeAllRename` | 0.3.0-beta.1 |
| `R-U8` | web | Saving over a built-in preset name answers 409, like delete | `savePreset`; `presets_test.go` `TestPresetStatusCodes` | 0.3.0-beta.1 |
| `M1` | web (audit) | `PUT /api/config?strict=1` refuses only warnings on the `[[channel]]` tables | `putConfig`; `config_api_test.go` `TestPutConfigStrictChannelOnly` | 0.3.0-rc1 |
| `M2` | web (audit) | The auth concurrency cap is counted per address, not per IPv6 /64 | `ratelimit.go` `busy`/`addrKey`; `ratelimit_test.go` `TestAuthBusyPerAddress` | 0.3.0-rc1 |
| `L3` | web (audit) | A link-local zone (`fe80::1%vmbr0`) is stripped before bucketing | `ratelimit.go` `parseAddr`; `ratelimit_test.go` `TestLimitKeyZone` | 0.3.0-rc1 |
| `L4` | web (audit) | Two successful verifications of a legacy hash at the same instant must not both rewrite the file | `upgradeMu`, `upgradeLegacyHash`; `account_test.go` `TestLegacyHashUpgradeSerialised` | 0.3.0-rc1 |
| `L6` | web (audit) | A config whose `password_hash` placeholder sits in an inline `web = { … }` table is refused (the restore does not reach it) | `putConfig`; `config_api_test.go` `TestPutConfigInlinePlaceholderRefused` | 0.3.0-rc1 |
| `L7` | web (audit) | A preset that was written but not taken by the daemon answers 500 "written, reload failed", not 400 | `ReloadError`, `applyPreset`; `presets_test.go` `TestApplyPresetReloadFailed500` | 0.3.0-rc1 |

## cmd/n5-fangov

| Tag | Package | Finding | Fixed where | Version |
|---|---|---|---|---|
| `M4` | cmd | The apt hook checks the kernels the box can boot into (running + `proxmox-boot-tool` selection), not every `/lib/modules` entry | `check.go` `relevantKernels`; `check_test.go` `TestRelevantKernels` | 0.2.0 |
| `M7` | cmd | A short password hash must not be replaced as a bare substring in the exported config | `redactConfigText` (`web.RedactRaw`); `bundle_test.go` `TestBundleExportShortHash` | 0.2.0 |
| `L1` | cmd | Bundle import stages presets under temp names and renames them only after the config is written | `bundle.go` `Import`; `bundle_test.go` `TestBundleImportStagesPresets` | 0.2.0 |
| `L4` | cmd | Ctrl-C while the password echo is off restores the terminal before the process ends | `prompt.go` `askPassword`; `prompt_unix_test.go` | 0.2.0 |
| `L5` | cmd | Without a log file, `Clear` is `errors.ErrUnsupported` (501), not a generic error | `journalLogStore.Clear`; `wiring_test.go` `TestJournalLogStoreClearUnsupported` | 0.2.0 |
| `M3` | cmd | `setup` and `passwd` write the salted PBKDF2 hash form | `setup.go` (`passwordHash`); `setup_test.go` `TestSetupConfigN5Pro` | 0.2.0 |
| `M5` / `H3` | cmd (cert) | An unspecified listen puts the route-based primary IPv4/IPv6 into the SANs, not every interface address (churn); no address found is logged and the certificate still covers host name + loopback; the interface fallback needs `AF_NETLINK` | `cert.go` `tlsHosts`, `primaryIPs`; `cert_test.go` `TestTLSHostsUnspecified` | 0.2.0 |
| `M1` | cmd (tlsmgr) | A pair the listener cannot serve (in-process handshake) is an error, never a swap; P-224 pairs on disk are "unusable" | `tlsmgr.go` `serve`, `load`; `tlsmgr_test.go` `TestTLSManagerFallback` | 0.2.1 |
| `M2` | cmd (tlsmgr) | The daemon owns `[web] tls/cert_file/key_file` while it runs: every config text written through the API is pinned (`pinConfig`); a mode overridden by `--listen` is not pinned | `tlsmgr.go` `ownsConfig`, `pinConfig`; `fileConfigStore`, `dirPresetStore`, `fileBundle`, `webDeps.ConfigPin`, `serveWeb`; `tlsmgr_test.go` `TestTLSManagerPinsConfigKeys` | 0.2.1 |
| `M3` | cmd (tlsmgr) | A configured file pair that cannot be loaded falls back to the automatic certificate with a `tls` alert; `cert reset/upload/regen` work offline on a broken pair | `tlsmgr.go` `loadForServe`, `shouldFallbackToAuto`, `modeFallback`; `cert_cli.go` `offline`; `serveWeb`; `tlsmgr_test.go` `TestTLSManagerFallback`, `TestCertCLIOfflineResetBrokenPair` | 0.2.1 |
| `L2` | cmd (tlsmgr) | `Regenerate` says whether the key really was reused | `tlsmgr.go` `Regenerate`; `tlsmgr_test.go` `TestTLSManagerRegenerateKeepsKey` | 0.2.1 |
| `L8` | cmd (tlsmgr) | The config path is made absolute (CLI vs. `/` under systemd); a private key readable by group/others is warned about at start | `tlsmgr.go` `newTLSManager`; `wiring_tls.go` `tlsCheckKeyMode`; `tlsmgr_test.go` `TestTLSManagerResetLeavesForeignPair` | 0.2.1 |
| `L9` | cmd (tlsmgr) | When the config cannot be written after an upload, the custom files are put back the way they were | `tlsmgr.go` `Upload`; `tlsmgr_test.go` `TestTLSManagerUploadRollback` | 0.2.1 |
| `R-L4` | cmd | Every config read-modify-write inside the daemon (account, alerts, dashboard, editor save, preset apply, bundle import) serialises on `configFileMu` | `wiring.go` `configFileMu`, `fileConfigStore.Save`; `editConfig`; `dirPresetStore.write`; `bundle.go` `Import`; `wiring_account_test.go` `TestConfigWritersSerialised` | 0.3.0-beta.1 |
| `R-L5` | cmd | The account store edits the value in the file at that moment, not its cached copy (a PUT or import may have changed `[web]`) | `wiring_account.go` `accountStore.Update`, `editConfig`; `wiring_account_test.go` `TestAccountStoreReadsFile` | 0.3.0-beta.1 |
| `R-L8` | cmd | A built-in preset of another profile is neither listed nor applied (404) | `wiring_presets.go` `dirPresetStore.load`; `wiring_presets_test.go` `TestPresetBuiltinOtherProfile` | 0.3.0-beta.1 |
| `R-L9` | cmd | One synchronous test delivery at a time, bounded to 20 s (below the HTTP write timeout) | `wiring_alerts.go` `alertManager.Test`, `testDeliveryLimit`; `wiring_alerts_test.go` `TestAlertTestBusyAndBounded` | 0.3.0-beta.1 |
| `R-L11` | cmd | The single-key stores handle the dotted `web.key = …` layout and refuse an inline table with a clear message instead of leaving the file inconsistent | `wiring_account.go` `editConfig`; `wiring_account_test.go` `TestStoresDottedLayout`, `TestStoresInlineTableRefused` | 0.3.0-beta.1 |
| `R-M2` | cmd | The PVE template probe (creates and unlinks a file on pmxcfs) is cached for 10 minutes and dropped after install/configure | `wiring_alerts.go` `templateStatus`, `invalidateTemplate`; `wiring_alerts_test.go` `TestAlertTemplateProbeCache` | 0.3.0-beta.1 |
| `L5` | cmd (audit) | The cooldown stamp of a start-up alert is written before the delivery goroutine starts | `wiring.go` `startAlert`; `alert_cooldown_test.go` `TestStartAlertStampBeforeDelivery` | 0.3.0-rc1 |
| `L6` | cmd (audit) | A bundle whose `password_hash` placeholder sits in an inline table is refused (it would become the stored hash) | `bundle.go` `Import`; `bundle_test.go` `TestBundleImportInlinePlaceholder` | 0.3.0-rc1 |
| `L7` | cmd (audit) | A preset written but not taken by the daemon comes back as `web.ReloadError` ("written, reload failed") | `wiring_presets.go` `dirPresetStore.Apply`; `wiring_presets_test.go` `TestPresetApplyReloadError` | 0.3.0-rc1 |

## internal/config

| Tag | Package | Finding | Fixed where | Version |
|---|---|---|---|---|
| `H2` | config | No fail-open: a broken `auth` setting forces the listener to loopback; `[log].file` must live under `/var/log/` | `parseWeb` (`authBroken`), `LogRoot`; `config_test.go` `TestWebAuthFailOpen`, `TestLogFileUnderVarLog` | 0.2.0 |
| `M3` | config | Both stored hash forms parse (PBKDF2 and legacy), malformed ones are rejected; a config that carries a hash is written 0600 | `ParsePasswordHash`, `file.go` `Save`; `config_test.go` `TestWebBasicAuth`, `TestParsePasswordHash`, `TestSaveTightensModeWithHash` | 0.2.0 |
| `R-M3` | config | `mail_to` never starts with `-` (mail(1) would take it as an option) | `ValidMailTo` (`mailToRe`); `config_test.go` `TestValidMailToNoLeadingDash` | 0.3.0-beta.1 |
| `R-L11` | config | Sections written as top-level dotted keys (`web.user = …`) parse and are edited in place by `SetKey` | `parser.table`, `file.go` `SetKey`; `config_test.go` `TestParseDottedTables`, `TestSetKeyDotted` | 0.3.0-beta.1 |

## internal/alert

| Tag | Package | Finding | Fixed where | Version |
|---|---|---|---|---|
| `L5` | alert | Every delivery command carries `WaitDelay`, so a grandchild holding the pipes cannot block the alert goroutine after the timeout | `command`; `alert_test.go` `TestCommandWaitDelay` | 0.2.0 |
| `R-M3` | alert | The mail recipient is passed after `--` | `Mail.SendCtx`; `alert_test.go` `TestMailRecipientAfterDoubleDash` | 0.3.0-beta.1 |
| `R-L9` | alert | Delivery is context-bounded (`ContextSender`, `Ring.SendCtx`); `ErrTestBusy` while a test runs | `ContextSender`, `ErrTestBusy`, `ring.go` `SendCtx`; `alert_test.go` `TestSendCtxBounded` | 0.3.0-beta.1 |

## internal/logfile, internal/ipc

| Tag | Package | Finding | Fixed where | Version |
|---|---|---|---|---|
| `H1` | logfile | A stalled export (slow HTTP client) must not hold the lock the controller's log calls need; a rotation during an export renames under the open descriptor | `Writer` (read side without the lock); `logfile_test.go` `TestExportDoesNotBlockWrites`, `TestExportSurvivesRotation` | 0.2.0 |
| `H2` | logfile | An existing log path must be a regular file (no symlink, device, directory); `O_NOFOLLOW` closes the race between check and open | `New`, `nofollow_unix.go`; `logfile_test.go` `TestNewRefusesNonRegularTarget` | 0.2.0 |
| `M3` | ipc | The socket node is created with umask 0117 so no window exists before `Chmod` | `Listen` (`setUmask`); `ipc_test.go` `TestListenRestoresUmask` | 0.2.0 |

## internal/tlscert

| Tag | Package | Finding | Fixed where | Version |
|---|---|---|---|---|
| `M1` | tlscert | Key types crypto/tls cannot sign with (ECDSA outside P-256/384/521, RSA < 1024) are refused; every pair survives a handshake against `ServerConfig`; the automatic CA carries name constraints and `pathlen 0` | `usableKey`, `ValidatePair`, `CheckUsable`, `ServerConfig`, `EnsureAuto`; `validate_test.go` `TestValidatePairKeyTypes`, `tlscert_test.go` `TestNameConstraints` | 0.2.1 |
| `M5` | tlscert | A regeneration caused by a SAN change keeps the private key (imported trust survives) | `EnsureAuto`; `tlscert_test.go` `TestRegenerateKeepsKeyOnSANChange` | 0.2.0 |
| `L1` | tlscert | Session tickets are off | `ServerConfig`; `validate_test.go` `TestServerConfig` | 0.2.1 |
| `L2` | tlscert | `Reissue` without a loadable key reports `kept=false` | `Reissue`; `store_test.go` `TestReissueKeepsKey` | 0.2.1 |
| `L3` | tlscert | Encrypted private keys are refused with a hint; `EC PARAMETERS` blocks are skipped; private files are written through an unpredictable temp file and rename | `ErrEncryptedKey`, `ValidatePair`, `writePrivate`; `validate_test.go` `TestValidatePairPEMForms`, `tlscert_test.go` `TestNameConstraints` (no temp files) | 0.2.1 |
| `L4` | tlscert | `Warnings` computes expiry / no SANs / uncovered hosts from `InfoData` for `GET /api/tls` | `Warnings`; `validate_test.go` `TestDaysLeftAndWarnings` | 0.2.1 |
| `L5` | tlscert | `DaysLeft` rounds up | `DaysLeft`; `validate_test.go` `TestDaysLeftAndWarnings` | 0.2.1 |
| `L8` | tlscert | A private key file readable by group or others is reported | `CheckKeyMode`; `tlscert_test.go` `TestCheckKeyMode` | 0.2.1 |

## deploy

| Tag | Package | Finding | Fixed where | Version |
|---|---|---|---|---|
| `H2` | deploy | `PrivateDevices=yes`: nothing needs a block or character device | `n5-fangov.service` | 0.2.0 |
| `H3` | deploy | `AF_NETLINK` only for the interface list of the certificate SANs | `n5-fangov.service` `RestrictAddressFamilies` | 0.2.0 |
| `H4` | deploy | The postfix maildrop path is writable for the mail(1) alert path on non-PVE hosts | `n5-fangov.service` `ReadWritePaths` | 0.2.0 |
| `M2` | deploy | The daemon runs as root without any capability | `n5-fangov.service` `CapabilityBoundingSet=`; `deploy_test.go` `TestDeployUnitFile` | 0.2.0 |
| `M3` | deploy | The run dir with the unauthenticated CLI socket is root only (0750) | `n5-fangov.service` `RuntimeDirectoryMode` | 0.2.0 |
| `L7` | deploy | `ReadWritePaths` entries carry `-` so an absent path does not fail the start | `n5-fangov.service` | 0.2.0 |
| `R-L6` | deploy | Installing the PVE template is best effort (pmxcfs is read-only without quorum) | `install.sh` | 0.3.0-beta.1 |
