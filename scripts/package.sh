#!/usr/bin/env bash
#
# package.sh — Build a fully self-contained portago bundle.
#
# This script:
#   1. Builds a bootstrap binary (lightweight, no bundle)
#   2. Runs setup to download nvim, plugins, Mason tools, and treesitter parsers
#   3. Aggressively strips unneeded files (plugins, tools, runtime, metadata)
#   4. Compresses everything into bundle.tar.gz
#   5. Rebuilds the Go binary with the bundle embedded
#
# Prerequisites: go, git, internet
#
set -euo pipefail

# Portable readlink -f (macOS BSD readlink doesn't support -f)
resolve_path() {
  (
    local result
    if result=$(readlink -f "$1" 2>/dev/null) && [ -n "$result" ]; then
      echo "$result"
      return
    fi
    # Fallback: Python 3 (available if Xcode CLI tools are installed on macOS)
    if result=$(python3 -c "import os,sys; print(os.path.realpath(sys.argv[1]))" "$1" 2>/dev/null) && [ -n "$result" ]; then
      echo "$result"
      return
    fi
    # Last resort: manual symlink resolution
    local target="$1"
    local dir
    while [ -L "$target" ]; do
      dir="$(cd -P "$(dirname "$target")" && pwd)" || return 1
      target="$(readlink "$target")" || return 1
      [[ "$target" != /* ]] && target="$dir/$target"
    done
    cd -P "$(dirname "$target")" && echo "$(pwd)/$(basename "$target")"
  )
}

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
STAGING_DIR="$PROJECT_DIR/.staging"
BUNDLE_FILE="$PROJECT_DIR/bundle.tar.gz"

if [ -z "${VERSION:-}" ]; then
  VERSION="$(cd "$PROJECT_DIR" && git describe --tags --always --dirty 2>/dev/null)" || true
  if [ -z "$VERSION" ]; then
    echo "ERROR: git describe failed and VERSION not set. Set VERSION explicitly or ensure a git tag exists." >&2
    exit 1
  fi
fi

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

# Also build the flatpack (online) binary while the empty bundle is in place
echo "==> Building flatpack (online) binary..."
mkdir -p "$PROJECT_DIR/dist"
go build -ldflags "-s -w -X main.version=$VERSION" -o "$PROJECT_DIR/dist/portago-flatpack" .

# --- Step 2: Run setup using the bootstrap binary ---
echo "==> Running setup to populate staging directory..."
HOME_DIR="$STAGING_DIR/home"
mkdir -p "$HOME_DIR"

# Keep Go module cache in the system location, not in the staging home
if ! HOME="$HOME_DIR" GOMODCACHE="${GOMODCACHE:-$(go env GOMODCACHE)}" "$STAGING_DIR/portago-bootstrap" --setup </dev/null; then
  echo "ERROR: Bootstrap setup failed (exit code $?). Check output above for details."
  exit 1
fi

PORTAGO_HOME="$HOME_DIR/.portago"

# Validate all critical outputs from setup
for required_dir in \
  "$PORTAGO_HOME/nvim/bin" \
  "$PORTAGO_HOME/config" \
  "$PORTAGO_HOME/data/config/lazy" \
  "$PORTAGO_HOME/data/config/mason/bin"; do
  if [ ! -d "$required_dir" ]; then
    echo "ERROR: Setup did not produce expected directory: $required_dir"
    exit 1
  fi
done

# --- Step 3: Strip unnecessary files to reduce size ---
echo "==> Stripping unnecessary files..."

# Remove .git directories from plugins (saves ~40MB)
find "$PORTAGO_HOME/data/config/lazy" -name ".git" -type d -exec rm -rf {} + 2>/dev/null || true

# Remove plugin test/doc/spec directories (not needed at runtime)
find "$PORTAGO_HOME/data/config/lazy" -type d \( -name "test" -o -name "tests" -o -name "spec" -o -name "doc" \) -exec rm -rf {} + 2>/dev/null || true

# Defensively remove tools that Mason or its dependencies may install
# opportunistically, even though they are not in our ensure_installed list.
# tree-sitter-cli is installed explicitly for parser compilation but not needed at runtime.
rm -rf "$PORTAGO_HOME/data/config/mason/packages/clangd"
rm -f "$PORTAGO_HOME/data/config/mason/bin/clangd"
rm -rf "$PORTAGO_HOME/data/config/mason/packages/tree-sitter-cli"
rm -f "$PORTAGO_HOME/data/config/mason/bin/tree-sitter"
rm -rf "$PORTAGO_HOME/data/config/mason/packages/lua-language-server"
rm -f "$PORTAGO_HOME/data/config/mason/bin/lua-language-server"
rm -rf "$PORTAGO_HOME/data/config/mason/packages/stylua"
rm -f "$PORTAGO_HOME/data/config/mason/bin/stylua"

# Strip plugin metadata files (README, LICENSE, CHANGELOG, .github, etc.)
echo "==> Stripping plugin metadata..."
find "$PORTAGO_HOME/data/config/lazy" -type d -name ".github" -exec rm -rf {} + 2>/dev/null || true
find "$PORTAGO_HOME/data/config/lazy" -maxdepth 2 -type f \( \
  -iname "README*" -o -iname "LICENSE*" -o -iname "CHANGELOG*" \
  -o -iname "CONTRIBUTING*" -o -iname "CODE_OF_CONDUCT*" \
  -o -name "*.md" -o -name ".editorconfig" -o -name ".luacheckrc" \
  -o -name ".stylua.toml" -o -name ".gitignore" \
  \) -delete 2>/dev/null || true
# Remove lazy.nvim lockfile/manifest (large, regenerated on use)
rm -f "$PORTAGO_HOME/data/config/lazy/lazy.nvim/manifest"

# Strip Go tool binaries (remove debug symbols and symbol tables)
echo "==> Stripping Go tool binaries..."
PERM_FLAG="-perm +111"
if [[ "$OSTYPE" != "darwin"* ]]; then
  PERM_FLAG="-perm /111"
fi
find "$PORTAGO_HOME/data/config/mason/packages" -type f $PERM_FLAG | while read -r bin; do
  if file "$bin" | grep -q "Mach-O\|ELF"; then
    strip "$bin" 2>/dev/null || true
  fi
done

# Prune nvim runtime to only languages needed for a Go IDE
echo "==> Pruning nvim runtime to Go-relevant languages..."
NVIM_RUNTIME="$PORTAGO_HOME/nvim/share/nvim/runtime"
KEEP_LANGS="go lua vim markdown sh bash zsh yaml json toml diff help"

# Core nvim runtime files that must not be removed (syntax engine bootstrap, etc.)
CORE_FILES="syntax synload syncolor nosyntax manual"

for dir in syntax ftplugin indent compiler; do
  if [ -d "$NVIM_RUNTIME/$dir" ]; then
    # Build a find expression that excludes files we want to keep
    FIND_EXCLUDE=""
    for lang in $KEEP_LANGS; do
      FIND_EXCLUDE="$FIND_EXCLUDE -not -name ${lang}.vim -not -name ${lang}.lua"
    done
    for core in $CORE_FILES; do
      FIND_EXCLUDE="$FIND_EXCLUDE -not -name ${core}.vim -not -name ${core}.lua"
    done
    # Delete files that don't match any kept language or core file
    find "$NVIM_RUNTIME/$dir" -maxdepth 1 -type f $FIND_EXCLUDE -delete 2>/dev/null || true
    # Remove subdirectories for languages we don't need (some ftplugins have subdirs)
    find "$NVIM_RUNTIME/$dir" -mindepth 1 -maxdepth 1 -type d | while read -r subdir; do
      dirname="$(basename "$subdir")"
      keep=false
      for lang in $KEEP_LANGS; do
        if [ "$dirname" = "$lang" ]; then keep=true; break; fi
      done
      for core in $CORE_FILES; do
        if [ "$dirname" = "$core" ]; then keep=true; break; fi
      done
      if [ "$keep" = false ]; then rm -rf "$subdir"; fi
    done
  fi
done

# Remove nvim runtime directories not needed for a Go IDE
rm -rf "$NVIM_RUNTIME/tutor"
rm -rf "$NVIM_RUNTIME/keymap"

# Remove duplicate parser .so files from nvim/lib/ (superseded by site/parser/)
echo "==> Removing duplicate nvim parsers superseded by site/..."
NVIM_PARSERS="$PORTAGO_HOME/nvim/lib/nvim/parser"
SITE_PARSERS="$PORTAGO_HOME/data/config/site/parser"
if [ -d "$NVIM_PARSERS" ] && [ -d "$SITE_PARSERS" ]; then
  for so in "$NVIM_PARSERS"/*.so; do
    name="$(basename "$so")"
    if [ -f "$SITE_PARSERS/$name" ]; then
      rm -f "$so"
    fi
  done
fi

# Resolve symlinks to real files throughout the data directory.
# nvim-treesitter and other plugins create symlinks that will break
# when extracted on a different machine.
echo "==> Resolving symlinks to real files..."
while IFS= read -r link; do
  target="$(resolve_path "$link")" || true
  if [ -z "$target" ]; then
    echo "  WARNING: Cannot resolve symlink, removing: $link"
    rm -f "$link"
    continue
  fi
  if [ -f "$target" ]; then
    cp "$target" "${link}.tmp" && rm "$link" && mv "${link}.tmp" "$link"
  elif [ -d "$target" ]; then
    cp -R "$target" "${link}.tmp" && rm "$link" && mv "${link}.tmp" "$link"
  else
    echo "  WARNING: Dangling symlink, removing: $link -> $target"
    rm -f "$link"
  fi
done < <(find "$PORTAGO_HOME" -type l)

# Fix Mason wrapper scripts that have hardcoded staging paths.
# Replace references to the staging PORTAGO_HOME with a placeholder
# that will be resolved at runtime to the actual portagoHome path.
echo "==> Fixing Mason wrapper scripts..."
find "$PORTAGO_HOME/data/config/mason/bin" -type f | while read -r f; do
  if head -1 "$f" 2>/dev/null | grep -q "^#!"; then
    if [[ "$OSTYPE" == "darwin"* ]]; then
      sed -i '' "s|$PORTAGO_HOME|PORTAGO_HOME_PLACEHOLDER|g" "$f"
    else
      sed -i "s|$PORTAGO_HOME|PORTAGO_HOME_PLACEHOLDER|g" "$f"
    fi
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

# Prune treesitter queries to only the languages we use
echo "==> Pruning treesitter queries to used languages..."
USED_QUERY_LANGS="bash diff go lua luadoc markdown markdown_inline query vim vimdoc"
if [ -d "$SITE_QUERIES" ]; then
  for qdir in "$SITE_QUERIES"/*/; do
    lang="$(basename "$qdir")"
    keep=false
    for used in $USED_QUERY_LANGS; do
      if [ "$lang" = "$used" ]; then keep=true; break; fi
    done
    if [ "$keep" = false ]; then rm -rf "$qdir"; fi
  done
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

BUNDLED_SIZE=$(ls -lh dist/portago | awk '{print $5}')
FLATPACK_SIZE=$(ls -lh dist/portago-flatpack | awk '{print $5}')
echo "==> Bundled binary:  dist/portago ($BUNDLED_SIZE)"
echo "==> Flatpack binary: dist/portago-flatpack ($FLATPACK_SIZE)"

# Clean up (Go module cache files are read-only)
chmod -R u+w "$STAGING_DIR" 2>/dev/null || true
rm -rf "$STAGING_DIR"

echo "==> Done! Both binaries at: $PROJECT_DIR/dist/"
echo "    portago          — fully self-contained, no internet needed"
echo "    portago-flatpack — lightweight, downloads dependencies on first run"
