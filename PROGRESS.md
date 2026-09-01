# Fortschritt

**Stand:** 2026-08-30 — **aktueller Schritt:** Schritt 6. Backend und Oberfläche sind
funktional deckungsgleich; offen sind der Legacy-Import, die Testportierung und die
Abnahme (visueller Abgleich, funktionaler Durchlauf, Mandantentest, README).

Umbau der Flask/SQLite-App (`_old/`) zu Go (Gin + GORM, MariaDB) + Vue 3, mandantenfähig.
Referenzplan: `/home/benni/.claude/plans/das-ist-ein-repo-tender-lecun.md`

Legende: `[ ]` offen · `[~]` in Arbeit · `[x]` fertig

---

## 0 — Fundament ✅

- [x] Alte App vollständig nach `_old/` verschoben (inkl. `.github/workflows`)
- [x] `PROGRESS.md` angelegt
- [x] `.gitignore` / `.env.example` für den neuen Stack
- [x] Go-Modul + Verzeichnisstruktur (`backend/`)
- [x] Config-Loading aus Env
- [x] Vue-3-Scaffold (`frontend/`, Vite + TS + Pinia + Router + i18n)
- [x] Design-Tokens aus `_old/static/app.css` nach `frontend/src/assets/`
- [x] `deploy/docker-compose.yml` (MariaDB + Backend + Caddy) + Dockerfiles
- [x] CI-Workflows (Go-Test, Frontend-Build, GHCR-Image bei `v*.*.*`)
- [x] Neue `README.md`

**Notizen:** Backend bootet gegen MariaDB, `/healthz` `/readyz` `/api/v1/version` antworten.
Frontend baut inkl. PWA-Precache. Design-Tokens liegen unverändert als
`frontend/src/assets/theme.css` (Zeilen 1–99 der alten `app.css`), die
Komponenten-Styles als `base.css`. Lokale DB:
`docker compose -f deploy/docker-compose.dev.yml up -d` → Port 3307, Zugang `merch/merch`.
`cmd/importlegacy` ist bislang nur ein Stub (Phase 6).

---

## 1 — Schema, Tenancy, Auth ✅

- [x] GORM-Modelle für alle Tabellen (Basis: `_old/app.py:83-480`)
- [x] Plattform-Tabellen: `bands`, `support_access_grants`, `platform_settings`, `sessions`, `backup_runs`
- [x] `band_id` auf allen operativen Tabellen, band-skopierte Unique-Constraints
- [x] Versionierte Migrationen (golang-migrate)
- [x] `internal/tenant`: Scope über `context.Context`
- [x] GORM-Callback erzwingt Band-Scope (Fehler statt stillem Durchlassen)
- [x] Tenant-Isolationstest über alle band-skopierten Modelle
- [x] Strukturtest: jede Tabelle mit `band_id NOT NULL` muss `models.Tenant` einbetten
- [x] `internal/rbac`: Rollen-Level + Capability-Matrix (Port von `user_capabilities()`, `_old/app.py:1458`)
- [x] argon2id-Passwörter, serverseitiger Session-Store, `session_version`-Abgleich
- [x] CSRF (Double-Submit, Header `X-CSRF-Token`)
- [x] Setup-Codes (einmalig, Ablauf nach `ACCOUNT_SETUP_CODE_DAYS`)
- [x] TOTP-Enrollment (zweiphasig) + 10 Recovery-Codes, 2FA-Pflicht für Plattformrollen
- [x] Reauth-Fenster (`PROFILE_REAUTH_SECONDS`) + `verify_admin_sensitive_action`-Äquivalent
- [x] Audit-Log (mit `band_id` und `acting_grant_id`)
- [x] Middleware-Kette: Wartungsmodus → Session/CSRF → Plattform-Grenze → POS-Modus
- [x] Bootstrap des ersten System-Admins aus `BOOTSTRAP_ADMIN_PASSWORD` (einmalig)
- [x] Integrationstests für Login, Setup-Code, 2FA, Recovery-Code, CSRF, Reauth, POS-Modus, Plattform-Grenze

**Notizen:** 26 Tabellen migriert, 18 davon band-skopiert und im Isolationstest abgedeckt.
Der Tenant-Guard sitzt als GORM-Callback in `internal/db/tenant_callback.go`; eine Query ohne
Band-Scope schlägt fehl statt still über alle Bands zu laufen. Zwei Designfehler dabei gefunden
und behoben: vier Ein-Zeilen-pro-Band-Tabellen hatten `band_id` als PK und umgingen dadurch den
Guard (jetzt Surrogatschlüssel + `models.Tenant`), und die Guard-Kette hing an der Route-Gruppe,
sodass unbekannte Pfade sie umgingen (jetzt auf Engine-Ebene via `underPrefix`).

Cross-Band-Zugriff geht nur über `tenant.WithCrossBandAccess` — bewusst und sichtbar an jeder
Aufrufstelle. Kontrollebenen-Tabellen (`users`, `sessions`, `audit_log`, …) sind bewusst
ungeguarded und in `models.ControlPlaneTables` dokumentiert; ein Strukturtest erzwingt, dass
jede andere Tabelle mit `band_id NOT NULL` `models.Tenant` einbettet.

Tests laufen mit:
`TEST_DATABASE_DSN='merch:merch@tcp(127.0.0.1:3307)/merch_test?charset=utf8mb4&parseTime=true&loc=UTC&multiStatements=true' go test ./...`

---

## 2 — Katalog, Verkauf, Einkäufe ✅

- [x] Artikel, Optionsgruppen, Optionswerte, Varianten (CRUD, Positionen, `is_offered` vs `is_active`)
- [x] `combination_key` (sortiert, ordnungsunabhängig) + Kartesisches Produkt → `internal/services/catalogue`
- [x] `sync_variants` / `preserve_variants_for_new_option_groups` / Deaktivieren statt Löschen (DB-Teil)
- [x] Default-Optionen für neue Artikel (Farbe: Schwarz/Weiß · Größe: S–XXL)
- [x] Beleg-IDs (`V-`/`E-`), Sequenz geteilt mit QR-Reservierungen → `internal/services/receipt`
- [x] `distribute_cents` (Largest-Remainder) + Spendenverteilung → `internal/services/money`
- [x] Geldbetrags-Parsing (`18,00` / `18.00` / `1.234,56`) und CSV-Formatierung
- [x] Verkaufs-Regeln + Buchung inkl. idempotentem Offline-Sync → `internal/services/sales`
- [x] HTTP-Endpunkte für Artikel, Varianten, Verkauf, `receipt-preview`
- [x] Abgeleiteter Bestand (Σ Einkäufe − Σ nicht stornierter Verkäufe), Mindestbestandswarnung
- [x] Einkäufe: Multi-Positions-Belege, Bearbeiten, Löschen
- [x] Rechnungs-Upload/-Download inkl. Typprüfung, Veranstaltungen (bandweit geteilt)
- [x] Datei-Store (Rechnungen) mit S3-fähigem Interface
- [x] Vue-Grundgerüst: Shell, Header mit rollengefilterter Navigation, Login mit allen Auth-Schritten
- [x] Vue: Verkaufsseite (3-Spalten-POS-Grid), Warenkorb, Spendenberechnung
- [x] Vue: Einkaufsseite, Artikelverwaltung

**Notizen:**

Das Backend von Schritt 2 ist vollständig und durch einen echten End-to-End-Rauchtest
belegt (Einrichtungscode → Passwort → Artikel → Wareneingang → Verkauf mit Überzahlung →
Offline-Sync doppelt → POS-Modus). Services:

| Paket | Inhalt |
|---|---|
| `services/money` | `Distribute` (Largest-Remainder), Betrags-Parsing, CSV-Format |
| `services/catalogue` | Kombinationsschlüssel, `SyncVariants`, `PreserveVariantsForNewOptionGroups`, `ApplyConfiguration`, abgeleiteter Bestand |
| `services/receipt` | `V-`/`E-`-IDs, Tages-Sequenz, geteilt mit `payment_qr_intents` |
| `services/sales` | `Prepare` (Regeln, rein) + `Book` (Transaktion, QR-Einlösung, Offline-Sync) |
| `services/purchases` | Multi-Positions-Belege, Korrigieren, Löschen, letzter Einkaufspreis |
| `storage` | Datei-Store mit Band-Präfix, Pfad-Traversal-Schutz, S3-fähiges Interface |

Bewusste Entscheidungen, alle im Test festgeschrieben:

- **Bestand blockiert nie einen Verkauf.** `Prepare` bekommt gar keinen Bestand übergeben.
- **Nichts wird gelöscht.** Zurückgezogene Varianten kommen mit Preis, Bestand und Fotos
  zurück; Umbenennen einer Option wirkt rückwirkend auf alte Belege.
- **Halb konfigurierter Artikel verkauft nichts** statt die leere Dimension wegzulassen.
- **Einkäufe sind korrigierbar, Verkäufe nur stornierbar** — ein Beleg, den ein Kunde in
  der Hand hatte, wird nie umgeschrieben.
- **Offline-Sync ist idempotent**; der Fingerabdruck ignoriert die Beleg-ID-Vorschau.
- **Uploads:** 10 MB, nur PDF/JPEG/PNG/WebP, Endung muss zum Content-Type passen, Dateiname
  landet nie im Dateisystem, Auslieferung immer als Attachment mit `nosniff`.

Zwei API-Fehler durch Tests gefunden und behoben: eine unbekannte Variante lieferte 500
statt 400, und unbekannte Pfade unter `/api/v1` umgingen die Guard-Kette (jetzt auf
Engine-Ebene, plus JSON-`NoRoute`).

Frontend: Shell, Header, Login (alle vier Auth-Schritte inkl. 2FA-Enrollment und
Recovery-Codes) und die Verkaufsseite stehen. Der Stil des Originals ist übernommen —
Screenshots des Aurora-Themes zeigen dasselbe 3-Spalten-Grid, dieselben Panels und
Buttons. Die Navigation scrollt horizontal wie im Original.

**Lokal starten:**
```bash
docker compose -f deploy/docker-compose.dev.yml up -d
cd backend  && DATABASE_DSN='merch:merch@tcp(127.0.0.1:3307)/merch?...' SECRET_KEY=... \
               ENVIRONMENT=development COOKIE_SECURE=false go run ./cmd/server
cd frontend && npm run dev     # http://localhost:5173
```

---

## 3 — Historie, Vorgänge, Bilanzen, Bandfinanzen, Export/Import, QR ✅

- [x] Stornierung (`item` / `receipt`), nie hart löschen
- [x] Historie: Belege mit aufklappbaren Positionen, Labels aus dem Live-Katalog
- [x] Liefer- und Zahlungsstatus-Workflows, vier Arbeitslisten in `/vorgaenge`
- [x] Veranstaltungen + globaler `sale_event_state`
- [x] `balance_payload`: Kennzahlen, Ranglisten, Einnahmenverlauf, Weighted-Average-Cost-Basis
- [x] Bandfinanzen (eigenes Ledger, Kategorien, Anhänge, Storno)
- [x] CSV-Export (UTF-8 BOM, Semikolon, exakte Header aus `_old/app.py:6478`) + ZIP
- [x] CSV-Import mit Preflight (Einkäufe/Verkäufe), atomarer Commit
- [x] Zahlungs-QR: PayPal-Link + EPC/GiroCode, `payment_qr_intents` mit TTL
- [x] Vue: Historie, Vorgänge, Bilanzen (Inline-SVG-Chart), Bandfinanzen

**Notizen:**

Backend steht bis auf CSV-Import und Zahlungs-QR. Neue Services:
`services/balances` (Kennzahlen, Ranglisten, Einnahmenverlauf, gewichtete Durchschnittskosten),
`services/bandfinance` (eigenes Ledger mit Kategorien und Storno),
`services/export` (CSV mit UTF-8-BOM und Semikolon, ZIP-Bündel).
Stornierung und die Status-Workflows liegen in `services/sales/lifecycle.go`,
Historie und die vier Arbeitslisten in `services/sales/history.go`.

Bewusste Entscheidungen:

- **Der Lieferstatus lässt sich in beide Richtungen setzen.** Ursprünglich war er hier nur
  vorwärts erlaubt; das war eine Erfindung der Portierung. Das Original nannte den Handler
  ausdrücklich „Advance **or correct**" (`_old/app.py:11078`), und zu Recht: ein Vertipper
  hätte einen Vorgang sonst für immer als erledigt festgeschrieben. Verschlossen bleibt nur
  der Weg aus dem Versandvorgang heraus — eine Sendung wird nie zum Thekenverkauf.
- **Eine nachträglich bezahlte Rechnung behält `payment_follow_up`**, landet also in einer
  eigenen Historie statt wie ein normaler Barverkauf auszusehen.
- **Storniertes bleibt sichtbar**, verschwindet aber aus Bestand, Bilanzen und Arbeitslisten.
- **Die Variantenbezeichnungen kommen aus dem Live-Katalog**, nicht aus einem Snapshot —
  deshalb wirkt das Umbenennen einer Option rückwirkend auf alte Belege.
- Die Ranglisten falten Varianten in ihren Artikel; der Gewinn nutzt den gewichteten
  Durchschnittspreis aus dem Einkaufsbuch (in SQL auf ganze Cent gerundet).

**Ein ernster Fehler durch die Tests gefunden:** GORM lässt Felder mit `default:`-Tag beim
INSERT weg, wenn ihr Wert der Null-Wert ist — dadurch wurde ein **unbezahlter Verkauf als
bezahlt gespeichert**. Alle 26 Boolean-Defaults sind aus den Modell-Tags entfernt (die
Spalten-Defaults bleiben in der Migration), und ein Strukturtest in
`internal/db/schema_consistency_test.go` verhindert den Rückfall.

Zwei kleinere: MariaDB kennt `CAST(x AS JSON)` nicht (Varianten-Labels werden jetzt in Go
zusammengesetzt), und ein Gin-Pfadparameter kann keine Datei-Endung tragen.

Frontend für Schritt 3 steht: Historie mit aufklappbaren Belegen und
Drei-Sekunden-Storno-Bestätigung, die vier Arbeitslisten, Bilanzen mit
Inline-SVG-Einnahmenchart und umschaltbaren Ranglisten, Bandkasse. Alle Seiten sind im
Browser gegen ein echtes Backend geprüft (Playwright-Screenshots, keine Konsolenfehler).

Drei UI-Fehler dabei gefunden und behoben: die Varianten in der Artikelverwaltung zeigten
den internen Kombinationsschlüssel (`1|3`) statt lesbarer Namen, die Einkaufshistorie
zeigte gar keinen Artikelnamen, und mein API-Client übergab die Belegart als Datum. Die
Label-Erzeugung liegt jetzt einmal in `catalogue.VariantLabels` statt dreimal kopiert.

Zahlungs-QR und CSV-Import sind fertig:

- `services/paymentqr` baut den EPC/GiroCode selbst (elf Zeilen, 331-Byte-Grenze) und
  prüft die IBAN per ISO-7064-Prüfsumme — ein Zahlendreher fällt bei der Einrichtung auf,
  nicht erst wenn Geld bei einem Fremden landet. Passt die Nutzlast nicht, wird die
  Artikelliste im Verwendungszweck gekürzt, **die Beleg-ID davor nie**: sie ist die
  einzige Verbindung zwischen Zahlungseingang und Verkauf. Der Betrag kommt aus dem
  Katalog, nicht aus dem Request. Einen Code anzuzeigen bucht nichts.
- `services/importer` parst die fünfspaltige CSV (UTF-8 oder Windows-1252, Semikolon),
  prüft die ganze Datei vorab und legt erst dann in einer Transaktion an. Eine Datei, der
  eine bereits existierende Optionsspalte fehlt, wird abgelehnt — sonst würden alle Zeilen
  stillschweigend auf die falsche Variante gebucht. Der Import landet unter **einem**
  Beleg, damit ein Fehlgriff als Einheit rückgängig gemacht werden kann.

---

## 4 — Fotos, Diashow, PWA/Offline ✅

- [x] Foto-Upload: max 1600 px, JPEG q84, ≤10 MB, ≤30 MP, EXIF-Transpose
- [x] Variantenfotos + Diashow-Extrafotos, `include_in_slideshow` / `show_price`
- [x] Diashow: Wechselrate, Animationsgeschwindigkeit, Collage, 16 Keyframes portiert
- [x] Service Worker (vite-plugin-pwa), Manifest, `start_url: /sales`
- [x] Registrierung des Workers in `main.ts` — er wurde gebaut, aber nie installiert,
      die PWA war damit bis 30.08. faktisch inaktiv
- [x] IndexedDB-Outbox für Offline-Verkäufe
- [x] Idempotenter Sync via `sync_events` (`payload_hash`, 409 bei Konflikt)

**Notizen:**

Backend steht bis auf CSV-Import und Zahlungs-QR. Neue Services:
`services/balances` (Kennzahlen, Ranglisten, Einnahmenverlauf, gewichtete Durchschnittskosten),
`services/bandfinance` (eigenes Ledger mit Kategorien und Storno),
`services/export` (CSV mit UTF-8-BOM und Semikolon, ZIP-Bündel).
Stornierung und die Status-Workflows liegen in `services/sales/lifecycle.go`,
Historie und die vier Arbeitslisten in `services/sales/history.go`.

Bewusste Entscheidungen:

- **Der Lieferstatus lässt sich in beide Richtungen setzen.** Ursprünglich war er hier nur
  vorwärts erlaubt; das war eine Erfindung der Portierung. Das Original nannte den Handler
  ausdrücklich „Advance **or correct**" (`_old/app.py:11078`), und zu Recht: ein Vertipper
  hätte einen Vorgang sonst für immer als erledigt festgeschrieben. Verschlossen bleibt nur
  der Weg aus dem Versandvorgang heraus — eine Sendung wird nie zum Thekenverkauf.
- **Eine nachträglich bezahlte Rechnung behält `payment_follow_up`**, landet also in einer
  eigenen Historie statt wie ein normaler Barverkauf auszusehen.
- **Storniertes bleibt sichtbar**, verschwindet aber aus Bestand, Bilanzen und Arbeitslisten.
- **Die Variantenbezeichnungen kommen aus dem Live-Katalog**, nicht aus einem Snapshot —
  deshalb wirkt das Umbenennen einer Option rückwirkend auf alte Belege.
- Die Ranglisten falten Varianten in ihren Artikel; der Gewinn nutzt den gewichteten
  Durchschnittspreis aus dem Einkaufsbuch (in SQL auf ganze Cent gerundet).

**Ein ernster Fehler durch die Tests gefunden:** GORM lässt Felder mit `default:`-Tag beim
INSERT weg, wenn ihr Wert der Null-Wert ist — dadurch wurde ein **unbezahlter Verkauf als
bezahlt gespeichert**. Alle 26 Boolean-Defaults sind aus den Modell-Tags entfernt (die
Spalten-Defaults bleiben in der Migration), und ein Strukturtest in
`internal/db/schema_consistency_test.go` verhindert den Rückfall.

Zwei kleinere: MariaDB kennt `CAST(x AS JSON)` nicht (Varianten-Labels werden jetzt in Go
zusammengesetzt), und ein Gin-Pfadparameter kann keine Datei-Endung tragen.

Frontend für Schritt 3 steht: Historie mit aufklappbaren Belegen und
Drei-Sekunden-Storno-Bestätigung, die vier Arbeitslisten, Bilanzen mit
Inline-SVG-Einnahmenchart und umschaltbaren Ranglisten, Bandkasse. Alle Seiten sind im
Browser gegen ein echtes Backend geprüft (Playwright-Screenshots, keine Konsolenfehler).

Drei UI-Fehler dabei gefunden und behoben: die Varianten in der Artikelverwaltung zeigten
den internen Kombinationsschlüssel (`1|3`) statt lesbarer Namen, die Einkaufshistorie
zeigte gar keinen Artikelnamen, und mein API-Client übergab die Belegart als Datum. Die
Label-Erzeugung liegt jetzt einmal in `catalogue.VariantLabels` statt dreimal kopiert.

Zahlungs-QR und CSV-Import sind fertig:

- `services/paymentqr` baut den EPC/GiroCode selbst (elf Zeilen, 331-Byte-Grenze) und
  prüft die IBAN per ISO-7064-Prüfsumme — ein Zahlendreher fällt bei der Einrichtung auf,
  nicht erst wenn Geld bei einem Fremden landet. Passt die Nutzlast nicht, wird die
  Artikelliste im Verwendungszweck gekürzt, **die Beleg-ID davor nie**: sie ist die
  einzige Verbindung zwischen Zahlungseingang und Verkauf. Der Betrag kommt aus dem
  Katalog, nicht aus dem Request. Einen Code anzuzeigen bucht nichts.
- `services/importer` parst die fünfspaltige CSV (UTF-8 oder Windows-1252, Semikolon),
  prüft die ganze Datei vorab und legt erst dann in einer Transaktion an. Eine Datei, der
  eine bereits existierende Optionsspalte fehlt, wird abgelehnt — sonst würden alle Zeilen
  stillschweigend auf die falsche Variante gebucht. Der Import landet unter **einem**
  Beleg, damit ein Fehlgriff als Einheit rückgängig gemacht werden kann.

---

## 5 — Admin Center, Supportzugriff, Ops ✅

- [x] Plattform-Shell (`/admin`) mit eigener Navigation (Vue)
- [x] Vue: Bands, Supportzugriff, Postfach, Audit-Log, Sicherungen, Einstellungen
- [x] Vue: Genehmigungsseite der Band mit Passwort-Bestätigung
- [x] Benutzerverwaltung der Band (anlegen, Rolle, aktiv, 2FA-Reset, löschen) + Vue
- [x] Profilseite: Personalisierung, Passwort, Benutzername, 2FA, Wiederherstellungscodes
- [x] Band-Lifecycle: anlegen, umbenennen, deaktivieren, Soft-Delete mit Karenzzeit
- [x] Quotas (Speicher, Nutzer) + Feature-Flags pro Band
- [x] Supportzugriff: anfordern → Band-Admin genehmigt → 2FA → zeitlich begrenzt → Audit
- [x] Banner für aktiven Supportzugriff auf **beiden** Seiten (API-Payload; Vue-Shell zeigt es)
- [x] `read_only`-Grants lehnen nicht-GET serverseitig ab
- [x] Support-Postfach band-übergreifend
- [x] Backup-Scheduler (Voll + pro Band) inkl. Datei-Stores, Aufbewahrung
- [x] Restore pro Band in einer Transaktion, vorher automatischer Sicherungspunkt
- [x] Wartungsmodus (global/band) + Ankündigungsbanner
- [x] Audit-Viewer band-übergreifend, filterbar
- [x] Session-Kill pro Nutzer / pro Band
- [x] `/healthz`, `/readyz`, `/metrics`, strukturierte JSON-Logs
- [x] SMTP-Einstellungen (Passwort verschlüsselt, nie zurückgegeben)
- [x] Testmail-Versand (`services/mailer`) und GitHub-Update-Check (`services/updates`)
- [x] Erstes Band-Admin-Konto durch den System-Admin (`POST /platform/bands/:id/admins`)

**Notizen:**

Backend steht bis auf CSV-Import und Zahlungs-QR. Neue Services:
`services/balances` (Kennzahlen, Ranglisten, Einnahmenverlauf, gewichtete Durchschnittskosten),
`services/bandfinance` (eigenes Ledger mit Kategorien und Storno),
`services/export` (CSV mit UTF-8-BOM und Semikolon, ZIP-Bündel).
Stornierung und die Status-Workflows liegen in `services/sales/lifecycle.go`,
Historie und die vier Arbeitslisten in `services/sales/history.go`.

Bewusste Entscheidungen:

- **Der Lieferstatus lässt sich in beide Richtungen setzen.** Ursprünglich war er hier nur
  vorwärts erlaubt; das war eine Erfindung der Portierung. Das Original nannte den Handler
  ausdrücklich „Advance **or correct**" (`_old/app.py:11078`), und zu Recht: ein Vertipper
  hätte einen Vorgang sonst für immer als erledigt festgeschrieben. Verschlossen bleibt nur
  der Weg aus dem Versandvorgang heraus — eine Sendung wird nie zum Thekenverkauf.
- **Eine nachträglich bezahlte Rechnung behält `payment_follow_up`**, landet also in einer
  eigenen Historie statt wie ein normaler Barverkauf auszusehen.
- **Storniertes bleibt sichtbar**, verschwindet aber aus Bestand, Bilanzen und Arbeitslisten.
- **Die Variantenbezeichnungen kommen aus dem Live-Katalog**, nicht aus einem Snapshot —
  deshalb wirkt das Umbenennen einer Option rückwirkend auf alte Belege.
- Die Ranglisten falten Varianten in ihren Artikel; der Gewinn nutzt den gewichteten
  Durchschnittspreis aus dem Einkaufsbuch (in SQL auf ganze Cent gerundet).

**Ein ernster Fehler durch die Tests gefunden:** GORM lässt Felder mit `default:`-Tag beim
INSERT weg, wenn ihr Wert der Null-Wert ist — dadurch wurde ein **unbezahlter Verkauf als
bezahlt gespeichert**. Alle 26 Boolean-Defaults sind aus den Modell-Tags entfernt (die
Spalten-Defaults bleiben in der Migration), und ein Strukturtest in
`internal/db/schema_consistency_test.go` verhindert den Rückfall.

Zwei kleinere: MariaDB kennt `CAST(x AS JSON)` nicht (Varianten-Labels werden jetzt in Go
zusammengesetzt), und ein Gin-Pfadparameter kann keine Datei-Endung tragen.

Frontend für Schritt 3 steht: Historie mit aufklappbaren Belegen und
Drei-Sekunden-Storno-Bestätigung, die vier Arbeitslisten, Bilanzen mit
Inline-SVG-Einnahmenchart und umschaltbaren Ranglisten, Bandkasse. Alle Seiten sind im
Browser gegen ein echtes Backend geprüft (Playwright-Screenshots, keine Konsolenfehler).

Drei UI-Fehler dabei gefunden und behoben: die Varianten in der Artikelverwaltung zeigten
den internen Kombinationsschlüssel (`1|3`) statt lesbarer Namen, die Einkaufshistorie
zeigte gar keinen Artikelnamen, und mein API-Client übergab die Belegart als Datum. Die
Label-Erzeugung liegt jetzt einmal in `catalogue.VariantLabels` statt dreimal kopiert.

Zahlungs-QR und CSV-Import sind fertig:

- `services/paymentqr` baut den EPC/GiroCode selbst (elf Zeilen, 331-Byte-Grenze) und
  prüft die IBAN per ISO-7064-Prüfsumme — ein Zahlendreher fällt bei der Einrichtung auf,
  nicht erst wenn Geld bei einem Fremden landet. Passt die Nutzlast nicht, wird die
  Artikelliste im Verwendungszweck gekürzt, **die Beleg-ID davor nie**: sie ist die
  einzige Verbindung zwischen Zahlungseingang und Verkauf. Der Betrag kommt aus dem
  Katalog, nicht aus dem Request. Einen Code anzuzeigen bucht nichts.
- `services/importer` parst die fünfspaltige CSV (UTF-8 oder Windows-1252, Semikolon),
  prüft die ganze Datei vorab und legt erst dann in einer Transaktion an. Eine Datei, der
  eine bereits existierende Optionsspalte fehlt, wird abgelehnt — sonst würden alle Zeilen
  stillschweigend auf die falsche Variante gebucht. Der Import landet unter **einem**
  Beleg, damit ein Fehlgriff als Einheit rückgängig gemacht werden kann.

---

## Nachtrag 30.08. — die Oberfläche eingeholt

Das Backend war weiter als die Oberfläche, und zwar systematisch: das CSS der alten App
wurde vollständig übernommen, das dazugehörige Markup an mehreren Stellen nie geschrieben.
Ungenutzte Klassen in `base.css` waren der zuverlässigste Hinweis darauf.

- [x] **Zahlungs-QR** am Stand und die Zahlungsziele in der Bandverwaltung. Ein Code
      anzuzeigen bucht weiterhin nichts; gebucht wird erst mit „Zahlung erhalten".
- [x] **2FA-QR** bei der Einrichtung. Der Code wird serverseitig gerendert, damit die
      Einrichtung auch ohne Netz funktioniert; das Secret bleibt als Tipp-Alternative.
- [x] **CSV-Import** in der Artikelverwaltung, mit der Vorschau, die das Backend
      ohnehin lieferte — ein Import legt Artikel und Optionen an, das muss man vorher sehen.
- [x] **Rechnungen und Beleganhänge** hochladen, ansehen, löschen. Sechs fertige
      Endpunkte hatten keinerlei Oberfläche; die Sicherung kopierte einen Datei-Store,
      den niemand füllen konnte.
- [x] **Variantenfotos** in der Artikelverwaltung verwalten und beim Verkauf anzeigen.
      `show_variant_photos` war eine Einstellung ohne Wirkung.
- [x] **Veranstaltungen** direkt auf der Verkaufsseite anlegen. `createEvent` existierte
      im API-Client, aufgerufen hat es niemand — die Auswahl blieb dauerhaft leer.
- [x] **Aufräumen alter Sicherungen** als Knopf statt nur als Endpunkt.

Behobene Fehler, die dabei auffielen:

- `BackupRun.Trigger` fehlte der `column:trigger_kind`-Tag; `TRIGGER` ist in MariaDB
  reserviert. Jede Sicherung brach mit „Unknown column 'trigger'" ab. Ein neuer Test
  (`TestEveryModelFieldHasItsColumn`) prüft jetzt **jede** Modellspalte gegen das Schema.
- Der **Standardverkaufspreis** wirkte nur im Moment der Variantenerzeugung. Wer ihn
  danach setzte, bekam Varianten mit 0,00 € — das Feld war faktisch nutzlos. Die Regel aus
  `_old/app.py:11826` ist nachgezogen: unveränderte Varianten ziehen mit, von Hand
  gesetzte Preise bleiben.
- Das **Supportzugriff-Banner** sah nur die Plattformseite. Die Band, um deren Daten es
  geht, sah nichts — genau umgekehrt zur Absicht.
- Der Weg in die Band mit aktivem Grant hing an `can_access_band_workflows`, das ein
  Plattformkonto nie hat. Der Link war unsichtbar, wenn er gebraucht wurde.
- Die Verkaufsdetails waren auch am Desktop zugeklappt; das Original öffnete sie ab
  761 px (`_old/static/sales.js:98`).
- Die Mengen-Schaltflächen waren mit 34 px zu klein fürs Tablet und das Zahlenfeld zeigte
  in Firefox seine Pfeilchen — die Regel blendete nur die `-webkit-`-Pseudoelemente aus.
- `deploy/Dockerfile.backend` baute mit Go 1.24, `go.mod` verlangt 1.25; dasselbe in
  `.github/workflows/test.yml`. Beides schlug fehl. `/data` gehörte im Image root,
  der Prozess läuft als `merch` — der Start scheiterte an `mkdir /data/storage`.
- `docker-compose.yml` nutzte `${SITE_ADDRESS::80}`, Caddy-Syntax in einer
  Compose-Datei; `compose up` brach sofort ab.

---

## 6 — Legacy-Import, Tests, Doku

- [ ] `cmd/importlegacy`: entschlüsseltes `merch.sqlite3` + `users.sqlite3` → eine Band
- [ ] Dateianhänge und Rollen-Mapping mit importieren
- [ ] Portierung der Regressionstests aus `_old/tests/test_app.py` (5.452 Zeilen)
- [ ] Visueller Abgleich alt/neu per Screenshot je Seite
- [ ] Funktionaler Durchlauf (Verkauf → Storno → Bilanz → Offline-Sync → CSV-Vergleich)
- [ ] Mandantentest (zweite Band, Isolation, Supportzugriff-Lebenszyklus)
- [ ] `README.md` fertig

**Stand 30.08.:** `cmd/importlegacy` ist weiterhin ein Stub — er prüft seine Flags und
bricht mit „legacy import is implemented in phase 6" ab. Für die eigentliche Abbildung
fehlt eine entschlüsselte Beispieldatenbank; blind gegen ein nur gelesenes Schema
geschrieben wäre sie mit hoher Wahrscheinlichkeit falsch, ohne dass es jemandem auffällt.

**Notizen:** Backend vollständig. **Schritt 5 wurde vor Schritt 4 gezogen**, weil das
Admin Center und der Supportzugriff der eigentliche Grund für den Umbau sind; Fotos und
Diashow sind nachrangig.

Der Supportzugriff ist genau der Ablauf, den das Original in
`_old/templates/system_admin.html:109` als deaktivierten Stub beschrieben hat:

1. Plattform-Admin fordert mit **Pflichtbegründung** und Scope an — noch kein Zugriff.
2. Ein Band-Admin genehmigt oder lehnt ab; das Genehmigen verlangt eine **frische
   Passwortbestätigung**, wie das Löschen eines Kontos.
3. Der Plattform-Admin aktiviert mit einem **frischen 2FA-Code** — eine morgens erteilte
   Genehmigung ist von einem gestohlenen Laptop abends wertlos.
4. Erst dann öffnet sich der Band-Scope, zeitlich begrenzt, mit Banner auf **beiden**
   Seiten und `acting_grant_id` an jedem Audit-Eintrag.
5. Beide Seiten können jederzeit widerrufen; die Session verliert den Scope sofort.

Ein Schlupfloch dabei gefunden und geschlossen: ein Plattform-Admin mit aktivem Grant
erfüllt `requireBandRole(band_admin)` und hätte damit seine **eigene nächste Anfrage
genehmigen** können. Der Service fing das ab; jetzt tut es zusätzlich ein expliziter
`requireBandAccount()`-Guard, und ein Test schreibt es fest.

Ein echter Lockout-Fehler ebenfalls behoben: der Wartungsmodus sperrte auch `/auth` —
ein Operator hätte sich nicht mehr anmelden können, um ihn abzuschalten.

Weiteres Backend: Band-Lifecycle mit Soft-Delete (nichts wird gelöscht, nur unsichtbar
und abgemeldet), Quotas und Feature-Flags, band-übergreifender Audit-Viewer mit
Band/User/Aktion/Grant-Filter, Session-Kill, Support-Postfach, Prometheus-`/metrics`,
und ein Cron-Scheduler (`internal/scheduler`) für abgelaufene Grants, Session-Purge und
Backups (voll + pro Band, inklusive Datei-Store, mit Aufbewahrungsfrist).

Backups nutzen `mariadb-dump`; die Zugangsdaten gehen über `MYSQL_PWD` statt über die
Kommandozeile, damit sie nicht in der Prozessliste stehen. Ein Pro-Band-Dump enthält
bewusst **keine** Kontrollebenen-Tabellen: das Wiederherstellen einer Band darf niemals
die Konten oder Einstellungen einer anderen überschreiben.

Die Vue-Oberfläche steht ebenfalls: eigene Plattform-Shell unter `/admin` mit Bands,
Supportzugriff, Postfach, Audit-Log, Sicherungen und Einstellungen; auf Bandseite die
Genehmigungsseite mit Passwort-Bestätigung. Der komplette Ablauf ist im Browser
durchgespielt (Playwright): Einrichtungscode → Passwort → 2FA-Enrollment →
Anfrage → Genehmigung → Aktivierung mit frischem Code → Banner auf beiden Seiten →
Widerruf. Keine Konsolenfehler.

**Ein ernster Fehler dabei gefunden:** der CSRF-Token lag nur im JS-Speicher. Nach jedem
Seiten-Reload blieb das Session-Cookie bestehen, der Token aber nicht — **jedes Speichern
schlug fehl**, mit der irreführenden Meldung „Sitzung abgelaufen". Der Token liegt jetzt
zusätzlich in einem lesbaren Cookie (Double-Submit; ein fremder Origin kann es weder
lesen noch den Header setzen). Ein Test hält das fest.

Zweiter Fehler: mit aktivem Grant leitete der Vue-Router den Admin trotzdem von den
Band-Seiten weg, obwohl der Server sie längst erlaubte.

Nachgezogen: die **Benutzerverwaltung der Band** und die **Profilseite**. Jede
Kontoänderung verlangt eine frische Passwortbestätigung (zehn Minuten gültig), und drei
Sperren verhindern, dass sich eine Band aussperrt: kein Selbst-Herabstufen, kein
Selbst-Deaktivieren, kein Löschen des letzten aktiven Band-Admins. Ein gelöschtes Konto
nimmt seine Buchungen nicht mit — dafür gibt es den Benutzernamen-Schnappschuss.

**Nachgezogen am 30.08.:** Restore, Mailversand, Update-Check und das Bootstrap-Konto.

Der Restore war die Lücke mit den meisten Konsequenzen: eine Sicherung ohne
Wiederherstellung ist eine Behauptung. Er nimmt **vorher** selbst einen Sicherungspunkt,
läuft in **einer** Transaktion und rührt ausschließlich die Tabellen der Band an — die
Kontrollebene und die anderen Bands sind schon dadurch außer Reichweite, dass ein
Band-Dump nichts anderes enthält. Der Dateibestand wird erst nach dem Commit getauscht,
und das alte Verzeichnis bleibt liegen, bis das geklappt hat.

Beim Bootstrap-Konto war eine echte Sackgasse: eine frische Band hatte keinen Zugang.
Konten anlegen darf nur ein Band-Admin, und der Supportzugriff braucht einen Band-Admin,
der ihn genehmigt. Der neue Endpunkt ist bewusst eng — nur System-Admin, Rolle fest auf
`band_admin`, Eintrag im Audit-Log **der Band**.

**Nachgezogen am 01.09.:** Bestandslisten, Supportnachrichten, Einkaufshistorie,
Artikelfotos und Diashow wurden an den Funktionsumfang des Originals angeglichen und
erweitert. Die beiden Bestandsbereiche sind getrennt, alphanumerisch sortierbar und
optional nach Artikel gruppiert; der CSV-Export übernimmt Filter, Reihenfolge und
Gruppierung. Der Bestellungs-Link ist ausgeblendet, seine Route bleibt für eine spätere
API-Nutzung erhalten.

Bandkonten können über das Kopfzeilen-Symbol Fragen oder Probleme an das gemeinsame
Supportpostfach senden. Support- und System-Admins sehen dieselben Nachrichten und
können sie einem aktiven Plattform-Admin zuweisen. Einkäufe erscheinen als aufklappbare
Belege und nehmen PDF-, JPEG- oder PNG-Rechnungen auf Belegebene an. Bei Artikeln lassen
sich Mindestbestände über alle Varianten setzen und mehrere Fotos gemeinsam hochladen;
die Bilder werden serverseitig geprüft, verkleinert und komprimiert. Die Diashow startet
im Vollbild, nutzt gegenläufig einfahrende Bild- und Preiskarten, vermeidet direkte
Wiederholungen und zeigt zwischen den Durchläufen eine bildzahlabhängige Collage.

**Weitergeführt am 01.09.:** Collagen sind jetzt echte, nicht überlappende Kachelansichten.
Pro Band lassen sich das Intervall in Produktfotos und die erlaubten Darstellungen
festlegen: gemeinsam scrollendes Mosaik, vollständig sichtbares Großmosaik mit Einflug
von allen Seiten und zwei gegenläufige Bildreihen. Jede Kachel verwendet `object-fit:
contain`; kein Produktfoto wird beschnitten oder verzerrt.

Wartungsmodus und Ankündigungen besitzen nun auch den zuvor fehlenden Band-Client:
Ankündigungen erscheinen als abgestuftes Banner, der Wartungsmodus als eigene Seite mit
Abmeldemöglichkeit und beide werden regelmäßig aktualisiert. Das erneute Speichern einer
Ankündigung entfernt außerdem ein altes Ablaufdatum, das neue Texte unsichtbar machen
konnte. Der Smartphone-Verkauf läuft in drei Schritten (Warenkorb, Zahlungsangaben,
Bestätigung beziehungsweise QR-Code) und ist separat als Commit `1778a63` abgelegt.

**Mobilkorrektur am 01.09.:** Im ersten Verkaufsschritt bleibt der Warenkorb auf
Ansichten bis 1000 Pixel als kompakte Summenleiste standardmäßig geschlossen und kann
bei Bedarf aufgeklappt werden. Unter 700 Pixel scrollen Artikelliste, Variantenwahl und
die Schaltfläche zum Hinzufügen gemeinsam als vollständige Seite, sodass die feste
Leiste keine Bedienelemente mehr verdeckt. Die Diashow übernimmt wieder die explizite
Viewport-Messung der Vorgängerversion: Für Einzelbilder und Collage-Kacheln wird der
kleinere Breiten- beziehungsweise Höhenfaktor verwendet und bei Resize, Drehung und
Fullscreen neu berechnet. Dadurch bleiben Seitenverhältnis und vollständiger Bildinhalt
erhalten.

Der eingeklappte mobile Warenkorb behält nun zusätzlich den Weiter-Button sichtbar.
Optionsgruppen stehen kompakt untereinander; bis zu fünf Werte laufen je Gruppe von
links nach rechts in nur 40 Pixel hohen Schaltflächen. Die Verkaufsansicht verwendet
außerdem wieder den Variantenfoto-Fallback des Originals: Fehlt das exakte Foto, wird
die fotografierte Variante desselben Artikels mit den meisten übereinstimmenden
Optionswerten gewählt und als ähnliche Variante beschriftet.
Im dritten Schritt steht die Verkaufs-ID zusätzlich als kontrastreiche, groß gesetzte
Kennung direkt im Abschlusskopf; bei QR-Zahlungen wird die reservierte ID angezeigt.

---
