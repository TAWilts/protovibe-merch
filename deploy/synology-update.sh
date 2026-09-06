#!/bin/sh
# Pulls release images and recreates backend/web only when the running image
# IDs differ. Intended for a root-owned DSM Task Scheduler entry.
set -eu

PROJECT_DIR="${PROJECT_DIR:-/volume1/docker/protovibe-merch-multitenant-test}"
ENV_FILE="${ENV_FILE:-$PROJECT_DIR/.env}"
COMPOSE_FILE="${COMPOSE_FILE:-$PROJECT_DIR/docker-compose.synology.yml}"

if [ ! -f "$ENV_FILE" ] || [ ! -f "$COMPOSE_FILE" ]; then
  echo "Missing $ENV_FILE or $COMPOSE_FILE" >&2
  exit 2
fi

if docker compose version >/dev/null 2>&1; then
  compose() {
    docker compose --env-file "$ENV_FILE" -f "$COMPOSE_FILE" "$@"
  }
elif command -v docker-compose >/dev/null 2>&1; then
  compose() {
    docker-compose --env-file "$ENV_FILE" -f "$COMPOSE_FILE" "$@"
  }
else
  echo "Docker Compose is not available" >&2
  exit 2
fi

setting() {
  key="$1"
  sed -n "s/^${key}=//p" "$ENV_FILE" | tail -n 1 | tr -d '\r'
}

repository="$(setting MERCH_IMAGE_REPOSITORY)"
tag="$(setting MERCH_IMAGE_TAG)"
data_root="$(setting SYNOLOGY_DATA_ROOT)"
[ -n "$repository" ] || repository="ghcr.io/tawilts/protovibe-merch-multitenant"
[ -n "$tag" ] || tag="latest"
[ -n "$data_root" ] || data_root="/volume1/docker/protovibe-merch-multitenant-test/data"

backend_image="${repository}:${tag}"
web_image="${repository}-web:${tag}"
backend_container="$(compose ps -q backend)"
web_container="$(compose ps -q web)"

if [ -z "$backend_container" ] || [ -z "$web_container" ]; then
  echo "No complete running stack found. Run the documented first installation instead." >&2
  exit 2
fi

compose pull backend web

running_backend_id="$(docker inspect --format '{{.Image}}' "$backend_container")"
running_web_id="$(docker inspect --format '{{.Image}}' "$web_container")"
target_backend_id="$(docker image inspect --format '{{.Id}}' "$backend_image")"
target_web_id="$(docker image inspect --format '{{.Id}}' "$web_image")"

if [ "$running_backend_id" = "$target_backend_id" ] && [ "$running_web_id" = "$target_web_id" ]; then
  echo "No image update available; containers stay untouched."
  exit 0
fi

# Schema migrations run when the new backend starts. Keep a host-visible dump
# from immediately before that point, independent of the app's daily backups.
pre_update_dir="${data_root}/pre-update"
mkdir -p "$pre_update_dir"
dump_base="${pre_update_dir}/merch-$(date +%Y%m%d-%H%M%S).sql"
if ! compose exec -T db sh -c 'exec mariadb-dump --single-transaction --routines --triggers --user=root --password="$MARIADB_ROOT_PASSWORD" "$MARIADB_DATABASE"' > "$dump_base"; then
  rm -f "$dump_base"
  echo "Pre-update database dump failed; containers stay untouched." >&2
  exit 1
fi
gzip "$dump_base"

compose up -d --no-deps backend web

attempt=0
while [ "$attempt" -lt 30 ]; do
  backend_container="$(compose ps -q backend)"
  web_container="$(compose ps -q web)"
  backend_health="$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "$backend_container")"
  web_health="$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "$web_container")"
  if [ "$backend_health" = "healthy" ] && [ "$web_health" = "healthy" ]; then
    echo "Update installed; backend and web are healthy."
    exit 0
  fi
  attempt=$((attempt + 1))
  sleep 2
done

echo "Updated containers did not become healthy in time." >&2
compose ps
exit 1
