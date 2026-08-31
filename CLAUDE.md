# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Was das Repo ist

Merch-Verwaltung für Bands (Verkauf am Stand, Wareneingang, Bestand, Bilanzen,
Bandkasse), mandantenfähig auf einer Instanz. Das Repo befindet sich mitten in
der Neuentwicklung der alten Flask/SQLite-App als Go-API + Vue-SPA.

- `_old/` — die vollständige Flask-Vorgängerversion. **Unverändert lassen.** Sie
  ist die funktionale und fachliche Referenz; neuer Code verweist per
  `_old/app.py:1458` auf die Stelle, die er portiert.
- `backend/` — Go (Gin, GORM, MariaDB), `frontend/` — Vue 3 + Vite,
  `deploy/` — Dockerfiles, Compose-Stacks, Caddy.
- `PROGRESS.md` — der laufende Umsetzungsplan (deutsch, Schritte 0–6 mit
  Notizen). Bei Arbeit an der Portierung dort den Stand nachziehen.

Projekt- und UI-Sprache ist Deutsch; Code, Kommentare, API-Pfade und
Fehler-Codes sind Englisch.

## Befehle

```bash
# MariaDB für die Entwicklung (Port 3307, Zugang merch/merch)
docker compose -f deploy/docker-compose.dev.yml up -d

# Backend
cd backend
DATABASE_DSN='merch:merch@tcp(127.0.0.1:3307)/merch?charset=utf8mb4&parseTime=true&loc=UTC&multiStatements=true' \
SECRET_KEY="$(openssl rand -base64 48)" ENVIRONMENT=development COOKIE_SECURE=false \
go run ./cmd/server                      # :8000

go test ./...                            # DB-Tests skippen ohne TEST_DATABASE_DSN
TEST_DATABASE_DSN='...' go test ./... -race
TEST_DATABASE_DSN='...' go test ./internal/api -run TestSalesFlow -v   # einzelner Test
gofmt -l . && go vet ./...               # CI bricht bei nicht formatiertem Code ab

# Entwicklungskonto anlegen (build-tagged, nie im Release-Image)
go run -tags seed ./cmd/seeduser <username> <role> [band-id]

# Frontend
cd frontend && npm install
npm run dev          # :5173, /api → :8000 (VITE_API_TARGET überschreibt das Ziel)
npm run test:unit    # vitest
npm run type-check   # vue-tsc; läuft in CI vor test:unit und build

# Gesamter Stack
cp .env.example .env && docker compose -f deploy/docker-compose.yml up --build
```

Die Integrationstests in `internal/api` und `internal/db` laufen gegen eine
echte MariaDB und **skippen still**, wenn `TEST_DATABASE_DSN` fehlt — ein grüner
`go test ./...` ohne diese Variable sagt also wenig aus.

## Architektur

### Mandantenfähigkeit ist ein GORM-Callback, kein Query-Detail

Der Band-Filter wird nie von Hand in eine Query geschrieben.
`internal/db/tenant_callback.go` hängt sich vor Query/Row/Update/Delete/Create
und wertet den Scope aus `context.Context` (`internal/tenant`) aus:

- Betroffen ist jedes Modell, das `models.Tenant` einbettet — erkannt per
  Reflection, nicht per Registrierung.
- Fehlender Scope ⇒ `ErrMissingScope`, also ein lauter Fehler statt einer
  Abfrage über alle Bands.
- Create stempelt `band_id`; ein Datensatz mit fremder `band_id` wird abgelehnt.
- `Scope.ReadOnly` (Support-Zugriff nur lesend) lässt jeden Schreibvorgang
  scheitern.

Handler holen die Band also über `tenant.MustBandID(ctx)`, nicht aus dem Request.
`tenant.WithCrossBandAccess` hebt den Filter auf und ist **nur** für echte
Plattformarbeit gedacht (Bandliste, Audit-Viewer, Backup-Scheduler, Migration) —
nie als Abkürzung um einen fehlenden Scope.

Kontroll-Ebene (`bands`, `users`, `sessions`, `pending_auth`,
`support_access_grants`, `platform_settings`, `backup_runs`, `audit_log`) ist
bewusst nicht band-skopiert und wird stattdessen im Handler autorisiert.

Neue Tabelle mit `band_id NOT NULL` ⇒ Modell muss `models.Tenant` einbetten und
in `models.AllModels()` stehen; sonst schlägt
`internal/db/schema_consistency_test.go` fehl. Eine bewusst ungeschützte Tabelle
gehört nach `models.ControlPlaneTables`.

### Schema

Die Wahrheit über das Schema sind die versionierten SQL-Migrationen unter
`backend/migrations` (golang-migrate, per `embed.go` eingebettet, beim Start in
`db.Migrate` angewandt). Die GORM-Structs mappen nur darauf — es gibt kein
AutoMigrate im Produktivpfad. Schemaänderung heißt: neues `NNNN_*.up.sql` +
`.down.sql` **und** Struct anpassen.

### Zwei Invarianten aus der Vorgängerversion

- **Geld ist immer ganzzahlige Cent.** Kein Float im Geldpfad. Aufteilungen
  laufen über `internal/services/money.Distribute` (deterministische
  Restcent-Verteilung, Port von `distribute_cents`).
- **Bestand wird berechnet**, aus Einkaufs- und Verkaufsbewegungen, und nie als
  Spalte gespeichert.

`models.Date` ist ein Kalendertag ohne Zeit (MariaDB `DATE`), damit ein am Stand
erfasstes Verkaufsdatum unabhängig von der Serverzeitzone bleibt. Timestamps
sind UTC (`NowFunc` in `db.Open`).

### Request-Kette

`internal/api/router.go` registriert die Guards auf der Engine (nicht auf der
Route-Group), damit auch unbekannte Pfade sie durchlaufen:
`noStore → resolveSession → maintenanceGuard → csrfGuard → platformBoundary →
posModeGuard`. `/healthz`, `/readyz`, `/metrics` liegen bewusst davor.

- Auth: HttpOnly-Session-Cookie `merch_session` + Double-Submit-CSRF über
  Cookie `merch_csrf` und Header `X-CSRF-Token`.
- `internal/rbac` hält die Capability-Matrix (Port von `user_capabilities()`).
  Sie geht ans Frontend nur zur Navigations-Darstellung — **jede Route prüft die
  Rechte serverseitig noch einmal selbst**. Dasselbe gilt für die Router-Guards
  im Frontend.
- Plattformkonten (`system_admin`, `support_admin`) haben keinen Zugriff auf
  Banddaten. Nur die Präfixe in `rbac.PlatformStaffAllowedPrefixes` sind ohne
  Grant erreichbar; alles andere braucht den Freigabeablauf
  (anfordern → Band-Admin genehmigt → 2FA → befristet, vollständig im Audit-Log).
  Einen Notfallzugang ohne Zustimmung der Band gibt es bewusst nicht.
- POS-Modus sperrt `rbac.POSRestrictedPrefixes` serverseitig.
- Fehlerformat ist einheitlich `{code, message, details?}` über die Helfer in
  `internal/api/context.go` (`fail`, `forbidden`, `serverError`); `code` ist
  stabil und wird im Frontend übersetzt.
- Audit-Einträge (`internal/audit`) tragen Nutzer, Band **und**
  `acting_grant_id` — daran hängt die Frage „wer vom Support hat wann unter
  welcher Freigabe geschaut“.

### Frontend

- `src/api/client.ts` ist der einzige Fetch-Wrapper (`credentials: 'include'`,
  CSRF-Header, `ApiError` mit `detailCode`); Endpunkte in `src/api/endpoints.ts`.
- Ein Router, zwei Shells: Band-App unter `/` (`AppShell`), Admin Center unter
  `/admin` (`PlatformShell`).
- **Jeder sichtbare String gehört nach `src/i18n/de.json` / `en.json`**, nie in
  eine Komponente. Deutsch ist Produktsprache und Fallback.
- Offline: `src/offline/outbox.ts` (IndexedDB via `idb`) und `sync.ts`. Ein
  Verkauf bekommt beim Erfassen eine dauerhafte `client_event_id`; der Server
  speichert sie mit einem Payload-Fingerprint und spielt bei einem Retry die
  ursprüngliche Antwort zurück — Übertragen ist dadurch idempotent. Offline ist
  bewusst nur die Verkaufsansicht; Admin- und Profilseiten werden nie gecacht.
