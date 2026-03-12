#!/usr/bin/env bash
#
# test-platforms.sh — Test portago compatibility across Linux distributions.
#
# Modes:
#   (default)   Cross-compile the flatpack binary and test --version on each distro
#   --full      Also run --setup on Ubuntu 24.04 (downloads nvim, plugins, ~5min)
#   --package   Run full 'make package' inside a Linux container, then test the
#               bundled binary on each distro (~10min, requires internet)
#
# Prerequisites: Docker
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
VERSION="${VERSION:-test}"

FULL=false
PACKAGE=false
for arg in "$@"; do
  case "$arg" in
    --full)    FULL=true ;;
    --package) PACKAGE=true ;;
    *)         echo "Usage: $0 [--full] [--package]"; exit 1 ;;
  esac
done

# Verify Docker is available before running any tests
if ! command -v docker &>/dev/null; then
  echo "ERROR: Docker is required but not found in PATH." >&2
  exit 1
fi
if ! docker info &>/dev/null; then
  echo "ERROR: Docker daemon is not running." >&2
  exit 1
fi

cd "$PROJECT_DIR"
mkdir -p dist

DISTROS=(
  "ubuntu:22.04"
  "ubuntu:24.04"
  "debian:12"
  "fedora:latest"
  "archlinux:latest"
  "alpine:latest"
)

# ---------------------------------------------------------------------------
# Step 1: Cross-compile the flatpack (online) binary for linux/amd64
# ---------------------------------------------------------------------------
echo "==> Cross-compiling portago (flatpack) for linux/amd64..."
tar czf bundle.tar.gz --files-from /dev/null
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
  -ldflags "-s -w -X main.version=$VERSION" \
  -o dist/portago-test-linux-amd64 .
echo ""

# ---------------------------------------------------------------------------
# Step 2: Test --version on each distro
# ---------------------------------------------------------------------------
echo "==> Testing --version on each distribution..."
echo ""

pass=0
fail=0
failed_distros=""

for distro in "${DISTROS[@]}"; do
  printf "  %-25s" "$distro"

  # Pull quietly if needed
  if ! docker pull -q --platform linux/amd64 "$distro" >/dev/null 2>&1; then
    echo "WARN: pull failed, using cached image (if any)" >&2
  fi

  set +e
  output=$(docker run --rm --platform linux/amd64 \
    -v "$PROJECT_DIR/dist:/dist:ro" \
    "$distro" /dist/portago-test-linux-amd64 --version 2>&1)
  exit_code=$?
  set -e

  if [ $exit_code -eq 0 ] && echo "$output" | grep -q "portago"; then
    echo "PASS  ($output)"
    pass=$((pass + 1))
  else
    echo "FAIL"
    if [ -n "$output" ]; then
      echo "        $output" | head -3
    fi
    fail=$((fail + 1))
    failed_distros="$failed_distros $distro"
  fi
done

echo ""
echo "==> Results: $pass passed, $fail failed"
if [ $fail -gt 0 ]; then
  echo "    Failed:$failed_distros"
fi

exit_with_failure=false
if [ $fail -gt 0 ]; then
  exit_with_failure=true
fi

# ---------------------------------------------------------------------------
# Step 3 (optional): Full --setup test on Ubuntu 24.04
# ---------------------------------------------------------------------------
if $FULL; then
  echo ""
  echo "==> Full test: running --setup on Ubuntu 24.04..."
  echo "    (downloads nvim, installs plugins, compiles parsers)"
  echo ""
  if ! docker run --rm --platform linux/amd64 \
    -v "$PROJECT_DIR/dist:/dist:ro" \
    ubuntu:24.04 bash -c '
      set -e
      apt-get update -qq >/dev/null
      apt-get install -y -qq git make gcc curl ca-certificates >/dev/null
      cp /dist/portago-test-linux-amd64 /usr/local/bin/portago
      chmod +x /usr/local/bin/portago
      portago --setup
      echo ""
      echo "==> Setup completed successfully on Ubuntu 24.04"
      portago --version
    '; then
    echo "ERROR: Full setup test FAILED on Ubuntu 24.04." >&2
    exit 1
  fi
fi

# ---------------------------------------------------------------------------
# Step 4 (optional): Build bundled binary inside Linux, test on each distro
# ---------------------------------------------------------------------------
if $PACKAGE; then
  echo ""
  echo "==> Building bundled portago inside Ubuntu 24.04 container..."
  echo "    (full make package — downloads nvim, plugins, tools, ~10min)"
  echo ""

  # We need a Go image that has the right version. Use the host Go to
  # determine which golang Docker image to use.
  GO_VERSION=$(go env GOVERSION | sed 's/^go//')
  GO_MAJOR_MINOR=$(echo "$GO_VERSION" | grep -oE '^[0-9]+\.[0-9]+')
  GO_IMAGE="golang:${GO_MAJOR_MINOR}-bookworm"

  echo "    Using Docker image: $GO_IMAGE"
  if ! docker pull -q "$GO_IMAGE" >/dev/null 2>&1; then
    echo "WARN: pull failed for $GO_IMAGE, using cached image (if any)" >&2
  fi

  if ! docker run --rm --platform linux/amd64 \
    -v "$PROJECT_DIR:/src" \
    -w /src \
    -e VERSION="$VERSION" \
    "$GO_IMAGE" bash -c '
      set -e
      apt-get update -qq >/dev/null
      apt-get install -y -qq git >/dev/null
      rm -rf dist/ bundle.tar.gz .staging/
      make package
    '; then
    echo "ERROR: Bundled build FAILED inside container." >&2
    exit 1
  fi

  if [ ! -f "$PROJECT_DIR/dist/portago" ]; then
    echo "ERROR: dist/portago not found. Build may have failed." >&2
    exit 1
  fi

  echo ""
  echo "==> Testing bundled binary on each distribution..."
  echo ""

  bpass=0
  bfail=0
  bfailed_distros=""

  for distro in "${DISTROS[@]}"; do
    printf "  %-25s" "$distro"

    set +e
    output=$(docker run --rm --platform linux/amd64 \
      -v "$PROJECT_DIR/dist:/dist:ro" \
      "$distro" /bin/sh -c '
        cp /dist/portago /tmp/portago || { echo "FAIL: cannot copy binary"; exit 1; }
        chmod +x /tmp/portago || { echo "FAIL: cannot set execute permission"; exit 1; }
        /tmp/portago --version
      ' 2>&1)
    exit_code=$?
    set -e

    if [ $exit_code -eq 0 ] && echo "$output" | grep -q "fully bundled"; then
      echo "PASS  ($output)"
      bpass=$((bpass + 1))
    else
      echo "FAIL"
      if [ -n "$output" ]; then
        echo "        $output" | head -3
      fi
      bfail=$((bfail + 1))
      bfailed_distros="$bfailed_distros $distro"
    fi
  done

  echo ""
  echo "==> Bundled results: $bpass passed, $bfail failed"
  if [ $bfail -gt 0 ]; then
    echo "    Failed:$bfailed_distros"
    exit_with_failure=true
  fi
fi

# ---------------------------------------------------------------------------
# Cleanup
# ---------------------------------------------------------------------------
rm -f dist/portago-test-linux-amd64
echo ""
echo "==> Platform testing complete."

if [ "${exit_with_failure:-false}" = true ]; then
  exit 1
fi
