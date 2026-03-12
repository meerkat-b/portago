# portago

A portable, single-binary Go IDE built on Neovim. One download, no setup, no dependencies — just run it.

Portago embeds a full Neovim configuration, plugins, LSP servers, debugger, and treesitter parsers into a single Go binary using `go:embed`. It works completely offline with zero configuration.

## Features

- **Single binary** — everything is embedded: nvim, gopls, delve, treesitter parsers, plugins
- **Zero config** — works out of the box with a curated Go development setup
- **Fully offline** (bundled mode) — no internet needed after download
- **Portable** — no system-level installation, doesn't touch your existing nvim config
- **Two modes:**
  - **Bundled** — fully self-contained, extracts to a temp cache on first run
  - **Flatpack** (~6MB) — lightweight, downloads dependencies on first run

## What's included

| Category | Tools |
|----------|-------|
| **LSP** | gopls |
| **Debugger** | delve (nvim-dap + nvim-dap-go) |
| **Go tools** | gomodifytags, impl (via gopher.nvim) |
| **Completion** | blink.cmp with LSP + snippet support |
| **Navigation** | telescope.nvim (fuzzy finder), nvim-tree (file explorer) |
| **Diagnostics** | trouble.nvim, inline diagnostics |
| **Git** | gitsigns.nvim |
| **Treesitter** | syntax highlighting for Go, Lua, Markdown, Bash, Vim, and more |
| **Theme** | tokyonight-night with high-contrast tweaks |
| **Other** | which-key, conform (autoformat), mini.nvim (statusline, surround, textobjects), autopairs |

## Quick start

### Download a release

```bash
# macOS (Apple Silicon)
curl -Lo portago https://github.com/meerkat-b/portago/releases/latest/download/portago-darwin-arm64
chmod +x portago
./portago myproject/
```

### Build from source

```bash
git clone https://github.com/meerkat-b/portago.git
cd portago

# Option 1: Build the flatpack (lightweight, downloads dependencies on first run)
make package-flatpack
./dist/portago-flatpack

# Option 2: Build the fully bundled binary (fully self-contained, extracts self on first run)
make package
./dist/portago
```

## Usage

```bash
# Open a file or directory
portago .
portago main.go

# Force re-extract and re-setup
portago --setup

# Use persistent storage instead of temp cache
portago --persist

# Remove all portago data
portago --clean

# Print version
portago --version
```

## How it works

### Bundled mode

The bundled binary contains a compressed tarball with nvim, the full config, plugins, Mason tools, and pre-compiled treesitter parsers. On first run, it extracts to a content-addressed temp directory (`/tmp/portago-<hash>/`). Subsequent runs detect the existing cache and start instantly.

The cache key is a SHA256 hash of the embedded bundle, so upgrading the binary automatically invalidates the old cache.

### Flatpack mode

The flatpack binary is lightweight (~6MB) and downloads everything on first run: Neovim from GitHub releases (with checksum verification), plugins via lazy.nvim, Mason tools (gopls, delve, etc.), and treesitter parsers. Everything goes into `~/.portago/`.

### State management

Mutable state (undo history, shada, swap files) is stored separately from the extraction cache so it survives binary upgrades and temp directory cleanup:

- **Temp cache mode:** state goes to `~/.local/state/portago/` (or `$XDG_STATE_HOME/portago/`)
- **Persist mode:** state lives inside `~/.portago/state/`

## Key bindings

`<Space>` is the leader key. Press it and wait for [which-key](https://github.com/folke/which-key.nvim) to show available options.

| Binding | Action |
|---------|--------|
| `<leader>sf` | Search files |
| `<leader>sg` | Search by grep |
| `<leader>e` | Toggle file explorer |
| `<leader>f` | Format buffer |
| `<leader>q` | Open diagnostic quickfix list |
| `<leader>td` | Trouble diagnostics |
| `<leader>b` | Toggle breakpoint |
| `<F5>` | Start/continue debugger |
| `<leader>rgt` | Run `go test ./...` |
| `<leader>rt` | Run test under cursor |
| `<leader>dt` | Debug test under cursor |
| `<leader>gga` | Add json struct tags |
| `<leader>ggi` | Add `if err != nil` |
| `grd` | Go to definition |
| `grr` | Go to references |
| `grn` | Rename symbol |

## Building for multiple platforms

```bash
# Cross-compile all 4 flatpack binaries (fast, ~10s)
make package-flatpack-all
# Produces: dist/portago-flatpack-{darwin,linux}-{arm64,amd64}

# Build the bundled binary for the current platform
make package
# Produces: dist/portago (+ dist/portago-flatpack)

# Test compatibility across Linux distributions
make test-platforms
```

The bundled binary contains native binaries (nvim, gopls, delve, treesitter parsers), so it must be built on each target platform. The flatpack binary has no CGO dependency and cross-compiles to any supported platform (macOS, Linux).

### Supported platforms

| Platform | Flatpack | Bundled |
|----------|----------|---------|
| macOS arm64 (Apple Silicon) | yes | yes |
| macOS amd64 (Intel) | yes | yes |
| Linux amd64 | yes | yes |
| Linux arm64 | yes | yes |

## Development

```bash
# Dev setup (uses system nvim, manages plugins locally)
make setup

# Launch via shell wrapper (for development)
make run

# Run tests
go test ./...

# Test on multiple Linux distros via Docker
make test-platforms
```

## License

This project is licensed under the GNU General Public License v3.0 — see [LICENSE](LICENSE) for details.
