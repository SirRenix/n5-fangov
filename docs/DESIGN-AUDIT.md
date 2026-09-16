# Design- und UX-Abnahmeprüfung — n5-fangov Dashboard

Stand: 16.09.2026 · geprüfter Stand: `main` `6d4dfc3` (0.3.0-beta.4 + die sieben Fixes aus `docs/AUDIT.md`; Manual-Tab und Primärbutton-Kontrast sind darin bereits behoben und hier nicht erneut bewertet). **Nur geprüft, nichts geändert.** Ergänzt die technische Abnahme in [`AUDIT.md`](AUDIT.md) (dort Bereich 8 „GUI"); Überschneidungen sind hier mit Zeilenangaben und Messwerten präzisiert.

| | |
|---|---|
| Zielgruppe | Homelab-Admins und technisch versierte Endanwender; Dashboard läuft auf dem Server im LAN |
| Nutzungsumgebung | Windows 11 mit Chrome/Edge/Firefox, Full HD 1920×1080 bei 100 % und 150 % Skalierung (≙ 1280 CSS-px), 1024 px (Laptop 125 %), Smartphone 375 px; Dark-Theme als Standard, Light-Theme |
| Grundlage | Code der Oberfläche (`internal/web/static/index.html`, `app.js`, `app.css`) und Live-Durchgang aller Hauptaufgaben gegen den eingebauten Mock (`?mock=1`) im Browser; Screenshots: **`docs/screenshots/` existiert nicht** — die geprüften Ansichten sind unten beschrieben, die für die Anleitung nötigen Screenshots in Abschnitt 9 gelistet |
| Ergebnis | **0 kritisch · 4 hoch · 15 mittel · 33 niedrig** — Go für das Design mit Auflagen (Abschnitt 8) |

## 1. Kopf

| | |
|---|---|
| Geprüfter Stand | `N5Pro/n5-fangov`, `main`, 0.3.0-beta.4 + Fixes (Manual-Tab, Button-Kontrast bereits korrigiert — nicht erneut bewertet) |
| Dateien | `internal/web/static/index.html` (288 Z.), `app.js` (938 Z.), `app.css` (326 Z.) |
| Umgebung | Windows 11, Claude-Browser-Pane (Chromium), Wegwerf-Static-Server `node static-srv.js` auf 127.0.0.1:8795 (PID 25784, nach der Prüfung beendet, Port ohne LISTEN) |
| Viewports | 1920×1080, 1280×720 (≙ Full HD bei 150 %), 1024×768, 640×540 (≙ 200 % Zoom bei 1280), 375×812 (mobile Preset) |
| Themes | dark (Standard), light; `prefers-reduced-motion: reduce` war in der Pane aktiv → Animationen nachweislich abgeschaltet |
| Methode | Mock-URLs `?mock=1`, `&user=1`, `&auth=none`, `&tab=…`, `&tls=off/soon/fallback/file`, `&syserr=1`; Accessibility-Baum (`read_page`), DOM-Messungen (`getBoundingClientRect`, `scrollWidth`), Tastatur (Tab, Pfeiltasten), Klicks; Kontraste rechnerisch aus den `:root`-Werten (WCAG-Formel, `color-mix` als lineare sRGB-Mischung nachgebildet); `grep` für hart codierte Werte |
| Nicht prüfbar | Enter-Taste zum Absenden von Formularen und Escape zum Schließen von `<dialog>` lösten in der Pane nichts aus — das ist ein Artefakt des Tool-Key-Dispatch, kein Befund (Markup ist Standard-HTML mit `type=submit` und nativem `showModal`). Downloads (.crt/.cer/Log/Settings) wurden nicht ausgelöst. |
| Screenshots | Die Browser-Pane liefert Bilder nur inline, es entstanden **keine Dateien**; `design-shots\` ist leer. Die Ansichten sind in den Befunden beschrieben; Abschnitt 9 listet, was die Anleitung braucht. |

Gesehene Ansichten (inline): Overview anonym 1920 · Login-Dialog leer/Fehler · Overview eingeloggt 1920 oben/unten · Overview 1280 · Curves 1280 (gültig, 400-Fehler mit critical=900) · Manual 1280 (auto, HDD-Warnung, manual) · Presets 1280 (Liste, Details offen, Validierungs-Bubble) · Alerts 1280 (auto, mail-Fehler-Toasts) · Certificate-Dialog (automatic, Regenerate-Form + How-to offen, TLS off, fallback) · Settings-Popover · Account-Dialog (Sessions, Passwort-Fehler) · Log 1280 · System 1280 oben/unten, mit `syserr` · Overview light 1280 · Alerts light 1280 · Overview mobile anonym/eingeloggt · Curves mobile · Overview 640 · Overview 1024.

## 2. Befundtabelle

Schweregrade: **kritisch** (blockiert Aufgabe) · **hoch** (Aufgabe erheblich erschwert / AA-Verstoß im Kerntext) · **mittel** (spürbare Reibung) · **niedrig** (Politur). Sortiert nach Bereich, dann Schweregrad.

| Nr | Bereich | Ansicht/Datei:Zeile | Befund | Schweregrad | Empfehlung |
|---|---|---|---|---|---|
| B01 | 1 Usability | Tab-Leiste, `app.css:111` | Bei 375 px (und 640 px) ist die Tab-Leiste 690 px breit, `overflow-x:auto` mit `scrollbar-width:none`. Gemessen: `tab-about` endet bei x=674, sichtbar sind nur Overview…Alerts. Kein Pfeil, kein Fade, kein Scrollbalken — System, Log, Compatibility, About sind auf dem Handy nicht auffindbar. | hoch | Fade-Kante rechts (`mask-image`) oder Scroll-Hinweis; unter 700 px alternativ ein `<select>`/Menü-Button für die Sektionen. |
| B02 | 1 Usability | Presets/Log/Cert/Account, `app.js:680, 694, 698, 751, 846, 877, 914, 924` | Rename nutzt `prompt()`, Apply/Delete/Import/Clear/Revoke/Regenerate-Key/Reset nutzen `confirm()`. Native Browser-Dialoge brechen den Stil der drei `<dialog>`-Dialoge, sind nicht themebar, in Chrome mit „127.0.0.1 meldet" betitelt und in Firefox ohne Fokus-Rückgabe. | mittel | Einen generischen Bestätigungs-`<dialog>` (Titel, Text, Primär-/Abbrechen-Button) und einen Eingabe-Dialog für Rename bauen; `prompt`/`confirm` entfernen. |
| B03 | 1 Usability | Presets, `app.js:695-696`; Curves `app.js:636-637`; Import `app.js:849` | Bei HTTP 202 erscheinen gleichzeitig Toast „Preset summer applied" (grün, ok) und Notice „Preset written — restart required: systemctl restart n5-fangov". Die grüne Erfolgsmeldung widerspricht dem gelben „noch nicht wirksam". Die Notice hat kein Schließen-Element und bleibt bis zum Tabwechsel/Reload. | mittel | Bei 202 nur einen warn-Toast („Written — restart required") und die Notice mit Schließen-Button; Curves-Variante gleich behandeln. |
| B04 | 1 Usability | Curves, `index.html:97-99`, `app.js:524-640` | Keine Anzeige ungespeicherter Änderungen: nach Drag/Tabelleneingabe gibt es kein „geändert"-Kennzeichen, Tabwechsel und Abmelden verwerfen still (`edState=null` in `applyAuth`). „Apply to daemon"/„Revert" stehen nur oben; auf 375 px liegen sie ~1 500 px über dem dritten Editor. | mittel | Dirty-Zustand pro Editor (Punkt am Kanalnamen, „Apply" nur aktiv wenn dirty), `beforeunload`/Tabwechsel-Warnung, Aktionsleiste `position:sticky; bottom:0` oder unten wiederholt. |
| B05 | 1 Usability | Manual, `app.js:651-654` | HDD-Slider erlaubt 0–255, der Daemon lehnt < `stall_min_duty` (60) ab. Der Nutzer stellt 40 ein, sieht die Warnung, klickt „Set" und bekommt den warn-Toast „hdd: duty below 60 refused". Ein Schritt, der nur scheitern kann. Außerdem kein Zahlenfeld: per Tastatur 255 Einzelschritte (`step` 1, kein `PageUp`-Mapping). | mittel | `min` des Sliders auf `minDuty(c)` setzen (Warnung bleibt als Hinweis), „Set" bei ungültigem Wert deaktivieren, Zahlenfeld + Slider koppeln, `step=5` mit Feineinstellung per Feld. |
| B06 | 1 Usability | Alerts, `app.js:711-726`, `index.html:124-125` | Transport „mail" ist wählbar, obwohl die Statusliste darunter „mail(1) not available" zeigt; „Save" quittiert mit ok-Toast „effective: mail", der Testalarm scheitert dann mit „Test alert failed (mail): mail: exit status 127". „Mail to" ist bei allen Transporten sichtbar. | mittel | Nicht verfügbare Transporte im Select mit „(not available)" markieren oder `disabled`; „Mail to" nur bei `auto`/`mail` einblenden; Fehlertext übersetzen („mail(1) is not installed — apt install bsd-mailx or choose pve/log"). |
| B07 | 1 Usability | Overview Sensors, `app.js:502-503` | „chart"-Button auf einem Sensor: kein Toast, die einzige Rückmeldung ist der Button-Zustand und das Extra-Chart, das bei 1920 ~300 px, bei 375 px ~1 000 px weiter oben liegt und nach `resetHistory()` kurz „no history" zeigt. | niedrig | Kurzer ok-Toast „ec:ambient added to the chart (3/8)" bzw. „removed"; Zähler „3/8" in der Kartenüberschrift statt nur „(max 8)". |
| B08 | 1 Usability | Log/Settings/Cert, `app.js:749, 839, 904` | Export-Log, Export-Settings, Download .crt/.cer laufen über `act(() => download(...))` ohne ok-Text → einzige Rückmeldung ist die Browser-Download-Leiste (in Edge/Chrome ein kleines Symbol rechts oben, auf Android gar nichts sichtbar). | niedrig | `act(..., 'Log exported')` etc.; beim Zertifikat „Downloaded n5-fangov-n5host.cer — now import it (see How to trust)". |
| B09 | 1 Usability | Presets, `index.html:112` | Ungültiger Name („Test Name") erzeugt die Browser-Bubble in Systemsprache („Deine Eingabe muss mit dem geforderten Format übereinstimmen.") plus englischem `title`. Gemischtsprachig, verschwindet beim Klick. | niedrig | `novalidate` + eigene Prüfung wie bei Rename (`app.js:681`) mit Notice/Toast; `title` als sichtbaren Hint unter das Feld. |
| B10 | 1 Usability | Log, `app.js:743`, `index.html:173-181` | Es werden die letzten 200 Zeilen geladen, ohne dass die Ansicht das sagt; „Refresh" wirkt wie „alles", Filter durchsucht nur diese 200. | niedrig | Meta-Text „last 200 lines · source: file"; Filterergebnis „12 of 200". |
| B11 | 1 Usability | Presets | Kein Hinweis, welches Preset zuletzt angewendet wurde oder ob die aktuellen Kurven einem Preset entsprechen. Nach „Apply" sieht die Liste identisch aus. | niedrig | Badge „active" wenn die Kanalkurven mit einem Preset übereinstimmen (Vergleich clientseitig aus `cfg` vs. `GET /api/presets/{name}`). |
| B12 | 1 Usability | Cert-Dialog fallback, `app.js:887` | Im Fallback-Zustand (`&tls=fallback`) ist „Regenerate…" sichtbar (`regen.hidden=false`), der Mock lehnt es mit 409 „custom certificate active; reset to auto first" ab. Sichtbarer Button, der nur scheitern kann. | niedrig | `#ct-regen` bei `fallback` ausblenden oder „Back to auto" als Primäraktion hervorheben. |
| B13 | 2 Layout | Header mobile eingeloggt, `app.css:38-39` | Bei 375 px umbricht der Sticky-Header auf 4 Zeilen (gemessen 150 px) + Tab-Leiste 43 px = 193 px = 24 % von 812 px dauerhaft belegt. Beim Scrollen bleibt der Header stehen; sichtbarer Inhalt beim Blick auf Temperaturen ~600 px. Anonym sind es 78 px. | hoch | Unter 700 px: Meta-Zeile (uptime, version, TLS, live) in ein Overflow-Menü oder in den About-Tab; Header auf 2 Zeilen (Brand+Status / User+Buttons) begrenzen; Tabs sticky machen und den Header beim Scrollen einklappen. |
| B14 | 2 Layout | Overview 1024 px, `app.css:121-122, 153` | Kanalkarten wechseln erst ab 1100 px auf 3 Spalten; bei 1024 (Laptop 125 %) stehen 2 Karten in Zeile 1, die dritte allein in Zeile 2 mit 483 px Leerraum; die Charts sind untereinander (947 px breit). | mittel | `grid-template-columns:repeat(auto-fit,minmax(300px,1fr))` für `.cards` — ergibt 3 Spalten ab ~960 px; Charts ab 900 px nebeneinander. |
| B15 | 2 Layout | Tab-Leiste, `app.css:111` | Nur der Header ist sticky. Overview eingeloggt ist 1 654 px hoch (1920×1080); nach dem Scrollen zu Sensors/Alerts ist die Tab-Leiste weg — Tabwechsel erfordert Hochscrollen. Auf 375 px gilt das für jede Sektion. | mittel | `.tabs{position:sticky;top:<hdr-höhe>}` oder Header+Tabs in einen gemeinsamen Sticky-Container. |
| B16 | 2 Layout | Manual 1280, `app.css:202` | `.mn .warn{min-height:18px}` reserviert eine Zeile; der HDD-Warntext bricht bei 403 px Kartenbreite auf 2 Zeilen → hdd-Karte 20 px höher, Buttons versetzt zu cpu/ssd. | niedrig | `min-height:36px` oder Warntext unter die Buttons; Karten mit `align-items:start` und Buttonreihe `margin-top:auto`. |
| B17 | 2 Layout | Curves 1280, `app.css:183-186` | Kopfzeile des Editors (Name · sensor-Select 170 px · critical · stop) bricht bei 611 px Editorbreite bei cpu/ssd um („stop auto" in Zeile 2), bei hdd (kürzere Sensor-Beschriftung) nicht → uneinheitliche Editorköpfe. | niedrig | Kopfzeile in zwei feste Zeilen (Name links, Felder in eigener `.fields`-Zeile) oder `select{max-width:140px}`. |
| B18 | 2 Layout | Presets 1280, `app.css:206` | Built-ins haben 2 Buttons (Details, Apply), eigene 4 (…, Rename, Delete) — die Spalten fluchten nicht, „Apply" springt zwischen Zeilen. | niedrig | Feste Button-Spalte (`grid-template-columns:auto 1fr repeat(4,auto)`), Platzhalter oder Rename/Delete in ein „…"-Menü. |
| B19 | 2 Layout | Curves 1280, `app.css:181` | `repeat(auto-fit,minmax(460px,1fr))` ergibt 2 Spalten; der dritte Editor steht allein mit 611 px Leerraum. | niedrig | Bei ungerader Anzahl letzten Editor `grid-column:1/-1` oder ab 1400 px 3 Spalten. |
| B20 | 3 Konsistenz | `app.css` gesamt, `app.js` Chart/Timer, `index.html` Attribute | Abgesehen von 4 Tokens (`--sh`, `--r`, `--gap`, `--focus`) sind Schriftgrößen (10/11/12/13/14/15/16/18/28/40 px), Abstände (2–16 px), Radien (1/4/6/8/99 px), Schatten, Breakpoints (480/700/900/1100), Timer (5 000/8 000/12 000/15 000/30 000/60 000 ms) und Canvas-Fonts hart codiert. Vollständige Liste in Abschnitt 3 (~180 Fundstellen). Folge: Chart-Achsenfarbe kommt aus `--fg3`, Chart-Font aber aus `'11px system-ui'` — bei Theme-/Font-Änderung driften Canvas und DOM auseinander. | mittel | Token-Set aus Abschnitt 6 einführen; Canvas liest `cssVar('--font-sans')`/`--fs-11`; Timer als `TIMING`-Objekt. |
| B21 | 3 Konsistenz | Header, `app.js:338`, `index.html:26, 59, 207` | Icons sind Emoji/Glyphen: 🔒/🔓 (TLS), ⚙ (Settings), × (Schließen). Windows rendert 🔒 farbig (Segoe UI Emoji), Firefox/Linux monochrom oder Twemoji; Größe und Baseline schwanken. `⚙` ist im `.btn.icon` 16 px, `×` im Toast 14 px, im Legend-× 14 px, im Dialog 18 px. | niedrig | Inline-SVG-Icons (16 px, `currentColor`), ein `.icon`-Token; Schloss-Zustand zusätzlich per Text „TLS"/„HTTP" (bereits vorhanden). |
| B22 | 3 Konsistenz | Toasts, `app.js:387, 391, 637-640, 683, 699, 705, 723, 871, 875, 910` | Wortlaut und Schreibweise uneinheitlich: „Preset renamed to “winter”" (Anführungszeichen) vs. „Preset winter deleted" (ohne) vs. „Saved current curves as “summer2”"; „Rejected" vs. „Curves applied"; „Signed in" (ok) vs. „Signed out" (neutral); „cpu: back to auto" klein, „Password changed" groß. | niedrig | Muster festlegen: `<Objekt> <Verb-Partizip>` ohne Anführungszeichen, Satzanfang groß; Kind: Erfolg = ok, Abmelden = ok. |
| B23 | 3 Konsistenz | Zeitformate, `app.js:37-43, 451, 712, 889` | Fünf Formate nebeneinander: „16 min ago", „1 d 2 h ago", „6 d 0 h ago" (Sessions), „17.9.2026, 04:20:51" (Locale-Absolut), „10m0s" (Go-Duration im Cooldown), „2026-09-16" (ISO im Zertifikat), „live 17:57" (HH:MM). | niedrig | Eine `fmtRel` (ohne „0 h"), eine `fmtAbs` (ISO-ähnlich `2026-09-17 04:20`), Cooldown serverseitig als Sekunden liefern und als „10 min" formatieren. |
| B24 | 3 Konsistenz | Buttons, `index.html:99, 112, 126, 212, 229, 261, 270`, `app.js:557, 653` | Primär-Stil ohne klare Regel: „Apply to daemon" (primary), Presets „Apply"/„Save" (sekundär), Alerts „Save" (primary), Manual „Set" (primary), Curves „Add" (`btn sm primary`), Cert-Downloads (kein Primär, obwohl Hauptaufgabe). | niedrig | Regel: genau eine Primäraktion pro Karte/Dialog; Presets „Save" primär; Cert „Download .cer" (auf Windows) primär. |
| B25 | 3 Konsistenz | Duty-Darstellung, `app.js:418, 652, 675`, Curves-Tabelle `app.js:547` + Achse `app.js:592` | Duty erscheint als „36 % (93 → 115)" (Karte), „33 % 85/255" (Manual), roh „85" in der Kurventabelle bei Achse in „%", „85 (33 %)" in Preset-Details. Nutzer muss 0–255 und Prozent parallel denken. | niedrig | Eine Darstellung „33 % (85)" überall; Kurventabelle mit Prozent-Spalte oder Achse in 0–255. |
| B26 | 4 Zustände | Header TLS-Button, `app.js:337-340`, Mock `&tls=soon`, `&tls=fallback` | Bei Zertifikat „expires in 12 days" und im Fallback („configured files could not be loaded") bleibt der Header-Button grün „🔒 TLS" (`class sec ok`); nur der `title`-Tooltip trägt „(soon!)", auf Touch nicht erreichbar. Der Fehlerzustand ist erst nach Klick im Dialog sichtbar. | mittel | `sec warn` bei `fallback` oder `dLeft < 30`, Text „🔒 TLS · 12 d" bzw. „TLS · fallback"; zusätzlich `#g-notice` beim Laden. |
| B27 | 4 Zustände | `app.js:630, 653, 656, 694, 724, 728, 915, 921` | Außer Login (`bt.disabled`) wird kein Button während des Requests deaktiviert; Doppelklick auf „Apply to daemon", „Set", „Send test alert", „Install template", „Regenerate now" löst den Request zweimal aus (Mock antwortet nach 120 ms). | niedrig | Hilfsfunktion `busy(btn, fn)` (disabled + `aria-busy`), in `act()` integrieren. |
| B28 | 4 Zustände | `index.html:52, 101, 114`, `app.js:849` | `#cv-notice`, `#ps-notice`, `#g-notice` (Banner „Settings imported — restart required") haben keinen Schließen-Button; `#g-notice` bleibt bis Reload. | niedrig | Schließen-× wie im Toast; `#g-notice` nach erfolgreichem Neustart (Uptime sinkt) automatisch ausblenden. |
| B29 | 4 Zustände | Fehlertexte, `app.js:452, 638, 726, 868, 922` | Servertexte roh: „validation failed\nchannel cpu: critical out of range 30..110", „mail: exit status 127", „current password wrong", „lspci: exec: "lspci": executable file not found in $PATH (device names from ids only)". Kein Handlungshinweis, Feld nicht markiert. | niedrig | Mapping bekannter Fehler auf Nutzer-Text + Feldmarkierung (`aria-invalid`, roter Rahmen am `critical`-Feld); System-Fehler „lspci fehlt — `apt install pciutils`". |
| B30 | 4 Zustände | Toasts 1280×720, `app.css:220-223` | Bis zu 4 Toasts gleichzeitig (Presets-Sequenz, Alerts-Sequenz) stapeln sich unten rechts und verdecken die „Recent alerts"-Liste; ok-Toasts 5 s, err 12 s. | niedrig | Maximal 3 sichtbar (älteste entfernen), gleiche Meldung zusammenfassen; Toasts auf Mobile oben. |
| B31 | 4 Zustände | Curves, `app.js:539, 584, 627` | `critical=900` (Feld hat `max=120`, wird nicht erzwungen): X-Achse läuft bis 910 mit Labels alle 20° → ~46 überlappende Beschriftungen, Kurve unlesbar; Client-Validierung prüft nur `> 0`, erst der Server meldet „30..110". | niedrig | `clamp(critical, 30, 120)` in `validateCurves`, `xmax` auf 130 deckeln, Label-Schrittweite aus `niceStep`. |
| B32 | 5 Barrierefreiheit | `app.css:4, 13` (`--fg3`) | `--fg3` auf `--bg2`: 3,69:1 dark, 3,83:1 light; auf `--bg`: 4,0 / 3,51. Verwendet für 11–13-px-Text: `.hint.sm`, `.kv dt`, `.tbl th`, `.sub`, `.ch .row .k`, `.sn .d`, `.empty`, `.ed .ref`, Chart-Achsen, Legenden-×. AA verlangt 4,5:1 für Text < 18 pt. | hoch | dark `--fg3:#8b94a3` (≈5,2:1), light `--fg3:#5f6875` (≈5,4:1); reine Dekoration (Achsenlinien) bleibt bei `--line`. |
| B33 | 5 Barrierefreiheit | `app.css:14-15` (light) | Light-Theme auf Weiß: `--ok #0f9d58` 3,51, `--warn #b7791f` 3,64, `--info/--man #1d6fd1` 4,95 (ok). Damit unter 4,5:1: Header „TLS" 12 px, `.badge.ok/.warn` (auf mix12: 3,05/3,19), `.mode.m-auto` 3,12, `.chip.warn` 3,19, Alert-Kinds `warn`, `.log .w` 12 px, Manual-Warntext, `.tbl .act` 11 px. Serienfarben `--s3` 2,82, `--s4` 2,17, `--s5` 2,69 verfehlen auch die 3:1 für Grafik (Linien 2 px, Wertelabels 11 px). | hoch | light: `--ok:#0b7a44` (4,9), `--warn:#8a5a0f` (5,4), `--s3:#0f8a5f`, `--s4:#a86f00`, `--s5:#c2477f`; Badges ohne Mix-Hintergrund auf `--bg3` prüfen. |
| B34 | 5 Barrierefreiheit | `app.css:5-6, 146, 52` (dark) | Dark: `.mode.m-critical` (crit auf mix12) 4,14, `.chip.sensor-error` 4,24, Serien-Wertelabels 11 px fett: `--s2` 4,49, `--s5` 4,42, `--s8 #008300` 3,53 (auf `--bg2`). | mittel | `--crit:#f87171` für Text (5,6), Badge-Text auf `--fg` mit farbigem Rahmen; `--s8:#22a03a`; Wertelabels in `--fg` mit farbigem Punkt. |
| B35 | 5 Barrierefreiheit | `app.js:387, 391` | Nach Login (`ld.close()` + `applyAuth`) und nach „Sign out" (Button wird `hidden` während er den Fokus hat) liegt `document.activeElement` auf `BODY`; Tastaturnutzer beginnt von vorn. | mittel | Nach Login Fokus auf `#tab-overview` (oder `#h-user`), nach Sign out auf `#h-signin`. |
| B36 | 5 Barrierefreiheit | `app.css:90, 81` | Eingabefeld-Rahmen `--line2` auf `--bg`: 1,65 dark / 1,46 light; Button-Rahmen auf `--bg2`: 1,52 / 1,6. WCAG 1.4.11 verlangt 3:1 für Komponentengrenzen, die den Zustand/Umriss allein anzeigen. Felder sind ohne Fokus kaum als Felder erkennbar (light: `#c8cdd6` auf `#f4f5f7`). | mittel | `--line2` dark `#4a5566` (≈3,1), light `#8e97a6` (≈3,0); oder Felder mit abgesetztem Hintergrund (`--bg3`) plus Rahmen. |
| B37 | 5 Barrierefreiheit | `app.css:282, 88, 66, 223`; Drag `app.js:614` | Zielgrößen: Legenden-× 16×14 px, Toast-× ~24×20, `.btn.sm` („chart", „remove", „copy") 24 px hoch, Header `button.sec` 22 px; Kurvenpunkt-Drag-Radius 14 px (28 px Ziel). WCAG 2.2 2.5.8 verlangt 24×24, Touch-Empfehlung 44 px. | mittel | Mindestens `min-height:32px` für `.btn.sm`, ×-Buttons 28×28 mit `padding`, Drag-Radius auf 22 px bei `pointerType==='touch'`. |
| B38 | 5 Barrierefreiheit | `index.html:72, 80, 86`; `app.js:253-323` | Die drei Overview-Canvas haben weder `role="img"` noch `aria-label` (Curves-Canvas haben es); der Tooltip ist nur per Pointer erreichbar; keine Tabellen-/Textalternative der letzten Werte außer den Kanalkarten. | niedrig | `role="img" aria-label="Temperature, last 2 hours: cpu 46 °C, ssd 37 °C, hdd 41 °C"` bei jedem `draw()` aktualisieren; optional `<details>` mit Wertetabelle. |
| B39 | 5 Barrierefreiheit | `index.html:13, 28-48` | Kein `<h1>` (Brand ist `<span>`), Sektionen beginnen mit `h2`; das Settings-Popover ist ein `<div>` ohne `role`/Label, nur `aria-controls`. | niedrig | `<h1 class="brand">n5-fangov</h1>`; Popover `role="dialog" aria-label="Settings"` oder `role="group"`. |
| B40 | 5 Barrierefreiheit | `index.html:211, 237, 250`, `app.js:44` | Fehler-Notices in Dialogen (`#l-err` „invalid user or password", `#ac-notice`, `#ct-notice`) werden per `textContent` gesetzt, ohne `role="alert"`/`aria-live` — Screenreader hört nichts; nur Toasts sind `aria-live=polite`. | niedrig | `.notice.err{role=alert}` bzw. `aria-live="assertive"` auf den Notice-Containern; err-Toasts `assertive`. |
| B41 | 5 Barrierefreiheit | `app.js:906-907, 867` | „Regenerate…"/„Upload own certificate…" öffnen ein Unterformular, der Fokus bleibt auf dem auslösenden Button (gemessen: `ct-close` bzw. Button). Account-Unterformulare setzen den Fokus korrekt (`$(i).focus()`). | niedrig | Nach `ctForm()` erstes Feld fokussieren (`#ct-newkey` bzw. `#ct-cfile`). |
| B42 | 5 Barrierefreiheit | Kurven-Editor, `app.js:610-622` | Punkte per Tastatur nur über die Tabelle verschiebbar (funktioniert, `aria-label="point n temp/duty"`), das Canvas selbst ist nicht fokussierbar; „remove" bei 2 Punkten `disabled` ohne Begründung. | niedrig | `title="at least 2 points"` am deaktivierten Button; optional Pfeiltasten am Canvas mit `tabindex=0`. |
| B43 | 6 Texte | `index.html:98, 106, 130`, `app.js:568, 652` | Fachbegriffe ohne Erklärung in der UI: „duty", „stop" (Feld akzeptiert „auto" oder Zahl — was bedeutet die Zahl?), „critical °C", „stall", „slew"/„→ 115" (Zielwert in Klammern ohne Legende), „PWM", „pwm1", „tach". Tooltips fehlen an den Feldlabels. | mittel | `title`/Info-Icon an `sensor`, `critical`, `stop` („duty the channel gets when the daemon stops; auto = driver default"); Legende „(current → target)" einmal unter den Karten; Glossar im About-Tab. |
| B44 | 6 Texte | `index.html:193` vs. `app.js:194, 734` | „licence" (britisch) im `dt`, API-Feld `license`, Link-Text „GPL-2.0-only"; sonst durchgehend amerikanisch („Compatibility", „Regenerate", „Authorities"). | niedrig | „license" für Konsistenz mit dem restlichen UI. |
| B45 | 6 Texte | Buttons | Verben ohne Semantikregel: Alerts „Save" schreibt Config und wirkt sofort, Curves „Apply to daemon" tut dasselbe, Manual „Set", Cert „Upload and use", „Regenerate now", Template „Install template"/„Update template". | niedrig | „Apply" für sofort wirksame Config-Änderungen, „Save" nur für Presets/Dateien, „Set" für Manual behalten. |
| B46 | 6 Texte | Header, `index.html:19-22` | Abkürzungen „up 5d 1h" (Tooltip „daemon uptime"), „live"/„paused", „v0.3.0-beta.1" — Tooltips per `title` sind auf Touch nicht erreichbar. | niedrig | „uptime 5 d 1 h"; „live" mit `aria-label="polling every 5 s"`. |
| B47 | 6 Texte | Alerts, `index.html:128, 130, 134` | „auto = pve-notify when PVE::Notify and perl exist, else mail(1), else the log." und „kind `test`, no cooldown" setzen Perl-Modulnamen und man-Page-Notation voraus. | niedrig | „auto: Proxmox notifications if available, otherwise local mail, otherwise only the log." |
| B48 | 6 Texte | Cert-Notice, `app.js:894` | Warnung „SAN list lacks host X — under HSTS the browser will refuse that name" wird pro Host wiederholt (3 Zeilen fast identisch). | niedrig | Hosts sammeln: „Certificate does not cover 192.0.2.20, n5.lan, n5host — under HSTS the browser will refuse these names." |
| B49 | 7 Fenster | System-Tab GPU·NPU, `app.js:461` | NPU `driver_version` „2.23.0_20260412,0000000000000000000000000000000000000000" wird ungekürzt in `.tbl.sm` (`white-space:nowrap`) gerendert; Tabelle 919 px breit → ab < 950 px horizontaler Scroll innerhalb der Karte (gemessen 640 px: 576/919). Die Overview-Karte kürzt am Komma (`split(',')[0]`). | mittel | In der Tabelle ebenfalls am Komma kürzen, Rest im `title`; oder `overflow-wrap:anywhere` für die Spalte. |
| B50 | 7 Fenster | Cert-Dialog 1280×720, `app.css:228` | Mit geöffnetem Upload-Formular und „How to trust" ist der Dialog 763 px hoch bei `max-height:688px` → interner Scroll, „Upload and use" außerhalb des sichtbaren Bereichs. | niedrig | Unterformulare als eigene Schritte (Upload ersetzt die Info-Ansicht) oder `.how` bei offenem Formular zuklappen. |
| B51 | 7 Fenster | Header, `app.css:42` | Profiltitel `max-width:40vw` → bei 375 px „Minisforum N5 Pro (IT…" abgeschnitten; Tooltip fehlt. | niedrig | `title` mit vollem Namen; unter 480 px nur Profilname (`snap.profile`) statt Titel. |
| B52 | 7 Fenster | Tab-Leiste 640 px (200 % Zoom) | Bei 640 px CSS-Breite (1280 px @ 200 %) ist der About-Tab abgeschnitten (690 > 640), sonst kein horizontaler Scroll (`scrollWidth` 640), Karten 1-spaltig, Tabellen scrollen in der Karte. Wie B01, aber auf dem Desktop. | niedrig | Siehe B01. |

## 3. Hart codierte Werte (Bereich 3)

Vollständige Erhebung per `grep -nE "#[0-9a-fA-F]{3,6}\b|rgba\(|[0-9.]+px|font-family|font:" app.css` und `grep -nE "'[0-9]+px|[0-9]{4,}|setTimeout|setInterval" app.js`. Werte innerhalb der `:root`-Blöcke (Z. 2–24) sind Tokens und nicht gelistet. Gruppiert nach Wert; „Vorschlag" verweist auf Abschnitt 6.

### 3.1 app.css — Farben außerhalb `:root`

| Datei:Zeile | Wert | Verwendung | Vorschlag |
|---|---|---|---|
| app.css:84, 85 | `#fff` | `.btn.primary` Textfarbe light/system | `--on-accent` |
| app.css:83 | `#0f1115`, `#5aa9ff` (Kommentar) | Kontrast-Notiz | entfällt mit `--on-accent` |
| app.css:60, 221 | `rgba(0,0,0,.35)` | Popover-/Toast-Schatten | `--shadow-2` |
| app.css:228 | `rgba(0,0,0,.5)` | Dialog-Schatten | `--shadow-3` |
| app.css:229 | `rgba(0,0,0,.55)` | Dialog-Backdrop | `--backdrop` |
| app.css:45, 46, 74, 76, 146-148, 233 | `color-mix(… 50%, transparent)` / `12%` | Badge-Rahmen/-Hintergrund | `--tint-border` (50 %), `--tint-bg` (12 %) via `@property` oder feste Tokens `--ok-soft` … |
| app.css:144, 145 | `40%` / `10%` | `.m-auto`, `.m-manual` | dieselben Tokens (Abweichung 40/10 vs. 50/12 vereinheitlichen) |
| app.css:70, 71, 106, 108 | `18%`, `16%`, `14%`, `14%` | Banner/Notice-Hintergrund | `--tint-strong` (16 %) |
| app.css:216 | `6%` | aktive Tabellenzeile | `--tint-row` |
| app.css:280 | `22%` | `.btn.on` | `--tint-active` |
| app.css:89 | `opacity:.5` | `:disabled` | `--opacity-disabled` |
| app.css:141 | `opacity:.7` | Zielmarke im Duty-Balken | `--opacity-marker` |
| app.css:59 | `opacity:.35` | Pulse-Keyframe | `--opacity-pulse` |

### 3.2 app.css — Schriftgrößen, Familien, Zeilenhöhen

| Datei:Zeile | Wert | Verwendung | Vorschlag |
|---|---|---|---|
| app.css:27 | `font:14px/1.45 system-ui,-apple-system,"Segoe UI",Roboto,Ubuntu,sans-serif` | body | `--font-sans`, `--fs-14`, `--lh-body` |
| app.css:29 | `ui-monospace,SFMono-Regular,Menlo,Consolas,monospace` | code/pre/.mono | `--font-mono` |
| app.css:247 | `font:12px/1.4 ui-monospace,…` | Dialog-Textarea | `--font-mono`, `--fs-12`, `--lh-tight` |
| app.css:325 | `ui-monospace,monospace` | Preset-Punkte (abweichender Stack!) | `--font-mono` |
| app.css:74 | `10px` | `.badge.beta` | `--fs-10` (prüfen: zu klein, → `--fs-11`) |
| app.css:44, 143, 217, 241, 272 | `11px` | badge, mode, `.tbl .act`, Fingerprint, `.sg h3` | `--fs-11` |
| app.css:43, 47, 54, 64, 72, 88, 95, 127, 133, 135, 156, 161, 167, 184, 189, 193, 202, 208, 209, 214, 239, 246, 262, 265, 276, 277, 294, 321 | `12px` | meta, chip, live, hint.sm, badge-code, btn.sm, seg, sub, row-k, tg, legend, tip, chips, ed-label, ed-table, ref, mn-warn, ps-sum, log, th, fp, file-input, h3, tbl.sm, sn-id/-d, addp, pd | `--fs-12` |
| app.css:28, 61, 70, 97, 102, 105, 106, 132, 169, 171, 177, 200, 212, 221, 245, 248, 249, 255, 274, 300 | `13px` | h2, popover-label, banner, hint, chk, inline-label, notice, ch-row, kv, alerts, empty, mn-small, tbl, toast, sub-label, summary, ol, login-label, sn, desc | `--fs-13` |
| app.css:223 | `14px` | Toast-× | `--fs-14` |
| app.css:282 | `14px` | Legenden-× | `--fs-14` |
| app.css:125, 129 | `15px` | `.name`, temp-Einheit | `--fs-15` |
| app.css:41, 87 | `16px` | brand, `.btn.icon` | `--fs-16` |
| app.css:232, 319 | `18px` | Dialog-Icon, About-Titel | `--fs-18` |
| app.css:199 | `28px` | Manual-Wert | `--fs-28` |
| app.css:128 | `40px` | Temperatur | `--fs-40` |
| app.css:81 | `line-height:1.3` | `.btn` | `--lh-tight` |
| app.css:103 | `line-height:1.35` | `.chk.warn` | `--lh-tight` |
| app.css:128 | `line-height:1.05` | temp | `--lh-display` |
| app.css:199 | `line-height:1` | Manual-Wert | `--lh-display` |
| app.css:209, 241 | `line-height:1.5` | log, fp | `--lh-loose` |
| app.css:28, 143, 214, 262, 272 | `letter-spacing:.04em` | Uppercase-Labels | `--ls-caps` |
| app.css:41 | `.02em` | brand | `--ls-brand` |
| app.css:128 | `-.02em` | temp | `--ls-display` |
| app.css:112, 190, 214 | `font-weight:500` | tabs, ed-td, th | `--fw-medium` |
| app.css:28, 44, 47, 125, 128, 143, 174, 217, 262, 272 | `600` | h2, badge, chip, name, temp, mode… | `--fw-semibold` |
| app.css:41 | `700` | brand | `--fw-bold` |

### 3.3 app.css — Abstände (Padding, Gap, Margin)

| Datei:Zeile | Wert | Verwendung | Vorschlag |
|---|---|---|---|
| app.css:74, 325 | `1px 6px` | beta-Badge, Preset-Punkt-Padding | `--sp-0 --sp-1` (Raster 4 px: 0/4/8/12/16/24) |
| app.css:44, 66, 143, 190, 223 | `2px …` (2px 8px, 2px 4px, 2px 6px 2px 0, 2px 6px) | badge, sec-Button, mode, ed-td, toast-× | `--sp-05` (2 px) als Ausnahme oder auf 4 px runden |
| app.css:62, 99, 141, 163, 257, 272, 299 | `2px`, `3px` (margin) | hr, marker, tip-t, chk-input, sg-h3, ps-sum | `--sp-05` |
| app.css:47, 88, 90, 95, 167, 191, 295 | `3px 10px`, `3px 8px`, `5px 8px`, `3px 6px` | chip, btn.sm, input, seg, chips, ed-input, addp-input | `--sp-1 --sp-2`; Input-Padding einheitlich `--sp-1 --sp-2` |
| app.css:87, 241 | `4px 8px` | btn.icon, fp-code | `--sp-1 --sp-2` |
| app.css:60, 115, 136, 137, 143, 172, 255, 274, 282, 321, 324 | `4px` (gap/padding/radius/top) | popover-top, tabs-focus-radius, bar-h, alerts-li, login-gap, sn, x, pd, pts | `--sp-1` / `--r-sm` |
| app.css:49, 50 | `0 0 6px` | Chip-Glow | `--glow` |
| app.css:47, 54, 57, 73, 102, 126, 127, 154, 157, 166, 184, 200, 217, 249, 294, 298 | `6px` (gap/margin) | chip, live, ver, chk, dot, sub, card-h, legend, chips, ed-label, mn-small, act, ol, addp, ps-name | `--sp-15` (6 px) oder auf 4/8 runden |
| app.css:81, 161 | `6px 12px`, `6px 8px` | btn, tip | `--sp-15 --sp-3` |
| app.css:70, 106, 123, 124, 154, 162, 172, 183, 201, 205, 206, 221, 239, 242, 244, 245, 261, 269, 292, 293 | `8px` / `8px 16px` / `8px 12px` / `8px 14px` / `8px 32px 8px 12px` | banner, notice, ch, ch-top, card-h, tip-div, alerts-li, ed-top, mn-act, list, ps, toast, fp, dlg-act, sub, sub-label, act, ov-card-h, addc, addp | `--sp-2` |
| app.css:38, 39, 61, 78, 98, 112, 182, 197, 213, 230, 254, 274, 305, 311 | `10px` / `10px 16px` / `10px 12px` | hdr, hdr-l/r, popover-label, s-account, bar, tabs-btn, ed, mn, tbl-td, dlg-h, login-f, sn, al, sy | `--sp-25` (10 px) oder auf 8/12 runden |
| app.css:28, 272 | `0 0 10px`, `10px 0 2px` | h2, sg-h3 | `--sp-25` |
| app.css:35, 60, 98, 106, 156, 169, 209, 213, 230, 235, 244, 263, 317, 322 | `12px` | main-mobile, popover, bar-mb, notice-mb, legend, kv, log, tbl, dlg-h-mb, ct-body, sub, ac-body, about, pc | `--sp-3` (= `--gap`) |
| app.css:154, 158, 183, 192, 206 | `14px` | card-h gap, legend-i width, ed-top, pts, ps | `--sp-35` oder 12/16 |
| app.css:118 | `14px` | card padding | `--card-pad` (→ 16 px) |
| app.css:34, 38, 60, 70, 111, 220, 228 | `16px` | main, hdr, popover-right, banner, tabs, toasts, dlg | `--sp-4` |
| app.css:249, 321 | `20px`, `18px` | ol padding-left, credits | `--sp-5` |

### 3.4 app.css — Radien, Rahmen, Schatten, Breiten, Höhen, Z-Index, Breakpoints, Zeit

| Datei:Zeile | Wert | Verwendung | Vorschlag |
|---|---|---|---|
| app.css:158 | `border-radius:1px` | Legenden-Strich | `--r-xs` |
| app.css:66, 115, 136, 137, 143, 282, 325 | `4px` | sec-btn, tabs-focus, bar-h, mode, x, preset-pt | `--r-sm` |
| app.css:81, 90, 94, 161, 167, 188, 221, 241, 247, 293, 312 | `6px` | btn, input, seg, tip, chips, ed-canvas, toast, fp, textarea, addp, sy-tbl | `--r-md` |
| app.css:60, 106, 118, 209, 228, 244 | `var(--r)` = 8 px | popover, notice, card, log, dlg, sub | `--r-lg` (bestehend) |
| app.css:44, 47 | `99px` | badge, chip | `--r-pill` |
| app.css:48, 55, 126 | `50%` | Punkte | `--r-full` |
| app.css:38, 44, 60, 62, 70, 81, 90, 94, 106, 111, 118, 143, 161, 167, 172, 209, 213, 221, 228, 241, 244, 247, 274, 293, 312, 321, 325 | `1px solid` | alle Rahmen | `--bw` |
| app.css:112 | `2px solid` | Tab-Unterstrich | `--bw-accent` |
| app.css:221 | `3px solid` | Toast-Akzent links | `--bw-accent` |
| app.css:141 | `width:2px; height:12px; top:-2px` | Zielmarke | `--marker-w`, `--marker-h` |
| app.css:48, 55, 126 | `8px` | Status-/Serienpunkt | `--dot` |
| app.css:158, 165 | `14px×2px`, `10px×2px` | Legenden-Strich, Tooltip-Strich (abweichend) | `--swatch-w`, `--swatch-h` |
| app.css:32 | `1px` | `.sr` | Standard-Snippet, bleibt |
| app.css:42 | `max-width:40vw` | Profilname | `--hdr-title-max` |
| app.css:60 | `min-width:220px` | Popover | `--popover-w` |
| app.css:99, 208, 207, 264, 284, 313, 323 | `min-width:180px/160px/100px/140px/56px/240px/36px` | bar-grow, ps-sum, ps-name, sub-input, alerts-k, sy-hint, pc-b | `--w-field`, `--w-label` |
| app.css:91, 186, 191, 295, 308 | `5.5em`, `5em`, `4.5em`, `4.5em`, `12em` | number-input, stop, ed-input, addp-input, mailto | `--w-num`, `--w-num-sm`, `--w-text` |
| app.css:132 | `52px 1fr auto` | ch-row Spalten | `--w-rowlabel` |
| app.css:134 | `min-width:88px` | ch-row Wert | `--w-value` |
| app.css:185 | `max-width:170px` | Sensor-Select | `--w-select` |
| app.css:34 | `max-width:1500px` | main | `--content-max` |
| app.css:181 | `minmax(460px,1fr)` | Editor-Grid | `--editor-min` |
| app.css:220 | `min(360px,calc(100vw - 32px))` | Toasts | `--toast-w` |
| app.css:228, 253 | `min(640px,…)`, `min(380px,…)` | dlg, dlg.narrow | `--dlg-w`, `--dlg-w-narrow` |
| app.css:228 | `calc(100vh - 32px)` | dlg max-height | `--dlg-max-h` |
| app.css:123, 124, 132, 202 | `min-height:196px/24px`, `height:20px`, `min-height:18px` | ch, ch-top, ch-row, mn-warn | `--card-min-h`, `--row-h` |
| app.css:128 | `height:44px` | temp-Zeile | `--display-h` |
| app.css:136 | `height:8px` | Duty-Balken | `--bar-h` |
| app.css:159, 187 | `height:220px`, `200px` | Chart, Kurven-Canvas | `--chart-h`, `--curve-h` |
| app.css:209 | `min(70vh,640px)` | Log-Höhe | `--log-h` |
| app.css:270, 271 | `1.35fr 1fr` | Overview-Grid | `--ov-cols` |
| app.css:38, 161, 220 | `z-index:20`, `5`, `30` | hdr, tip, toasts (Dialog = Top-Layer) | `--z-header`, `--z-tip`, `--z-toast` |
| app.css:35 | `max-width:480px` | main-Padding mobil | `--bp-xs` |
| app.css:121, 270 | `min-width:700px` | 2 Spalten | `--bp-sm` |
| app.css:304 | `min-width:900px` | grid2 | `--bp-md` |
| app.css:122, 153, 181, 271 | `min-width:1100px` | 3 Spalten, Charts, Editoren | `--bp-lg` |
| app.css:58 | `pulse 2s infinite` | Live-Punkt | `--dur-pulse` |
| app.css:137 | `transition:width .5s` | Duty-Balken | `--dur-bar` |
| app.css:224 | `animation:in .15s ease-out` | Toast | `--dur-fast`, `--ease` |
| app.css:225 | `translateY(6px)` | Toast-Einblendung | `--sp-15` |
| app.css:8 | `0 0 0 2px … 0 0 0 4px` | Fokusring (bereits Token, Werte hart) | `--focus-w:2px`, `--focus-gap:2px` |
| app.css:7, 16, 23 | `0 1px 2px` | `--sh` | `--shadow-1` |
| app.css:60, 221 | `0 8px 24px` | popover/toast | `--shadow-2` |
| app.css:228 | `0 16px 48px` | dlg | `--shadow-3` |

### 3.5 app.js — Canvas, Geometrie, Zeit, Grenzen

| Datei:Zeile | Wert | Verwendung | Vorschlag |
|---|---|---|---|
| app.js:264 | `'12px system-ui'` | „no history"-Text | `font(--fs-12)` aus `--font-sans` |
| app.js:275, 591 | `'11px system-ui'` | Achsenbeschriftung | `font(--fs-11)` |
| app.js:298 | `'600 11px system-ui'` | Wertelabels rechts | `font(--fs-11, --fw-semibold)` |
| app.js:262 | `pad = { l: 40, r: 58, t: 8, b: 22 }` | Chart-Innenabstand | `CHART.pad` |
| app.js:583 | `pad = { l: 34, r: 12, t: 10, b: 22 }` | Kurven-Canvas (abweichend) | `CURVE.pad` |
| app.js:265, 311, 784 | `7200` | Chart-Fenster 2 h | `CHART.windowS` |
| app.js:282 | `1800` | X-Gitter alle 30 min | `CHART.gridS` |
| app.js:270 | `.15`, `minSpan || 10` | Y-Rand, Mindestspanne | `CHART.yMargin`, `CHART.minSpan` |
| app.js:271 | `/ 5) * 5` | Y-Rundung | `CHART.yRound` |
| app.js:279, 284, 299 | `- 6`, `+ 6`, `+ 6` | Label-Offsets | `CHART.labelGap` |
| app.js:291, 601 | `lineWidth = 2` | Linien | `CHART.lineW` |
| app.js:294, 602 | `globalAlpha .08` / `.1` (abweichend) | Flächenfüllung | `CHART.fillAlpha` |
| app.js:297 | `< 13`, `+ 13` | Label-Kollisionsabstand | `CHART.labelH` |
| app.js:301, 595, 606 | `[3,3]`, `[4,3]`, `[2,3]` | Strichmuster Hover/crit/now (drei verschiedene) | `CHART.dash` |
| app.js:316 | `+ 12`, `- 12`, `- 4` | Tooltip-Versatz | `CHART.tipGap` |
| app.js:326 | `4.5`, `lineWidth 2`, `arc … 7` | Punkt-Radius/Rand | `CHART.dotR`, `CHART.dotStroke` |
| app.js:345 | `.6`, `.85`, `75` | Temperaturfarbe-Schwellen, Default-crit | `THRESH.warm`, `THRESH.hot`, `DEFAULT_CRIT` |
| app.js:347 | `60` | Default `stall_min_duty` | `DEFAULT_STALL_MIN` |
| app.js:510 | `minSpan: 15` | Temperatur-Chart | `CHART.minSpanTemp` |
| app.js:515 | `minSpan: 1000`, `yMax: 255` | RPM/Duty-Chart | `CHART.minSpanRpm`, `DUTY_MAX` |
| app.js:539 | `min: 30, max: 120` | critical-Feld | `LIMITS.critical` |
| app.js:546, 547, 555 | `0..120`, `0..255` | Punktfelder | `LIMITS.temp`, `LIMITS.duty` |
| app.js:564 | `+ 5`, `40` | Add-Point-Vorschlag | `CURVE.addStep`, `CURVE.addDefault` |
| app.js:584 | `100`, `+ 10` | X-Achsenmaximum | `CURVE.xMin`, `CURVE.xPad` |
| app.js:592 | `+= 51` | Y-Gitter (20 %) | `CURVE.yStep` |
| app.js:594 | `+= 20` | X-Gitter 20° | `CURVE.xStep` |
| app.js:596, 608 | `x - 3`, `±8`, `- 6` | Textversatz | `CHART.labelGap` |
| app.js:614 | `best = 14` | Drag-Trefferradius | `CURVE.hitR` (Touch: 22) |
| app.js:624 | `< 2 || > 8` | Punktanzahl | `LIMITS.points` |
| app.js:549 | `>= 8` | Add-Button | `LIMITS.points.max` |
| app.js:496 | `>= 8` | Sensor-Chart-Limit | `LIMITS.sensors` (Serverwert übernehmen) |
| app.js:651 | `min: 0, max: 255` | Slider | `DUTY_MAX` |
| app.js:681 | `{1,64}` | Preset-Name | `LIMITS.presetName` |
| app.js:61 | `12000`, `5000` | Toast err/ok | `TIMING.toastErr`, `TIMING.toast` |
| app.js:849, 852, 910, 929 | `12000`, `15000`, `8000`, `8000` | Toast-Sonderdauern | `TIMING.toastLong` |
| app.js:95 | `30000` | Blob-URL-Revoke | `TIMING.blobRevoke` |
| app.js:803 | `S.interval * 1000` | State-Poll | `TIMING.poll` (aus Settings) |
| app.js:803 | `30000` | History-Poll | `TIMING.history` |
| app.js:804 | `60000` | Alerts-Poll | `TIMING.alerts` |
| app.js:805 | `30000` | System-Poll | `TIMING.system` |
| app.js:743 | `lines=200` | Log-Zeilen | `LOG.lines` |
| app.js:784 | `720` | History-Punkte-Cap | `CHART.maxPoints` |
| app.js:843 | `1 << 20` | Import-Limit 1 MiB | `LIMITS.importBytes` |
| app.js:917 | `65536` | PEM-Datei 64 KiB | `LIMITS.pemBytes` |
| app.js:336 | `86400e3` | Tage-Rest | `MS_DAY` |
| app.js:37-39 | `60`, `3600`, `86400` | Relativzeit | `S_MIN`, `S_HOUR`, `S_DAY` |
| app.js:889 | `< 30` | „soon"-Schwelle Zertifikat | `CERT.soonDays` |
| app.js:138 | `120` | Mock-Latenz | nur Mock |

### 3.6 index.html — Attribute und Texte mit Zahlen

| Datei:Zeile | Wert | Verwendung | Vorschlag |
|---|---|---|---|
| index.html:32 | `5`, `10`, `30` (s) | Refresh-Optionen | aus `TIMING.pollOptions` rendern |
| index.html:71, 75, 85 | „last 2 h" | Chart-Titel | aus `CHART.windowS` |
| index.html:89 | „(max 8)" | Sensors-Hint | aus `LIMITS.sensors` |
| index.html:98 | „2–8 points" | Curves-Hint | aus `LIMITS.points` |
| index.html:112 | `pattern="[a-z0-9_\-]{1,64}" maxlength="64"` + title „at most 64" | Preset-Name | `LIMITS.presetName` |
| index.html:125 | `maxlength="128"` | Mail to | `LIMITS.mailTo` |
| index.html:147 | „10 minutes", „30 s" | System-Hint | aus `TIMING.system` / Server |
| index.html:210 | „30 days" | Remember me | `AUTH.rememberDays` |
| index.html:225, 227 | „8–128", `minlength="8" maxlength="128"` | Passwort | `LIMITS.password` |
| index.html:232, 234 | „1–32", `pattern="[A-Za-z0-9_.\-]{1,32}" maxlength="32"` | User | `LIMITS.user` |
| index.html:266, 268 | `rows="3"` | PEM-Textareas | `--textarea-rows` |

## 4. Klickweg-Tabelle der Hauptaufgaben

Klicks ab Startzustand (eingeloggt auf Overview, sofern nicht anders); Tipp-Eingaben nicht gezählt.

| Aufgabe | Klicks | Rückmeldung | Befund-Nr. |
|---|---|---|---|
| (a) Erster Besuch → Temperaturen sehen | 0 (Karten sofort sichtbar, 3 Kanäle + 2 Charts anonym) | Live-Werte, Chip „ok", „live"-Punkt | — |
| (a) Anmelden | 2 (Sign in → Sign in) | Toast „Signed in", 7 Tabs erscheinen, Header zeigt User; falsches Passwort: Notice „invalid user or password", Feld geleert, Fokus zurück | B35 (Fokus nach Login auf BODY) |
| (b) Kurve ändern und anwenden | 2–3 (Tab Curves → Punkt ziehen/Feld → Apply) | Toast „Curves applied"; Fehler: rote Notice mit Servertext + Toast „Rejected"; Revert: Toast „Reverted" | B04, B25, B29, B31 |
| (c) Lüfter manuell setzen | 2 (Tab Manual → Slider → Set) | Toast „cpu: manual 128 (50 %)", Badge MANUAL in Manual und Overview | B05 |
| (c) Zurück auf Auto | 1 (Back to auto) | Toast „cpu: back to auto", Badge AUTO | — |
| (d) Preset anwenden | 2 + `confirm()` | Toast „Preset summer applied" + ggf. Notice „restart required" | B02, B03, B11 |
| (d) Preset speichern | 1 + Name + 1 (Save) | Toast „Saved current curves as “summer2”", Liste aktualisiert | B09 |
| (d) Preset umbenennen | 1 + `prompt()` | Toast „Preset renamed to “winter”" | B02, B22 |
| (d) Preset löschen | 1 + `confirm()` | Toast „Preset winter deleted" | B02 |
| (e) Alarm testen | 2 (Tab Alerts → Send test alert) | Toast „Test alert sent via pve-notify", Eintrag „test … 0 s ago" in Recent alerts; Fehler (mail): err-Toast „Test alert failed (mail): mail: exit status 127" | B06, B29 |
| (e) Transport ändern | 2 (Select → Save) | Toast „Transport saved — effective: mail", Status-Liste aktualisiert | B06 |
| (f) Zertifikat herunterladen | 2 (Header TLS → Download .cer) oder 3 über ⚙ → Certificate… | Nur Browser-Download-Leiste, kein Toast | B08 |
| (f) Vertrauen (Anleitung) | +1 (How to trust aufklappen) | 4 OS-Schritte als Text | B24 (kein Primärweg je OS) |
| (g) Passwort ändern | 3 (⚙ → Account… → Change password…) + 3 Felder + 1 (Change password) | Toast „Password changed", Formular zu, Dialog bleibt offen; Fehler: Notice „current password wrong"/„new password: 8–128 characters", Fokus ins Feld | B29, B40 |
| (h) Sensor als Extra-Chart | 1 (chart) nach Scroll zu Sensors | Button-Zustand „on", Extra-Chart-Legende +1, kein Toast | B07 |
| (h) Sensor entfernen | 1 (× in Legende, 16×14 px) oder 1 (chart erneut) | Legende −1, kein Toast | B07, B37 |
| (i) Log exportieren | 2 (Tab Log → Export) | Nur Browser-Download | B08, B10 |
| (j) Einstellungen exportieren | 2 (⚙ → Export settings) | Nur Browser-Download, Popover schließt | B08 |
| (j) Einstellungen importieren | 2 (⚙ → Import settings…) + Dateidialog + `confirm()` | Toast „Settings imported" bzw. warn-Toast + Banner „restart required" (bleibt) | B02, B28 |
| (k) Abmelden | 1 (Sign out) | Toast „Signed out" (neutral), Tabs auf 2 reduziert, Overview aktiv | B35 (Fokus auf BODY) |

## 5. Kontrast-Tabelle

Rechnerisch aus den Tokens; `mix12` = `color-mix(in srgb, Farbe 12%, --bg2)`. AA: 4,5:1 Text < 18 pt/14 pt fett, 3:1 großer Text und Grafik/UI-Komponenten.

| Paar | Verwendung (Größe) | Dark | Light | AA? |
|---|---|---|---|---|
| `--fg` / `--bg2` | Fließtext 13–14 px | 14,34 | 18,15 | ja / ja |
| `--fg2` / `--bg2` | Hints, Tabs inaktiv, Labels 12–13 px | 7,54 | 7,68 | ja / ja |
| `--fg2` / `--bg3` | Seg-Button, Toast-×, builtin-Badge | 6,90 | 6,72 | ja / ja |
| `--fg3` / `--bg2` | hint.sm, `dt`, `th`, Achsen, Legenden-× (11–13 px) | **3,69** | **3,83** | **nein / nein** (B32) |
| `--fg3` / `--bg` | Kurven-Canvas-Achsen, `.addp`-Labels | 4,00 | **3,51** | nein / nein |
| `--ok` / `--bg2` | Temperatur 40 px (groß), Alert-Kind 13 px, `sec.ok` 12 px, `.tbl .act` 11 px | 9,78 | **3,51** | ja / nur groß |
| `--warn` / `--bg2` | t-warm 40 px, Manual-Warnung 12 px, `.log .w` 12 px, Alert-Kinds | 7,72 | **3,64** | ja / nur groß |
| `--hot` / `--bg2` | t-hot 40 px | 6,22 | 5,18 | ja / ja |
| `--crit` / `--bg2` | t-crit, Alert-Kinds 13 px, `.btn.danger`, `.log .e` | 4,64 | 4,83 | ja (knapp) / ja |
| `--stall` / `--bg2` | Alert-Kind stall | 6,75 | 5,70 | ja / ja |
| `--info` / `--bg2` | Links, Alert-Kind test, Fokusring | 7,11 | 4,95 | ja / ja |
| `--man` / `--bg2` | Slider-Akzent | 7,11 | 4,95 | ja / ja |
| `.badge.ok` Text auf mix12 | „verified on hardware" 11 px | 7,72 | **3,05** | ja / **nein** (B33) |
| `.badge.warn` Text auf mix12 | „from documentation" 11 px | 6,32 | **3,19** | ja / nein |
| `.badge.beta` (`--stall`) auf mix12 | „BETA" 10 px | 5,57 | 4,75 | ja / ja |
| `.badge.builtin` `--fg2`/`--bg3` | „built-in" 11 px | 6,90 | 6,72 | ja / ja |
| `.mode.m-auto` `--ok` auf mix10 | „AUTO" 11 px | 8,09 | **3,12** | ja / nein |
| `.mode.m-manual` `--man` auf mix10 | „MANUAL" 11 px | 6,06 | **4,33** | ja / nein |
| `.mode.m-critical` `--crit` auf mix12 | „CRITICAL" 11 px | **4,14** | **4,01** | **nein / nein** (B34) |
| `.chip.sensor-error` `--crit`/`--bg3` | Header-Chip 12 px | **4,24** | **4,23** | nein / nein |
| `.chip.dry-run` `--warn`/`--bg3` | Header-Chip 12 px | 7,07 | **3,19** | ja / nein |
| `.btn.primary` Text | `#0f1115`/`--info` bzw. `#fff`/`--info` | 7,70 | 4,95 | ja / ja |
| `.btn` Text `--fg`/`--bg3` | Standard-Button | 13,12 | 15,89 | ja / ja |
| `.banner` `--fg` auf crit18 | Verbindungsverlust | 11,85 | 13,69 | ja / ja |
| `.notice` `--fg` auf warn14 / `.notice.err` auf crit14 | Notices | 11,25 / 12,51 | 15,52 / 14,62 | ja / ja |
| Input-Rahmen `--line2`/`--bg` | Feldgrenze (UI-Komponente, 3:1) | **1,65** | **1,46** | **nein / nein** (B36) |
| Button-Rahmen `--line2`/`--bg2` | Buttongrenze | 1,52 | 1,60 | nein / nein (Füllung `--bg3` hilft: 1,2) |
| Kartenrahmen `--line`/`--bg` | Dekoration | 1,35 | 1,20 | n. z. (dekorativ) |
| Fokusring `--info`/`--bg2` | 2 px + 2 px Gap | 7,11 | 4,95 | ja / ja |
| Serie `--s1` … `--s8` / `--bg2` | Linien 2 px (3:1) und Wertelabels 11 px fett (4,5:1) | 4,79 · 4,49 · 5,12 · 5,68 · 4,42 · 5,58 · 5,40 · **3,53** | 4,42 · **3,20** · **2,82** · **2,17** · **2,69** · 8,56 · 3,95 · 4,95 | Linien dark ja, light **s3/s4/s5 nein**; Labels dark s2/s5/s8 nein, light s2–s5/s7 nein |

## 6. Vorschlag: einheitliches Design-Schema

### 6.1 `:root`-Block

```css
:root{
  /* Farben — semantisch (dark) */
  --bg:#0f1115; --surface:#161a21; --surface-2:#1d222b;
  --line:#262c37; --line-strong:#4a5566;            /* B36: ≥3:1 auf --bg */
  --text:#e6e9ef; --text-2:#a3abb8; --text-3:#8b94a3; /* B32: fg3 ≥4,5:1 */
  --ok:#3ddc84; --warn:#e3a008; --hot:#f97316; --crit:#f87171; /* B34 */
  --stall:#b48ef5; --info:#5aa9ff; --accent:var(--info); --on-accent:#0f1115;
  --s1:#3987e5; --s2:#4d95ea; --s3:#199e70; --s4:#c98500;
  --s5:#e06a95; --s6:#9085e9; --s7:#e66767; --s8:#22a03a;
  --tint-bg:12%; --tint-border:50%; --tint-strong:16%; --tint-row:6%; --tint-active:22%;
  --backdrop:rgba(0,0,0,.55);
  --shadow-1:0 1px 2px rgba(0,0,0,.4); --shadow-2:0 8px 24px rgba(0,0,0,.35); --shadow-3:0 16px 48px rgba(0,0,0,.5);
  --glow:0 0 6px;
  --opacity-disabled:.5; --opacity-marker:.7; --opacity-pulse:.35;

  /* Typografie */
  --font-sans:system-ui,-apple-system,"Segoe UI",Roboto,Ubuntu,sans-serif;
  --font-mono:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;
  --fs-11:11px; --fs-12:12px; --fs-13:13px; --fs-14:14px; --fs-15:15px;
  --fs-16:16px; --fs-18:18px; --fs-20:20px; --fs-28:28px; --fs-40:40px;
  --lh-display:1.05; --lh-tight:1.3; --lh-body:1.45; --lh-loose:1.5;
  --fw-regular:400; --fw-medium:500; --fw-semibold:600; --fw-bold:700;
  --ls-caps:.04em; --ls-brand:.02em; --ls-display:-.02em;

  /* Abstände (4-px-Raster) */
  --sp-05:2px; --sp-1:4px; --sp-15:6px; --sp-2:8px; --sp-25:10px;
  --sp-3:12px; --sp-4:16px; --sp-5:20px; --sp-6:24px; --sp-8:32px;
  --gap:var(--sp-3); --card-pad:var(--sp-4); --page-pad:var(--sp-4); --page-pad-xs:var(--sp-3);

  /* Radien, Rahmen */
  --r-xs:1px; --r-sm:4px; --r-md:6px; --r-lg:8px; --r-pill:99px; --r-full:50%;
  --bw:1px; --bw-accent:2px; --bw-toast:3px;

  /* Fokus, Übergänge */
  --focus-w:2px; --focus-gap:2px;
  --focus:0 0 0 var(--focus-gap) var(--bg),0 0 0 calc(var(--focus-gap) + var(--focus-w)) var(--info);
  --dur-fast:.15s; --dur-bar:.5s; --dur-pulse:2s; --ease:ease-out;

  /* Z-Index */
  --z-tip:5; --z-header:20; --z-toast:30;

  /* Größen */
  --dot:8px; --swatch-w:14px; --swatch-h:2px; --bar-h:8px; --marker-w:2px; --marker-h:12px;
  --chart-h:220px; --curve-h:200px; --log-h:min(70vh,640px);
  --content-max:1500px; --editor-min:460px; --popover-w:220px;
  --toast-w:min(360px,calc(100vw - 2*var(--sp-4)));
  --dlg-w:min(640px,calc(100vw - 2*var(--sp-4))); --dlg-w-narrow:min(380px,calc(100vw - 2*var(--sp-4)));
  --dlg-max-h:calc(100vh - 2*var(--sp-4));
  --w-num:5.5em; --w-num-sm:4.5em; --w-text:12em; --w-select:170px; --w-rowlabel:52px; --w-value:88px;
  --hit-min:24px; --hit-touch:44px;
  color-scheme:dark;
}
:root[data-theme=light],
:root[data-theme=system]{ /* letzterer nur unter @media (prefers-color-scheme:light) */
  --bg:#f4f5f7; --surface:#ffffff; --surface-2:#eef0f3;
  --line:#dde1e7; --line-strong:#8e97a6;
  --text:#12161c; --text-2:#4a5462; --text-3:#5f6875;
  --ok:#0b7a44; --warn:#8a5a0f; --hot:#c2410c; --crit:#dc2626;
  --stall:#7c3aed; --info:#1d6fd1; --on-accent:#fff;
  --s1:#2a78d6; --s2:#c4501f; --s3:#0f8a5f; --s4:#a86f00;
  --s5:#c2477f; --s6:#4a3aa7; --s7:#c53030; --s8:#008300;
  --shadow-1:0 1px 2px rgba(0,0,0,.08); --shadow-2:0 8px 24px rgba(0,0,0,.12); --shadow-3:0 16px 48px rgba(0,0,0,.18);
  color-scheme:light;
}
```

Breakpoints lassen sich in CSS nicht als Custom Properties in `@media` nutzen; sie gehören als Kommentarblock an den Dateikopf und in `UI.bp` (JS): `xs 480 · sm 700 · md 900 · lg 1100`. Empfehlung: `sm` auf 640 und `lg` auf 960 senken (B14).

### 6.2 JS-Konstantenobjekt (`app.js`, direkt nach `cssVar`)

```js
const UI = Object.freeze({
  bp: { xs: 480, sm: 700, md: 900, lg: 1100 },
  timing: { toast: 5000, toastErr: 12000, toastLong: 15000, toastNotice: 8000,
            history: 30000, alerts: 60000, system: 30000, blobRevoke: 30000, pollOptions: [5, 10, 30] },
  chart: { windowS: 7200, gridS: 1800, maxPoints: 720, yMargin: .15, yRound: 5,
           minSpanTemp: 15, minSpanRpm: 1000, pad: { l: 40, r: 58, t: 8, b: 22 },
           lineW: 2, fillAlpha: .08, dotR: 4.5, dotStroke: 2, labelH: 13, labelGap: 6, tipGap: 12,
           dash: { hover: [3, 3], crit: [4, 3], now: [2, 3] } },
  curve: { pad: { l: 34, r: 12, t: 10, b: 22 }, xMin: 100, xPad: 10, xMax: 130, xStep: 20, yStep: 51,
           hitR: 14, hitRTouch: 22, addStep: 5, addDefault: 40 },
  limits: { points: [2, 8], sensors: 8, duty: [0, 255], temp: [0, 120], critical: [30, 120],
            presetName: /^[a-z0-9_-]{1,64}$/, user: /^[A-Za-z0-9_.-]{1,32}$/, password: [8, 128],
            mailTo: 128, importBytes: 1 << 20, pemBytes: 65536 },
  thresh: { warm: .6, hot: .85, defaultCrit: 75, defaultStallMin: 60, certSoonDays: 30 },
  font: (size, weight) => `${weight ? weight + ' ' : ''}${cssVar(size)} ${cssVar('--font-sans')}`,
});
```

### 6.3 Migrationshinweis (welche Werte auf welches Token)

| Hart codiert | Token |
|---|---|
| `font:14px/1.45 system-ui,…` (css:27) | `font:var(--fs-14)/var(--lh-body) var(--font-sans)` |
| alle `font-family:ui-monospace…` (css:29, 247, 325) | `var(--font-mono)` |
| `11px/12px/13px/…` (Tabelle 3.2) | `var(--fs-11)` … `var(--fs-40)`; `10px` (beta) → `--fs-11` |
| `.04em` | `var(--ls-caps)` |
| `600`/`500`/`700` | `--fw-semibold/-medium/-bold` |
| `2px/4px/6px/8px/10px/12px/14px/16px` Abstände (3.3) | `--sp-05` … `--sp-4`; 14 px Card-Padding → `--card-pad` (16) |
| `border-radius:4px/6px/99px/50%` | `--r-sm/--r-md/--r-pill/--r-full`; `var(--r)` → `--r-lg` |
| `1px solid` | `var(--bw) solid` |
| `rgba(0,0,0,.35/.5/.55)` | `--shadow-2/--shadow-3/--backdrop` |
| `color-mix(… 12%/50%)` | `color-mix(in srgb,var(--ok) var(--tint-bg),transparent)`; `40%/10%` in `.m-auto/.m-manual` auf dieselben Tokens |
| `#fff` (css:84-85) | `var(--on-accent)` |
| `z-index:5/20/30` | `--z-tip/--z-header/--z-toast` |
| `transition:.5s`, `animation .15s`, `pulse 2s` | `--dur-bar/--dur-fast/--dur-pulse` |
| `height:220px/200px`, `min(70vh,640px)` | `--chart-h/--curve-h/--log-h` |
| `max-width:1500px`, `minmax(460px,…)`, `min-width:220px` | `--content-max/--editor-min/--popover-w` |
| `min(640px,…)`, `min(380px,…)`, `min(360px,…)` | `--dlg-w/--dlg-w-narrow/--toast-w` |
| `'11px system-ui'`, `'12px system-ui'`, `'600 11px system-ui'` (js) | `UI.font('--fs-11')`, `UI.font('--fs-12')`, `UI.font('--fs-11', 600)` |
| `pad = {…}` (js:262, 583) | `UI.chart.pad`, `UI.curve.pad` |
| `7200`, `1800`, `720` | `UI.chart.windowS/gridS/maxPoints` |
| `12000/5000/8000/15000/30000/60000` | `UI.timing.*` |
| `best = 14` | `ev.pointerType === 'touch' ? UI.curve.hitRTouch : UI.curve.hitR` |
| `min/max` in `h('input', …)` und `index.html` Attribute | aus `UI.limits` setzen (HTML: per JS nach Laden, oder Werte im HTML lassen und Test gegen `UI.limits` absichern) |
| „last 2 h", „(max 8)", „2–8 points", „30 days", „8–128", „1–32" | Texte per JS aus `UI` befüllen (`data-limit`-Attribute) |

## 7. Top 5 Verbesserungen

1. **Kontraste beider Themes auf AA heben (B32, B33, B34, B36).** Problem: Hint-/Label-Text (`--fg3`) liegt in beiden Themes unter 4,5:1, im Light-Theme fallen zusätzlich ok/warn-Badges, Mode-Badges und drei Serienfarben durch — das betrifft genau die Stellen, an denen der Admin Zustand abliest (verified, AUTO/STALL, Alarm-Kinds, Chartlinien). Nutzen: Lesbarkeit auf Laptop bei 150 % und im hellen Büro, keine Rate-Farben im Chart. Aufwand: klein — sechs Hex-Werte in `:root`, dann Kontrasttabelle erneut rechnen.
2. **Mobile Navigation und Header (B01, B13, B15).** Problem: auf dem Handy sind vier Tabs unerreichbar und ein Viertel des Bildschirms bleibt sticky belegt; auf dem Desktop verschwindet die Tab-Leiste beim Scrollen. Nutzen: der beworbene „Blick auf die Temperaturen" per Smartphone wird flüssig, Sektionen bleiben von überall erreichbar. Aufwand: mittel — Header unter 700 px auf zwei Zeilen kürzen (Meta in About/Menü), `.tabs` sticky mit Fade-Kante.
3. **Native `prompt`/`confirm` durch eigene Dialoge ersetzen und Rückmeldungen konsistent machen (B02, B03, B07, B08, B22).** Problem: sieben Aktionen laufen über Browser-Popups mit fremdem Look, zwei Aktionen melden gleichzeitig „applied" und „restart required", Downloads und Chart-Aufnahme melden nichts. Nutzen: eine Rückmeldesprache, kein Rätseln, ob die Aktion gewirkt hat. Aufwand: mittel — ein generischer `<dialog>` mit Promise-API (`ask(title, text, {input})`), Toast-Wortlaut-Regel, `act(..., 'msg')` nachziehen.
4. **Manual- und Alerts-Formulare fehlerfrei machen (B05, B06).** Problem: der Slider erlaubt Werte, die der Daemon ablehnt; der Transport „mail" ist wählbar, obwohl `mail(1)` fehlt — beides führt in eine Sackgasse mit rohem Fehlertext. Nutzen: der Fehlerfall tritt gar nicht ein, statt erklärt werden zu müssen. Aufwand: klein — `min` am Slider, `disabled`-Optionen im Select, Zahlenfeld neben dem Slider.
5. **Design-Tokens einführen (B20) und Fokus-/Zielgrößen nachziehen (B35, B37).** Problem: ~180 Streu-Werte, Canvas-Schrift unabhängig vom DOM, Fokus geht nach Login/Logout verloren, ×-Buttons 16×14 px. Nutzen: Theme- und Größenänderungen an einer Stelle, Tastatur- und Touch-Bedienung ohne Ausrutscher. Aufwand: mittel bis groß (mechanische Ersetzung, aber flächig); Fokus und Zielgrößen sind je eine Zeile.

## 8. Geprüft, ohne Befund

- Anonymer Erstbesuch: Temperaturen, Duty, RPM und beide Charts ohne Login sichtbar; Tab-Reihenfolge Sign in → Overview → RPM → Duty ist logisch; keine Konsolenfehler in allen Mock-Zuständen.
- Login-Dialog: `autocomplete`, `required`, Fehler „invalid user or password" mit Feldleerung und Fokus; Submit-Button während des Requests deaktiviert; Backdrop-Klick schließt.
- Tabs: `role=tablist/tab/tabpanel`, `aria-selected`, Roving-Tabindex, Pfeiltasten/Home/End funktionieren (gemessen), Fokusring innen sichtbar (`inset var(--focus)`), Deep-Link `&tab=` funktioniert.
- `:focus-visible` mit 2-px-Ring auf `--info` (7,1:1 dark / 4,95 light) auf allen Buttons/Feldern sichtbar.
- `prefers-reduced-motion`: Pulse und Toast-Einblendung sind unter `no-preference` gekapselt, Duty-Balken-Transition unter `reduce` aus — in der Pane mit aktivem `reduce` nachgewiesen (`animationName: none`).
- Kein horizontaler Seiten-Scroll bei 375, 640, 1024, 1280, 1920 px (`scrollWidth` = `innerWidth`); Tabellen scrollen innerhalb `.tbl-wrap`.
- Sensor-IDs und Beschreibungen mit `text-overflow:ellipsis`; Fingerprint mit `overflow-wrap:anywhere` umbrechend; `.kv dd` umbricht lange Pfade.
- Leerzustände vorhanden und lesbar: „no history", „No presets yet.", „no alerts", „alerts: unavailable", „no readable sensors", „no physical interfaces", „none found", „no SANs", „inventory: not available — see the System tab".
- Fehlerzustände vorhanden: Verbindungsbanner (`role=alert`, ab 2 Fehlern), „Session expired — sign in again" mit Rückfall auf anonym, Alerts-Tab `#al-notice` bei API-Fehler, System-Tab 501-Text, `syserr`-Notice + Overview-„notes"-Zeile, Cert 501/off/fallback/soon/expired jeweils mit Badge und Text.
- Deaktivierte Zustände korrekt: „remove" bei 2 Punkten, „+ add point" bei 8, „chart" bei 8 Sensoren, „Update template" bei `up to date` mit Begründungstext, „Clear" bei Journal-Quelle mit erklärendem `title`.
- Zertifikat-Dialog: Modus-Badge (automatic/own/fallback/off), SAN-Chips, Ablaufwarnung „in 12 days" farbig, Download-Buttons mit OS-Hinweis im `title`, Vier-OS-Anleitung, HSTS-Warnung beim Upload ohne passenden Host, „Back to auto" nur bei file/fallback.
- Account-Dialog: Session-Tabelle markiert „THIS SESSION", „remembered", Formulare mit `autocomplete=new-password`, Validierung 8–128 / 1–32 clientseitig, Fokus auf erstes Feld beim Öffnen.
- Settings-Popover: `aria-expanded`/`aria-controls`, Escape schließt und gibt Fokus zurück, Klick außerhalb schließt, Theme-/Einheit-/Intervallwechsel wirken sofort (Charts, Karten, Selects neu gerendert).
- Icon-Buttons haben Namen (`aria-label` Close/Dismiss/remove …, `.sr` „Settings"); Kurven-Canvas `role=img` + Label; Punktfelder mit `aria-label`.
- Theme-Wechsel light/dark/system: alle Farben laufen über Tokens, Canvas liest `cssVar` neu (`redrawAll`), `color-scheme` gesetzt, `<meta name="color-scheme">` vorhanden.
- 150 %-DPI-Äquivalent (1280×720): 3 Kanalkarten + 2 Charts nebeneinander vollständig sichtbar, Header einzeilig.

### Zusammenfassung und Go/No-Go

Befunde: **0 kritisch · 4 hoch (B01, B13, B32, B33) · 15 mittel · 33 niedrig** = 52.

Die Oberfläche ist als Operator-Konsole für den Desktop schlüssig: kurze Klickwege (jede Hauptaufgabe ≤ 3 Klicks), durchgehende Toast-/Notice-Rückmeldung, saubere ARIA-Grundstruktur, funktionierende Tastaturbedienung der Tabs und Dialoge, konsistente Tokens für Farben. Die vier hohen Befunde liegen an zwei Stellen: **Kontrast** (`--fg3` in beiden Themes, Light-Theme ok/warn/Serien) und **Smartphone** (Tab-Überlauf ohne Hinweis, 193 px sticky belegt). Beides sind Wertänderungen in `app.css` plus eine Header-Regel unter 700 px, kein Umbau.

**Go für das Design mit Auflagen:** B32/B33 (Farbwerte) und B01/B13 (Mobile-Header/Tabs) vor dem 0.3.0-Release beheben; B02–B06, B26, B35–B37 in den nächsten Beta-Zyklus; die Token-Migration (B20) als eigenes Refactoring ohne sichtbare Änderung.

## 9. Screenshots für die Benutzeranleitung (fehlend — es existiert kein `docs/screenshots/`)

| # | Ansicht | Zweck in der Anleitung | Zustand / Mock-URL | Viewport |
|---|---|---|---|---|
| 1 | Overview anonym | Erster Eindruck, was ohne Login sichtbar ist | `?mock=1` | 1280×720 dark |
| 2 | Login-Dialog | Anmeldung, „Remember me" erklären | `?mock=1` → Sign in klicken | 1280×720 |
| 3 | Overview eingeloggt, oben | Kanalkarten (Temp-Farbe, Duty-Balken mit Zielmarke, Modus-Badge), Charts, RPM/Duty-Umschalter | `?mock=1&user=1` | 1280×720 |
| 4 | Overview eingeloggt, unten | Extra-Sensoren-Chart, Sensors-Karte mit „chart"-Buttons, System-Kurzinfo, Recent alerts | `?mock=1&user=1`, gescrollt | 1280×720 |
| 5 | Header-Ausschnitt | Status-Chip, Uptime, Version/BETA, TLS-Button, live/paused, User, Sign out, ⚙ | `?mock=1&user=1` | Zoom auf Header |
| 6 | Settings-Popover | Einheit, Intervall, Theme, Export/Import, Certificate…, Account… | `?mock=1&user=1` → ⚙ | 1280×720 |
| 7 | Curves | Editor mit Drag-Punkt, Tabelle, „+ add point", crit-Linie, „now"-Marker, Duty→RPM-Referenz | `?mock=1&user=1&tab=curves` | 1280×720 |
| 8 | Curves – Fehler | Rote Notice nach abgelehntem Apply | `&tab=curves`, critical=900 → Apply | 1280×720 |
| 9 | Curves – 202 | Gelbe Notice „restart required" | Mock liefert 202 bei geändertem Kanalsatz | 1280×720 |
| 10 | Manual | Slider, Set/Back to auto, HDD-Mindestwert-Warnung, MANUAL-Badge | `?mock=1&user=1&tab=manual`, hdd-Slider < 60 | 1280×720 |
| 11 | Presets | Liste mit built-in/recommended-Badges, Details aufgeklappt, Save-Feld | `?mock=1&user=1&tab=presets` → Details | 1280×720 |
| 12 | Alerts | Transport-Form, Statusliste, Template-Karte, Kinds-Tabelle, Recent alerts | `?mock=1&user=1&tab=alerts` | 1280×720 |
| 13 | Alerts – Test-Toast | Erfolgs-Toast „Test alert sent via pve-notify" | `&tab=alerts` → Send test alert | Zoom unten rechts |
| 14 | Certificate – automatic | Felder, SAN-Chips, Fingerprint + copy, Download-Buttons, „How to trust" aufgeklappt | `?mock=1&user=1` → TLS-Button, details öffnen | 1280×720 |
| 15 | Certificate – Upload-Formular | Eigenes Zertifikat einspielen, HSTS-Force-Checkbox | → Upload own certificate…, nach 400 `force_required` | 1280×720 |
| 16 | Certificate – fallback / soon / off | Drei Fehler-/Sonderzustände mit Badge und Notice | `&tls=fallback`, `&tls=soon`, `&tls=off` | 1280×720 |
| 17 | Account | Change password/user, Sessions-Tabelle mit THIS SESSION | ⚙ → Account… → Change password… | 1280×720 |
| 18 | System | Host/Machine/CPU/Fan-Controller-Karten, Memory/DIMM-, GPU·NPU-, Network-, Storage-Tabellen | `?mock=1&user=1&tab=system` (2 Screens) | 1280×720 |
| 19 | System – Fehlerhinweis | Notice „Some sources could not be read" | `&tab=system&syserr=1` | 1280×720 |
| 20 | Log | Filter, auto-scroll, Refresh/Export/Clear, WARN/ERROR-Färbung | `?mock=1&user=1&tab=log` | 1280×720 |
| 21 | Compatibility | Profiltabelle mit ACTIVE und verified-Badges | `&tab=compat` | 1280×720 |
| 22 | About | Beschreibung, Lizenz/Repo/Credits | `&tab=about` | 1280×720 |
| 23 | Verbindungsbanner | „Connection to the daemon lost" | ohne Mock: Daemon stoppen, oder `failures=2` per DevTools | 1280×720 |
| 24 | Light-Theme Overview | Theme-Vergleich | `?mock=1&user=1`, Theme light | 1280×720 |
| 25 | Mobile Overview | Handy-Nutzung, Header zweizeilig, Karten gestapelt | `?mock=1` und `?mock=1&user=1` | 375×812 |
| 26 | Mobile Curves | Touch-Drag am Punkt | `&tab=curves` | 375×812 |
