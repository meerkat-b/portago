#!/usr/bin/env bash
#
# package.sh — Build a fully self-contained portago bundle.
#
# This script:
#   1. Runs portago setup into a staging directory
#   2. Strips unnecessary files (.git dirs, clangd)
#   3. Compresses everything into bundle.tar.gz
#   4. Rebuilds the Go binary with the bundle embedded
#
# Prerequisites: go, nvim (or a previously built portago), git, internet
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
STAGING_DIR="$PROJECT_DIR/.staging"
BUNDLE_FILE="$PROJECT_DIR/bundle.tar.gz"

VERSION="${VERSION:-$(cd "$PROJECT_DIR" && git describe --tags --always --dirty 2>/dev/null || echo "dev")}"

echo "==> Packaging portago $VERSION for $(uname -s)/$(uname -m)"

# Clean previous staging. Go module cache files are read-only,
# so make everything writable first.
chmod -R u+w "$STAGING_DIR" 2>/dev/null || true
rm -rf "$STAGING_DIR" "$BUNDLE_FILE"
mkdir -p "$STAGING_DIR"

# --- Step 1: Build a lightweight portago (without bundle) to run setup ---
echo "==> Building bootstrap binary..."
cd "$PROJECT_DIR"
# Create an empty bundle.tar.gz so the embed directive is satisfied
tar czf "$BUNDLE_FILE" --files-from /dev/null
go build -ldflags "-X main.version=$VERSION" -o "$STAGING_DIR/portago-bootstrap" .

# --- Step 2: Run setup using the bootstrap binary ---
echo "==> Running setup to populate staging directory..."
HOME_DIR="$STAGING_DIR/home"
mkdir -p "$HOME_DIR"

# Keep Go module cache in the system location, not in the staging home
HOME="$HOME_DIR" GOMODCACHE="${GOMODCACHE:-$(go env GOMODCACHE)}" "$STAGING_DIR/portago-bootstrap" --setup </dev/null || true

PORTAGO_HOME="$HOME_DIR/.portago"

if [ ! -d "$PORTAGO_HOME/nvim/bin" ]; then
  echo "ERROR: Setup did not produce a valid ~/.portago directory"
  exit 1
fi

# --- Step 3: Strip unnecessary files to reduce size ---
echo "==> Stripping unnecessary files..."

# Remove .git directories from plugins (saves ~40MB)
find "$PORTAGO_HOME/data/config/lazy" -name ".git" -type d -exec rm -rf {} + 2>/dev/null || true

# Remove clangd (164MB — this is a Go IDE)
rm -rf "$PORTAGO_HOME/data/config/mason/packages/clangd"
rm -f "$PORTAGO_HOME/data/config/mason/bin/clangd"

# Resolve symlinks to real files throughout the data directory.
# nvim-treesitter and other plugins create symlinks that will break
# when extracted on a different machine.
echo "==> Resolving symlinks to real files..."
find "$PORTAGO_HOME" -type l | while read -r link; do
  target="$(readlink -f "$link" 2>/dev/null || true)"
  if [ -f "$target" ]; then
    rm "$link"
    cp "$target" "$link"
  elif [ -d "$target" ]; then
    rm "$link"
    cp -R "$target" "$link"
  fi
done

# Fix Mason wrapper scripts that have hardcoded staging paths.
# Replace references to the staging PORTAGO_HOME with a placeholder
# that will be resolved at runtime (~/.portago).
echo "==> Fixing Mason wrapper scripts..."
find "$PORTAGO_HOME/data/config/mason/bin" -type f | while read -r f; do
  if head -1 "$f" 2>/dev/null | grep -q "^#!"; then
    sed -i '' "s|$PORTAGO_HOME|PORTAGO_HOME_PLACEHOLDER|g" "$f"
  fi
done

# Ensure treesitter queries exist in site/queries/ as real files
SITE_QUERIES="$PORTAGO_HOME/data/config/site/queries"
TS_QUERIES="$PORTAGO_HOME/data/config/lazy/nvim-treesitter/runtime/queries"
if [ -d "$TS_QUERIES" ]; then
  echo "==> Copying treesitter queries to site/queries/..."
  rm -rf "$SITE_QUERIES"
  mkdir -p "$SITE_QUERIES"
  cp -R "$TS_QUERIES"/* "$SITE_QUERIES"/
fi

# Remove cache/state artifacts
rm -rf "$PORTAGO_HOME/cache"
rm -rf "$PORTAGO_HOME/state"
rm -f "$PORTAGO_HOME/.setup-done"

# Remove the bootstrap binary
rm -f "$STAGING_DIR/portago-bootstrap"

# --- Step 4: Create the compressed bundle ---
echo "==> Creating bundle.tar.gz..."
cd "$PORTAGO_HOME"
# Bundle contains: nvim/, config/, data/
COPYFILE_DISABLE=1 tar czf "$BUNDLE_FILE" --exclude='._*' --exclude='.DS_Store' nvim config data

BUNDLE_SIZE=$(ls -lh "$BUNDLE_FILE" | awk '{print $5}')
echo "==> Bundle created: $BUNDLE_FILE ($BUNDLE_SIZE)"

# --- Step 5: Rebuild the Go binary with the bundle embedded ---
echo "==> Building final portago binary with embedded bundle..."
cd "$PROJECT_DIR"
go build -ldflags "-s -w -X main.version=$VERSION" -o dist/portago .

BINARY_SIZE=$(ls -lh dist/portago | awk '{print $5}')
echo "==> Final binary: dist/portago ($BINARY_SIZE)"

# Clean up (Go module cache files are read-only)
chmod -R u+w "$STAGING_DIR" 2>/dev/null || true
rm -rf "$STAGING_DIR"

echo "==> Done! Fully self-contained binary at: $PROJECT_DIR/dist/portago"
