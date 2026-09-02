# Testbetrieb auf Synology DSM

Dieser Stack ist für DSM mit Container Manager vorbereitet. GitHub Actions baut
Backend und Weboberfläche für `linux/amd64` und `linux/arm64`; auf dem NAS werden
nur MariaDB und die fertigen Images gestartet. Die Datenbank hat keinen
veröffentlichten Host-Port.

## 1. Voraussetzungen

- Synology DSM mit Container Manager und Docker Compose
- SSH-Zugriff für die Erstinstallation und den geplanten Update-Task
- ausgehender HTTPS-Zugriff zu `ghcr.io`
- freier LAN-Port `8090` oder eine Domain im DSM Reverse Proxy
- ein gesicherter Ordner `/volume1/docker/protovibe-merch-multitenant-test`

Projektname, Verzeichnis, Netzwerk und Persistenzpfade sind absichtlich von der
älteren Installation getrennt. Beide Stacks können dadurch gleichzeitig laufen;
die bestehende Instanz auf Port `8089` wird weder gestoppt noch verändert.

Das Repository ist öffentlich. Die GHCR-Pakete sind nach dem ersten Publish
trotzdem zunächst privat. Entweder beide Pakete in GitHub unter **Packages →
Package settings → Change visibility** öffentlich machen oder auf der Synology
als `root` ein GitHub-PAT (classic) mit ausschließlich `read:packages`
verwenden, damit auch der geplante Root-Task dieselbe Anmeldung nutzt:

```sh
sudo -i
docker login ghcr.io -u TAWilts
```

Das Token wird dabei als Passwort eingegeben und gehört nicht in `.env`.

## 2. Test-Images über GitHub Actions veröffentlichen

Nach dem Merge dieser Dateien in den Default-Branch:

1. GitHub → **Actions** → **Publish release images** öffnen.
2. **Run workflow** wählen und als `image_tag` zunächst `synology-test` setzen.
3. Auf zwei veröffentlichte Pakete warten:
   `ghcr.io/tawilts/protovibe-merch-multitenant:synology-test` und
   `ghcr.io/tawilts/protovibe-merch-multitenant-web:synology-test`.

Die getrennten Paketnamen sind absichtlich gewählt: Ein neues `latest` kann
dadurch niemals vom Update-Task der bestehenden Legacy-Installation gezogen
werden.

Für eine reguläre Veröffentlichung einen SemVer-Tag pushen, beispielsweise
`v2.0.0`. Dieser erzeugt zusätzlich die Tags `v2.0.0`, `2.0.0`, `2.0` und
`latest`. Vor dem Publish läuft dieselbe vollständige Testsuite wie bei Pull
Requests.

## 3. Verzeichnisse und Konfiguration anlegen

`docker-compose.synology.yml`, `.env.synology.example` und
`synology-update.sh` nach `/volume1/docker/protovibe-merch-multitenant-test`
kopieren. Dann per SSH:

```sh
sudo -i
cd /volume1/docker/protovibe-merch-multitenant-test
cp .env.synology.example .env
chmod 600 .env
mkdir -p data/app data/mariadb data/caddy-data data/caddy-config data/pre-update
chown -R 10001:10001 data/app
```

In `.env` müssen vor dem Start mindestens diese Werte angepasst werden:

- `MERCH_IMAGE_TAG=synology-test`
- `PUBLIC_BASE_URL=http://<NAS-IP>:8090`
- `PUBLIC_REGISTRATION_ENABLED=true`, wenn das öffentliche Anfrageformular
  auf der Landingpage verwendet werden soll
- `SECRET_KEY` mit dauerhaftem Zufallswert
- `DB_PASSWORD` und `DB_ROOT_PASSWORD` mit unabhängigen Zufallswerten
- `BOOTSTRAP_ADMIN_PASSWORD` mit einem einmaligen Initialpasswort

Sichere Werte ohne problematische Shell-Zeichen lassen sich erzeugen mit:

```sh
openssl rand -hex 48
openssl rand -hex 32
```

`SECRET_KEY` darf nach der Inbetriebnahme nie mehr geändert werden, weil damit
unter anderem die gespeicherten 2FA-Geheimnisse verschlüsselt werden.

## 4. Erster Start

```sh
cd /volume1/docker/protovibe-merch-multitenant-test
docker compose --env-file .env -f docker-compose.synology.yml pull
docker compose --env-file .env -f docker-compose.synology.yml up -d
docker compose --env-file .env -f docker-compose.synology.yml ps
curl -f http://127.0.0.1:8090/healthz
curl -f http://127.0.0.1:8090/readyz
```

Danach `http://<NAS-IP>:8090` öffnen, das Bootstrap-Konto einrichten und 2FA
abschließen. Anschließend `BOOTSTRAP_ADMIN_PASSWORD` in `.env` leeren und den
Backend-Container einmal aktualisieren:

```sh
docker compose --env-file .env -f docker-compose.synology.yml up -d backend
```

Persistiert werden MariaDB, Uploads, App-Backups und Caddy-Daten unter
`/volume1/docker/protovibe-merch-multitenant-test/data`. Dieser gesamte Ordner
gehört zusätzlich in
Hyper Backup beziehungsweise in die Snapshot-Replikation.

## 5. HTTPS über den DSM Reverse Proxy

Für einen Zugriff außerhalb des LAN:

1. In DSM eine Domain und ein Zertifikat einrichten.
2. Unter **Anmeldeportal → Erweitert → Reverse Proxy** eine HTTPS-Quelle auf
   `http://127.0.0.1:8090` weiterleiten.
3. In `.env` `PUBLIC_BASE_URL=https://merch.example.org`,
   `COOKIE_SECURE=true` und `HOST_BIND=127.0.0.1` setzen.
4. Den Stack mit `docker compose ... up -d` neu anwenden.

Die Anwendung niemals direkt per Portfreigabe aus dem Internet erreichbar
machen. Der DSM Reverse Proxy soll TLS terminieren; MariaDB bleibt ausschließlich
im Compose-Netz.

## 6. Updates nur bei neuen Images

In DSM unter **Systemsteuerung → Aufgabenplaner** einen benutzerdefinierten Task
als `root` anlegen, zum Beispiel täglich. Befehl:

```sh
/bin/sh /volume1/docker/protovibe-merch-multitenant-test/synology-update.sh >> /volume1/docker/protovibe-merch-multitenant-test/update.log 2>&1
```

Der Task zieht nur Backend und Web. Stimmen deren Image-IDs bereits mit den
laufenden Containern überein, beendet er sich ohne Neustart. Vor einem echten
Update schreibt er einen MariaDB-Dump nach `data/pre-update`, ersetzt nur die
geänderten App-Container und wartet auf beide Healthchecks. Das MariaDB-Major-
Image wird bewusst nicht automatisch aktualisiert.

Für einen festen Rollback `MERCH_IMAGE_TAG` auf einen veröffentlichten
Versionstag setzen und `pull` plus `up -d` ausführen. Vor einem Downgrade immer
den Datenbankstand sichern, da neuere Releases bereits Schema-Migrationen
ausgeführt haben können.
