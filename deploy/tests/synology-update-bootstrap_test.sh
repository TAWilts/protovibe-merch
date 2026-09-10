#!/bin/sh
set -eu

SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
TEST_ROOT="$(mktemp -d)"
trap 'rm -rf "$TEST_ROOT"' EXIT HUP INT TERM

mkdir -p "$TEST_ROOT/bin" "$TEST_ROOT/project"

cat > "$TEST_ROOT/project/.env" <<EOF
MERCH_IMAGE_REPOSITORY=ghcr.io/example/merch
MERCH_IMAGE_TAG=v9.9.9
ENVIRONMENT=production
EOF

cat > "$TEST_ROOT/bin/docker" <<'EOF'
#!/bin/sh
set -eu

case "$1" in
  pull)
    echo "pull:$2" >> "$FAKE_LOG"
    exit 0
    ;;
  create)
    echo "create:$2" >> "$FAKE_LOG"
    echo fake-container
    exit 0
    ;;
  cp)
    echo "cp:$2:$3" >> "$FAKE_LOG"
    cat > "$3" <<'SCRIPT'
#!/bin/sh
echo "updater-ran:$PROJECT_DIR" >> "$FAKE_LOG"
SCRIPT
    exit 0
    ;;
  rm)
    echo "rm:$*" >> "$FAKE_LOG"
    exit 0
    ;;
esac

echo "unexpected docker call: $*" >&2
exit 1
EOF
chmod +x "$TEST_ROOT/bin/docker"

: > "$TEST_ROOT/calls.log"

FAKE_LOG="$TEST_ROOT/calls.log" \
PROJECT_DIR="$TEST_ROOT/project" \
PATH="$TEST_ROOT/bin:$PATH" \
  sh "$SCRIPT_DIR/synology-update-bootstrap.sh"

UPDATE_FILE="$TEST_ROOT/project/synology-update.sh"

[ -s "$UPDATE_FILE" ] || {
  echo "bootstrap must install synology-update.sh" >&2
  exit 1
}

grep -q '^pull:ghcr.io/example/merch:v9.9.9$' "$TEST_ROOT/calls.log" || {
  echo "bootstrap must pull the configured backend image" >&2
  exit 1
}

grep -q '^create:ghcr.io/example/merch:v9.9.9$' "$TEST_ROOT/calls.log" || {
  echo "bootstrap must create a temporary container from the configured image" >&2
  exit 1
}

grep -q 'fake-container:/usr/local/share/merch-manager/synology-update.sh' "$TEST_ROOT/calls.log" || {
  echo "bootstrap must copy the versioned updater from the image" >&2
  exit 1
}

grep -q "^updater-ran:$TEST_ROOT/project$" "$TEST_ROOT/calls.log" || {
  echo "bootstrap must execute the freshly installed updater" >&2
  exit 1
}

# A development server follows the image published by every successful push,
# even when MERCH_IMAGE_TAG still points at the production release channel.
cat > "$TEST_ROOT/project/.env" <<EOF
MERCH_IMAGE_REPOSITORY=ghcr.io/example/merch
MERCH_IMAGE_TAG=latest
ENVIRONMENT=development
EOF
: > "$TEST_ROOT/calls.log"

FAKE_LOG="$TEST_ROOT/calls.log" \
PROJECT_DIR="$TEST_ROOT/project" \
PATH="$TEST_ROOT/bin:$PATH" \
  sh "$SCRIPT_DIR/synology-update-bootstrap.sh"

grep -q '^pull:ghcr.io/example/merch:development$' "$TEST_ROOT/calls.log" || {
  echo "development bootstrap must pull the development backend image" >&2
  exit 1
}

grep -q '^create:ghcr.io/example/merch:development$' "$TEST_ROOT/calls.log" || {
  echo "development bootstrap must extract the updater from the development image" >&2
  exit 1
}

echo "synology updater bootstrap tests passed"
