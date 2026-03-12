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
  docker pull -q --platform linux/amd64 "$distro" >/dev/null 2>&1 || true

  output=$(docker run --rm --platform linux/amd64 \
    -v "$PROJECT_DIR/dist:/dist:ro" \
    "$distro" /dist/portago-test-linux-amd64 --version 2>&1) || true

  if echo "$output" | grep -q "portago"; then
    echo "PASS  ($output)"
    ((pass++))
  else
    echo "FAIL"
    if [ -n "$output" ]; then
      echo "        $output" | head -3
    fi
    ((fail++))
    failed_distros="$failed_distros $distro"
  fi
done

echo ""
echo "==> Results: $pass passed, $fail failed"
if [ $fail -gt 0 ]; then
  echo "    Failed:$failed_distros"
fi

# ---------------------------------------------------------------------------
# Step 3 (optional): Full --setup test on Ubuntu 24.04
# ---------------------------------------------------------------------------
if $FULL; then
  echo ""
  echo "==> Full test: running --setup on Ubuntu 24.04..."
  echo "    (downloads nvim, installs plugins, compiles parsers)"
  echo ""
  docker run --rm --platform linux/amd64 \
    -v "$PROJECT_DIR/dist:/dist:ro" \
    ubuntu:24.04 bash -c '
      set -e
      apt-get update -qq >/dev/null
      apt-get install -y -qq git make gcc curl ca-certificates >/dev/null 2>&1
      cp /dist/portago-test-linux-amd64 /usr/local/bin/portago
      chmod +x /usr/local/bin/portago
      portago --setup
      echo ""
      echo "==> Setup completed successfully on Ubuntu 24.04"
      portago --version
    '
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
  docker pull -q "$GO_IMAGE" >/dev/null 2>&1 || true

  docker run --rm --platform linux/amd64 \
    -v "$PROJECT_DIR:/src" \
    -w /src \
    -e VERSION="$VERSION" \
    "$GO_IMAGE" bash -c '
      set -e
      apt-get update -qq >/dev/null
      apt-get install -y -qq git >/dev/null 2>&1
      make clean 2>/dev/null || true
      make package
    '

  echo ""
  echo "==> Testing bundled binary on each distribution..."
  echo ""

  bpass=0
  bfail=0

  for distro in "${DISTROS[@]}"; do
    printf "  %-25s" "$distro"

    output=$(docker run --rm --platform linux/amd64 \
      -v "$PROJECT_DIR/dist:/dist:ro" \
      "$distro" bash -c '
        cp /dist/portago /tmp/portago 2>/dev/null || cp /dist/portago /usr/local/bin/portago 2>/dev/null
        chmod +x /tmp/portago 2>/dev/null || chmod +x /usr/local/bin/portago 2>/dev/null
        /tmp/portago --version 2>/dev/null || /usr/local/bin/portago --version
      ' 2>&1) || true

    if echo "$output" | grep -q "fully bundled"; then
      echo "PASS  ($output)"
      ((bpass++))
    else
      echo "FAIL"
      if [ -n "$output" ]; then
        echo "        $output" | head -3
      fi
      ((bfail++))
    fi
  done

  echo ""
  echo "==> Bundled results: $bpass passed, $bfail failed"
fi

# ---------------------------------------------------------------------------
# Cleanup
# ---------------------------------------------------------------------------
rm -f dist/portago-test-linux-amd64
echo ""
echo "==> Platform testing complete."
