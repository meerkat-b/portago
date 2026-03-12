#!/usr/bin/env bash
set -euo pipefail

# Resolve portago root
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PORTAGO_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

export XDG_CONFIG_HOME="$PORTAGO_DIR"
export XDG_DATA_HOME="$PORTAGO_DIR/data"
export XDG_STATE_HOME="$PORTAGO_DIR/state"
export XDG_CACHE_HOME="$PORTAGO_DIR/cache"
export NVIM_APPNAME="config"

mkdir -p "$PORTAGO_DIR"/{data,state,cache}

echo "==> Installing plugins via lazy.nvim..."
nvim --headless "+Lazy! sync" +qa 2>&1

echo "==> Installing Mason tools (gopls, delve, gomodifytags, impl)..."
nvim --headless "+MasonToolsInstallSync" +qa 2>&1

# Mason installs tree-sitter-cli which is needed to compile TreeSitter parsers.
# Wait for the binary to be available before proceeding.
TS_CLI="$PORTAGO_DIR/data/config/mason/bin/tree-sitter"
echo "==> Waiting for tree-sitter-cli to be available..."
for i in $(seq 1 30); do
  [ -x "$TS_CLI" ] && break
  sleep 1
done
if [ ! -x "$TS_CLI" ]; then
  echo "WARNING: tree-sitter-cli not found at $TS_CLI after 30s, parsers may fail to compile"
fi

echo "==> Installing TreeSitter parsers..."
nvim --headless "+lua require('nvim-treesitter').install({'bash','diff','go','lua','luadoc','markdown','markdown_inline','query','vim','vimdoc'})" +qa 2>&1

echo "==> Setup complete. Run portago with: $PORTAGO_DIR/bin/portago"
