#!/bin/sh
# Stable DSM-side bootstrap. It pulls the configured backend image, extracts
# that image's matching synology-update.sh atomically, then executes it.
set -eu

PROJECT_DIR="${PROJECT_DIR:-/volume1/docker/protovibe-merch-multitenant-test}"
ENV_FILE="${ENV_FILE:-$PROJECT_DIR/.env}"
UPDATE_FILE="${UPDATE_FILE:-$PROJECT_DIR/synology-update.sh}"
IMAGE_UPDATE_PATH="/usr/local/share/merch-manager/synology-update.sh"

fail() {
  echo "FEHLER: $*" >&2
  exit 1
}

setting() {
  sed -n "s/^${1}=//p" "$ENV_FILE" |
    tr -d '\r' |
    tail -n 1
}

[ -d "$PROJECT_DIR" ] ||
  fail "Projektordner $PROJECT_DIR fehlt."

[ -f "$ENV_FILE" ] ||
  fail "Konfiguration $ENV_FILE fehlt."

repository="$(setting MERCH_IMAGE_REPOSITORY)"
tag="$(setting MERCH_IMAGE_TAG)"

[ -n "$repository" ] ||
  repository="ghcr.io/tawilts/protovibe-merch-multitenant"

[ -n "$tag" ] ||
  tag="latest"

image="${repository}:${tag}"
tmp_update="${UPDATE_FILE}.new"

rm -f "$tmp_update"

echo "Beziehe Backend-Image fuer Updater: $image"

docker pull "$image"

container_id="$(docker create "$image")"

cleanup() {
  docker rm -f "$container_id" >/dev/null 2>&1 || true
  rm -f "$tmp_update"
}
trap cleanup 0 HUP INT TERM

echo "Extrahiere passenden Synology-Updater aus dem Image..."

docker cp   "${container_id}:${IMAGE_UPDATE_PATH}"   "$tmp_update"

[ -s "$tmp_update" ] ||
  fail "Updater im Image ist leer oder konnte nicht gelesen werden."

chmod 700 "$tmp_update"
mv "$tmp_update" "$UPDATE_FILE"

docker rm -f "$container_id" >/dev/null
trap - 0 HUP INT TERM

echo "Updater installiert: $UPDATE_FILE"

export PROJECT_DIR ENV_FILE

exec /bin/sh "$UPDATE_FILE"
