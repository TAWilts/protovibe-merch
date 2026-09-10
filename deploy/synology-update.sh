#!/bin/sh
set -eu

SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
PROJECT_DIR="${PROJECT_DIR:-$SCRIPT_DIR}"
PROJECT_NAME="${PROJECT_NAME:-$(basename "$PROJECT_DIR")}"
ENV_FILE="${ENV_FILE:-$PROJECT_DIR/.env}"
COMPOSE_FILE="${COMPOSE_FILE:-$PROJECT_DIR/docker-compose.synology.yml}"
IMAGE_COMPOSE_PATH="/usr/local/share/merch-manager/docker-compose.synology.yml"

EXPECTED_REPOSITORY="${EXPECTED_REPOSITORY:-ghcr.io/tawilts/protovibe-merch-multitenant}"
EXPECTED_DATA_ROOT="${EXPECTED_DATA_ROOT:-$PROJECT_DIR/data}"
# Optional safety check. Leave empty when the deployment is intentionally
# using a different port, for example a parallel test instance.
EXPECTED_PORT="${EXPECTED_PORT:-}"

fail() {
  echo "FEHLER: $*" >&2
  exit 1
}

setting() {
  sed -n "s/^${1}=//p" "$ENV_FILE" |
    tr -d '\r' |
    tail -n 1
}

show_status() {
  echo
  echo "=== Container-Status ==="
  compose ps -a || true

  echo
  echo "=== Letzte Logs ==="
  compose logs --no-color --tail=100 db backend web || true
}

container_health() {
  docker inspect \
    --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' \
    "$1"
}

container_setting() {
  docker inspect \
    --format '{{range .Config.Env}}{{println .}}{{end}}' \
    "$1" |
    sed -n "s/^${2}=//p" |
    tail -n 1
}

bool_value() {
  value="$(printf '%s' "$1" | tr '[:upper:]' '[:lower:]')"
  case "$value" in
    1|true|yes|on)
      printf 'true'
      ;;
    *)
      printf 'false'
      ;;
  esac
}

app_ready() {
  status="$(
    curl \
      --silent \
      --output /dev/null \
      --write-out '%{http_code}' \
      --max-time 5 \
      "$APP_URL/readyz" ||
      true
  )"

  [ "$status" = 200 ]
}

# ------------------------------------------------------------
# Grundkonfiguration pruefen
# ------------------------------------------------------------

echo "========================================"
echo "Merch Manager Deployment"
echo "$(date)"
echo "========================================"

[ -d "$PROJECT_DIR" ] ||
  fail "Projektordner $PROJECT_DIR fehlt."

[ -f "$ENV_FILE" ] ||
  fail "Konfiguration $ENV_FILE fehlt."

[ -f "$COMPOSE_FILE" ] ||
  fail "Compose-Datei $COMPOSE_FILE fehlt."

cd "$PROJECT_DIR"

# ------------------------------------------------------------
# Docker Compose bestimmen
# ------------------------------------------------------------

if docker compose version >/dev/null 2>&1; then
  compose() {
    docker compose \
      -p "$PROJECT_NAME" \
      --env-file "$ENV_FILE" \
      -f "$COMPOSE_FILE" \
      "$@"
  }
  validate_compose() {
    docker compose \
      -p "$PROJECT_NAME" \
      --env-file "$ENV_FILE" \
      -f "$1" \
      config --quiet
  }
elif command -v docker-compose >/dev/null 2>&1; then
  compose() {
    docker-compose \
      -p "$PROJECT_NAME" \
      --env-file "$ENV_FILE" \
      -f "$COMPOSE_FILE" \
      "$@"
  }
  validate_compose() {
    docker-compose \
      -p "$PROJECT_NAME" \
      --env-file "$ENV_FILE" \
      -f "$1" \
      config --quiet
  }
else
  fail "Docker Compose ist nicht verfuegbar."
fi

# ------------------------------------------------------------
# Deployment-Konfiguration pruefen
# ------------------------------------------------------------

REPOSITORY="$(setting MERCH_IMAGE_REPOSITORY)"
CONFIGURED_IMAGE_TAG="$(setting MERCH_IMAGE_TAG)"
ENVIRONMENT="$(setting ENVIRONMENT)"
DATA_ROOT="$(setting SYNOLOGY_DATA_ROOT)"
HOST_PORT="$(setting HOST_PORT)"

case "$(printf '%s' "$ENVIRONMENT" | tr '[:upper:]' '[:lower:]')" in
  development)
    IMAGE_TAG="$(setting MERCH_DEVELOPMENT_IMAGE_TAG)"
    [ -n "$IMAGE_TAG" ] || IMAGE_TAG="development"
    ;;
  *)
    IMAGE_TAG="$CONFIGURED_IMAGE_TAG"
    ;;
esac

[ "$REPOSITORY" = "$EXPECTED_REPOSITORY" ] ||
  fail "Falsches Image-Repository: $REPOSITORY"

[ -n "$IMAGE_TAG" ] ||
  fail "MERCH_IMAGE_TAG fehlt in .env."

# Shell variables take precedence over --env-file interpolation. This makes
# both backend and web use the selected development tag without rewriting the
# server's persistent .env file.
MERCH_IMAGE_TAG="$IMAGE_TAG"
export MERCH_IMAGE_TAG

[ "$DATA_ROOT" = "$EXPECTED_DATA_ROOT" ] ||
  fail "SYNOLOGY_DATA_ROOT muss $EXPECTED_DATA_ROOT sein."

[ -n "$HOST_PORT" ] ||
  fail "HOST_PORT fehlt in .env."

if [ -n "$EXPECTED_PORT" ] && [ "$HOST_PORT" != "$EXPECTED_PORT" ]; then
  fail "HOST_PORT muss $EXPECTED_PORT sein."
fi

APP_URL="http://127.0.0.1:${HOST_PORT}"

echo
echo "Deployment-Konfiguration:"
echo "  Repository: $REPOSITORY"
echo "  Umgebung:   ${ENVIRONMENT:-production}"
echo "  Image-Tag:  $IMAGE_TAG"
echo "  Daten:      $DATA_ROOT"
echo "  Port:       $HOST_PORT"

echo
echo "Pruefe Compose-Konfiguration..."
compose config --quiet

# ------------------------------------------------------------
# Persistente Verzeichnisse und Synology-Rechte sicherstellen
# ------------------------------------------------------------

echo
echo "Pruefe Datenverzeichnisse..."

mkdir -p \
  "$DATA_ROOT/mariadb" \
  "$DATA_ROOT/app" \
  "$DATA_ROOT/caddy-data" \
  "$DATA_ROOT/caddy-config"

# MariaDB laeuft im Container mit UID/GID 999.
chown -R 999:999 "$DATA_ROOT/mariadb"
chmod -R u+rwX "$DATA_ROOT/mariadb"

# Das Merch-Manager-Backend laeuft mit UID/GID 10001.
chown -R 10001:10001 "$DATA_ROOT/app"
chmod -R u+rwX "$DATA_ROOT/app"

# Caddy laeuft im aktuellen Web-Image als root.
chown -R 0:0 "$DATA_ROOT/caddy-data"
chown -R 0:0 "$DATA_ROOT/caddy-config"
chmod -R u+rwX "$DATA_ROOT/caddy-data"
chmod -R u+rwX "$DATA_ROOT/caddy-config"

# ------------------------------------------------------------
# Bestehenden Stack pruefen
# ------------------------------------------------------------

DB_CONTAINER="$(compose ps -q db 2>/dev/null || true)"
BACKEND_CONTAINER="$(compose ps -q backend 2>/dev/null || true)"
WEB_CONTAINER="$(compose ps -q web 2>/dev/null || true)"

if [ -z "$DB_CONTAINER" ] ||
   [ -z "$BACKEND_CONTAINER" ] ||
   [ -z "$WEB_CONTAINER" ]; then
  fail "Der Stack laeuft nicht vollstaendig. Erstinstallation oder Diagnose erforderlich."
fi

# ------------------------------------------------------------
# Aktuellen Zustand pruefen
# ------------------------------------------------------------

DB_HEALTH="$(container_health "$DB_CONTAINER")"
BACKEND_HEALTH="$(container_health "$BACKEND_CONTAINER")"
WEB_HEALTH="$(container_health "$WEB_CONTAINER")"

echo
echo "Aktueller Zustand:"
echo "  MariaDB: $DB_HEALTH"
echo "  Backend: $BACKEND_HEALTH"
echo "  Web:     $WEB_HEALTH"

[ "$DB_HEALTH" = healthy ] ||
  fail "MariaDB ist nicht healthy. Deployment wird nicht ausgefuehrt."

[ "$BACKEND_HEALTH" = healthy ] ||
  fail "Backend ist nicht healthy. Deployment wird nicht ausgefuehrt."

[ "$WEB_HEALTH" = healthy ] ||
  fail "Web-Container ist nicht healthy. Deployment wird nicht ausgefuehrt."

app_ready ||
  fail "Die Anwendung antwortet vor dem Update nicht auf /readyz."

# ------------------------------------------------------------
# Aktuelle Anwendungs-Images laden
# MariaDB wird absichtlich nicht automatisch aktualisiert.
# ------------------------------------------------------------

echo
echo "Beziehe aktuelle Merch-Manager-Images von GitHub..."

if ! compose pull backend web; then
  fail "Image-Download fehlgeschlagen. Laufender Stack bleibt unveraendert."
fi

# The DSM-side compose file used to stay unchanged forever while only this
# updater was refreshed from the image. New environment mappings (notably
# PURCHASE_EDITING_ENABLED) could therefore be present in .env but absent from
# the backend container. Install the compose file shipped with the exact same
# backend image before recreating the stack.
echo
echo "Aktualisiere Compose-Datei passend zum Backend-Image..."

TEMP_COMPOSE="${COMPOSE_FILE}.new"
rm -f "$TEMP_COMPOSE"

if ! CONFIG_CONTAINER="$(docker create "${REPOSITORY}:${IMAGE_TAG}")"; then
  fail "Compose-Datei konnte nicht aus dem Backend-Image vorbereitet werden."
fi

if ! docker cp "${CONFIG_CONTAINER}:${IMAGE_COMPOSE_PATH}" "$TEMP_COMPOSE"; then
  docker rm -f "$CONFIG_CONTAINER" >/dev/null 2>&1 || true
  rm -f "$TEMP_COMPOSE"
  fail "Compose-Datei konnte nicht aus dem Backend-Image gelesen werden."
fi
docker rm -f "$CONFIG_CONTAINER" >/dev/null 2>&1 || true

[ -s "$TEMP_COMPOSE" ] || {
  rm -f "$TEMP_COMPOSE"
  fail "Compose-Datei im Backend-Image ist leer."
}

grep -q 'PURCHASE_EDITING_ENABLED:' "$TEMP_COMPOSE" || {
  rm -f "$TEMP_COMPOSE"
  fail "Compose-Datei reicht PURCHASE_EDITING_ENABLED nicht an das Backend weiter."
}

if ! validate_compose "$TEMP_COMPOSE"; then
  rm -f "$TEMP_COMPOSE"
  fail "Neue Compose-Datei ist mit der vorhandenen .env nicht gueltig."
fi

chmod 644 "$TEMP_COMPOSE"
mv "$TEMP_COMPOSE" "$COMPOSE_FILE"
echo "Compose-Datei aktualisiert: $COMPOSE_FILE"
echo "Purchase-Editing: ${PURCHASE_EDITING_ENABLED:-$(setting PURCHASE_EDITING_ENABLED)}"

# ------------------------------------------------------------
# Datenbank VOR dem Neu-Erzeugen der Container sichern
# ------------------------------------------------------------

PRE_UPDATE_DIR="$DATA_ROOT/pre-update"
mkdir -p "$PRE_UPDATE_DIR"
chown 0:0 "$PRE_UPDATE_DIR"
chmod 700 "$PRE_UPDATE_DIR"

TIMESTAMP="$(date +%Y%m%d-%H%M%S)-$$"
DUMP_FILE="$PRE_UPDATE_DIR/merch-${TIMESTAMP}.sql"
TEMP_DUMP="${DUMP_FILE}.tmp"

echo
echo "Erstelle Datenbanksicherung..."

if ! compose exec -T db sh -c \
  'exec mariadb-dump \
    --single-transaction \
    --routines \
    --triggers \
    --user=root \
    --password="$MARIADB_ROOT_PASSWORD" \
    "$MARIADB_DATABASE"' \
  > "$TEMP_DUMP"; then

  rm -f "$TEMP_DUMP"
  fail "Datenbanksicherung fehlgeschlagen. Container bleiben unveraendert."
fi

if [ ! -s "$TEMP_DUMP" ]; then
  rm -f "$TEMP_DUMP"
  fail "Datenbanksicherung ist leer."
fi

mv "$TEMP_DUMP" "$DUMP_FILE"
gzip "$DUMP_FILE"
chmod 600 "${DUMP_FILE}.gz"

echo "Sicherung erstellt:"
echo "  ${DUMP_FILE}.gz"

# ------------------------------------------------------------
# Gesamten Stack neu erzeugen
# --force-recreate uebernimmt auch .env-/Compose-Aenderungen.
# Die Bind-Mount-Daten bleiben erhalten.
# ------------------------------------------------------------

echo
echo "Erzeuge Stack mit aktueller Konfiguration neu..."

if ! compose up \
  -d \
  --force-recreate \
  --remove-orphans; then

  echo "Neu-Erzeugung des Stacks fehlgeschlagen." >&2
  show_status
  exit 1
fi

# ------------------------------------------------------------
# Auf DB, Backend, Web und /readyz warten
# ------------------------------------------------------------

echo
echo "Warte auf den neu gestarteten Stack..."

attempt=0

while [ "$attempt" -lt 90 ]; do
  DB_CONTAINER="$(compose ps -q db 2>/dev/null || true)"
  BACKEND_CONTAINER="$(compose ps -q backend 2>/dev/null || true)"
  WEB_CONTAINER="$(compose ps -q web 2>/dev/null || true)"

  if [ -n "$DB_CONTAINER" ] &&
     [ -n "$BACKEND_CONTAINER" ] &&
     [ -n "$WEB_CONTAINER" ]; then

    DB_HEALTH="$(container_health "$DB_CONTAINER")"
    BACKEND_HEALTH="$(container_health "$BACKEND_CONTAINER")"
    WEB_HEALTH="$(container_health "$WEB_CONTAINER")"

    if [ "$DB_HEALTH" = healthy ] &&
       [ "$BACKEND_HEALTH" = healthy ] &&
       [ "$WEB_HEALTH" = healthy ] &&
       app_ready; then

      EXPECTED_PURCHASE_EDITING="$(bool_value "$(setting PURCHASE_EDITING_ENABLED)")"
      ACTUAL_PURCHASE_EDITING="$(bool_value "$(container_setting "$BACKEND_CONTAINER" PURCHASE_EDITING_ENABLED)")"

      if [ "$ACTUAL_PURCHASE_EDITING" != "$EXPECTED_PURCHASE_EDITING" ]; then
        show_status
        fail "PURCHASE_EDITING_ENABLED wurde nicht korrekt an das Backend uebergeben."
      fi

      echo
      echo "========================================"
      echo "Deployment erfolgreich"
      echo "========================================"
      echo
      echo "Image-Tag: $IMAGE_TAG"
      echo "URL:       $APP_URL"
      echo "Backup:    ${DUMP_FILE}.gz"
      echo "Purchase-Editing im Backend: $ACTUAL_PURCHASE_EDITING"
      echo

      compose ps
      exit 0
    fi
  fi

  attempt=$((attempt + 1))
  sleep 2
done

# ------------------------------------------------------------
# Timeout / Fehlerdiagnose
# ------------------------------------------------------------

echo
echo "FEHLER: Die Anwendung wurde nach dem Neustart nicht rechtzeitig bereit." >&2
show_status
exit 1
