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
EOF

cat > "$TEST_ROOT/bin/docker" <<'EOF'
#!/bin/sh
set -eu

if [ "$1" = "compose" ] && [ "$2" = "version" ]; then
  exit 0
fi

if [ "$1" = "compose" ]; then
  shift
  while [ "$1" = "--env-file" ] || [ "$1" = "-f" ]; do
    shift 2
  done
  case "$*" in
    "ps -q backend") echo backend-container ;;
    "ps -q web") echo web-container ;;
    "pull backend web") echo pull >> "$FAKE_LOG" ;;
    "exec -T db "*) echo 'CREATE TABLE test (id INT);' ;;
    "up -d --no-deps backend web") echo up >> "$FAKE_LOG" ;;
    "ps") echo healthy ;;
    *) echo "unexpected compose call: $*" >&2; exit 1 ;;
  esac
  exit 0
fi

if [ "$1" = "inspect" ]; then
  case "$*" in
    *State.Health*backend-container|*State.Health*web-container) echo healthy ;;
    *backend-container)
      if [ "$FAKE_UPDATE" = "1" ]; then echo sha256:backend-old; else echo sha256:backend-new; fi
      ;;
    *web-container)
      if [ "$FAKE_UPDATE" = "1" ]; then echo sha256:web-old; else echo sha256:web-new; fi
      ;;
    *) echo "unexpected inspect call: $*" >&2; exit 1 ;;
  esac
  exit 0
fi

if [ "$1" = "image" ] && [ "$2" = "inspect" ]; then
  case "$*" in
    *-web:latest) echo sha256:web-new ;;
    *:latest) echo sha256:backend-new ;;
    *) echo "unexpected image inspect call: $*" >&2; exit 1 ;;
  esac
  exit 0
fi

echo "unexpected docker call: $*" >&2
exit 1
EOF
chmod +x "$TEST_ROOT/bin/docker"

run_update() {
  FAKE_UPDATE="$1" \
  FAKE_LOG="$TEST_ROOT/calls.log" \
  PROJECT_DIR="$TEST_ROOT/project" \
  PATH="$TEST_ROOT/bin:$PATH" \
    sh "$SCRIPT_DIR/synology-update.sh"
}

: > "$TEST_ROOT/calls.log"
run_update 0
if grep -q '^up$' "$TEST_ROOT/calls.log"; then
  echo "same images must not recreate containers" >&2
  exit 1
fi
if find "$TEST_ROOT/data/pre-update" -type f 2>/dev/null | grep -q .; then
  echo "same images must not create a pre-update dump" >&2
  exit 1
fi

: > "$TEST_ROOT/calls.log"
run_update 1
if ! grep -q '^up$' "$TEST_ROOT/calls.log"; then
  echo "changed images must recreate app containers" >&2
  exit 1
fi
if ! find "$TEST_ROOT/data/pre-update" -name '*.sql.gz' -type f | grep -q .; then
  echo "an update must create a compressed pre-update dump" >&2
  exit 1
fi

echo "synology update task tests passed"
