#!/bin/sh
set -eu

SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
TEST_ROOT="$(mktemp -d)"
trap 'rm -rf "$TEST_ROOT"' EXIT HUP INT TERM

mkdir -p "$TEST_ROOT/bin" "$TEST_ROOT/project" "$TEST_ROOT/data"
touch "$TEST_ROOT/project/docker-compose.synology.yml"

cat > "$TEST_ROOT/project/.env" <<EOF
MERCH_IMAGE_REPOSITORY=ghcr.io/tawilts/protovibe-merch-multitenant
MERCH_IMAGE_TAG=latest
SYNOLOGY_DATA_ROOT=$TEST_ROOT/data
HOST_PORT=8090
EOF

cat > "$TEST_ROOT/bin/docker" <<'EOF'
#!/bin/sh
set -eu

if [ "$1" = "compose" ] && [ "$2" = "version" ]; then
  exit 0
fi

if [ "$1" = "compose" ]; then
  shift

  while [ "$#" -gt 0 ]; do
    case "$1" in
      -p|--env-file|-f)
        shift 2
        ;;
      *)
        break
        ;;
    esac
  done

  case "$*" in
    "config --quiet")
      exit 0
      ;;
    "ps -q db")
      echo db-container
      ;;
    "ps -q backend")
      echo backend-container
      ;;
    "ps -q web")
      echo web-container
      ;;
    "pull backend web")
      echo pull >> "$FAKE_LOG"
      ;;
    "exec -T db "*)
      echo 'CREATE TABLE test (id INT);'
      ;;
    "up -d --force-recreate --remove-orphans")
      echo up >> "$FAKE_LOG"
      ;;
    "ps")
      echo healthy
      ;;
    "ps -a")
      echo healthy
      ;;
    "logs --no-color --tail=100 db backend web")
      exit 0
      ;;
    *)
      echo "unexpected compose call: $*" >&2
      exit 1
      ;;
  esac

  exit 0
fi

if [ "$1" = "inspect" ]; then
  case "$*" in
    *db-container)
      echo "${FAKE_DB_HEALTH:-healthy}"
      ;;
    *backend-container|*web-container)
      echo healthy
      ;;
    *)
      echo "unexpected inspect call: $*" >&2
      exit 1
      ;;
  esac

  exit 0
fi

echo "unexpected docker call: $*" >&2
exit 1
EOF
chmod +x "$TEST_ROOT/bin/docker"

cat > "$TEST_ROOT/bin/curl" <<'EOF'
#!/bin/sh
printf '200'
EOF
chmod +x "$TEST_ROOT/bin/curl"

cat > "$TEST_ROOT/bin/chown" <<'EOF'
#!/bin/sh
exit 0
EOF
chmod +x "$TEST_ROOT/bin/chown"

run_update() {
  FAKE_DB_HEALTH="$1" \
  FAKE_LOG="$TEST_ROOT/calls.log" \
  PROJECT_DIR="$TEST_ROOT/project" \
  PROJECT_NAME="synology-update-test" \
  EXPECTED_DATA_ROOT="$TEST_ROOT/data" \
  EXPECTED_PORT=8090 \
  PATH="$TEST_ROOT/bin:$PATH" \
    sh "$SCRIPT_DIR/synology-update.sh"
}

# A broken database must abort before pulling images, taking a backup or
# recreating containers.
: > "$TEST_ROOT/calls.log"

if run_update unhealthy; then
  echo "unhealthy database must abort the update" >&2
  exit 1
fi

if grep -q '^pull$' "$TEST_ROOT/calls.log"; then
  echo "unhealthy database must not pull application images" >&2
  exit 1
fi

if grep -q '^up$' "$TEST_ROOT/calls.log"; then
  echo "unhealthy database must not recreate containers" >&2
  exit 1
fi

if find "$TEST_ROOT/data/pre-update" -type f 2>/dev/null | grep -q .; then
  echo "unhealthy database must not create a pre-update dump" >&2
  exit 1
fi

# A healthy stack must pull the app images, create a compressed backup and
# recreate the stack with the current .env / compose configuration.
: > "$TEST_ROOT/calls.log"

run_update healthy

grep -q '^pull$' "$TEST_ROOT/calls.log" || {
  echo "healthy update must pull backend and web" >&2
  exit 1
}

grep -q '^up$' "$TEST_ROOT/calls.log" || {
  echo "healthy update must recreate the stack" >&2
  exit 1
}

if ! find "$TEST_ROOT/data/pre-update" -name '*.sql.gz' -type f | grep -q .; then
  echo "healthy update must create a compressed pre-update dump" >&2
  exit 1
fi

echo "synology update task tests passed"
