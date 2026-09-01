# Merch Manager

Selbst gehostete Merch-Verwaltung für Bands: Verkauf am Stand, Wareneingang,
Bestand, Bilanzen, Bandkasse und Auswertungen — mandantenfähig, sodass mehrere
Bands unabhängig voneinander auf derselben Instanz arbeiten.

Sie ist kein öffentlicher Onlineshop und kein ERP. Ihr Zweck ist die schnelle,
nachvollziehbare Erfassung am Merch-Stand.

## Stand

Diese Version ist die Neuentwicklung der bisherigen Flask-Anwendung als
Go-API mit Vue-Frontend. Die Vorgängerversion liegt vollständig unter
[`_old/`](_old/) und bleibt die funktionale Referenz — dort steht auch die
ausführliche fachliche Dokumentation.

Der Umsetzungsfortschritt wird in [`PROGRESS.md`](PROGRESS.md) geführt.

## Architektur

| Bereich | Technik |
|---|---|
| Backend | Go, Gin, GORM |
| Datenbank | MariaDB 11 |
| Frontend | Vue 3, TypeScript, Vite, Pinia, vue-i18n |
| Offline | PWA mit Service Worker und IndexedDB-Warteschlange |
| Auslieferung | Docker Compose, Caddy als TLS-Terminierung und SPA-Server |

```
_old/       vorherige Flask-App, unverändert als Referenz
backend/    Go-API
frontend/   Vue-SPA
deploy/     Dockerfiles, Compose-Stacks, Caddy-Konfiguration
```

Geldbeträge sind immer ganzzahlige Cent-Werte, Bestände werden ausschließlich
aus Einkaufs- und Verkaufsbewegungen berechnet. Beides ist aus der
Vorgängerversion übernommen und verhindert Rundungsfehler und divergierende
Bestandsspalten.

## Mandantenfähigkeit

Alle operativen Tabellen tragen eine `band_id`. Der Band-Bezug wird nicht in
den einzelnen Abfragen gesetzt, sondern zentral im GORM-Layer erzwungen: eine
Abfrage ohne gesetzten Band-Kontext schlägt fehl, statt stillschweigend alle
Bands zu treffen.

Plattformkonten (`system_admin`, `support_admin`) gehören zu keiner Band und
haben **keinen** Zugriff auf Banddaten. Supportzugriff läuft ausschließlich über
den freigabepflichtigen Ablauf: anfordern → ein Band-Admin genehmigt →
Bestätigung mit 2FA → zeitlich begrenzter Zugriff, für beide Seiten sichtbar und
vollständig im Audit-Log. Einen Notfallzugang ohne Zustimmung der Band gibt es
bewusst nicht.

## Lokale Entwicklung

```bash
cp .env.example .env          # SECRET_KEY und DB_PASSWORD setzen

docker compose -f deploy/docker-compose.dev.yml up -d   # MariaDB auf Port 3307

cd backend
DATABASE_DSN='merch:merch@tcp(127.0.0.1:3307)/merch?charset=utf8mb4&parseTime=true&loc=UTC&multiStatements=true' \
SECRET_KEY="$(openssl rand -base64 48)" \
ENVIRONMENT=development COOKIE_SECURE=false \
go run ./cmd/server

cd ../frontend
npm install
npm run dev                   # http://localhost:5173, /api wird auf :8000 geproxyt
```

## Gesamter Stack

```bash
cp .env.example .env
docker compose -f deploy/docker-compose.yml up --build
```

## Synology

Für DSM Container Manager gibt es einen Image-basierten Stack, eine
Konfigurationsvorlage und einen Update-Task, der nur bei tatsächlich neuen
Images neu startet. Die vollständige Erstinstallation steht in
[`deploy/SYNOLOGY.md`](deploy/SYNOLOGY.md).

## Tests

```bash
cd backend  && go test ./...
cd frontend && npm run test:unit && npm run type-check
```

## Sicherung

Die App legt geplante Dumps an (voll und pro Band), inklusive der hochgeladenen
Rechnungen und Produktfotos. Aufbewahrung, manuelle Läufe, Download und das
Wiederherstellen einer einzelnen Band laufen über das Admin Center. Ergänzend
ist ein Snapshot des Docker-Volumes sinnvoll.
