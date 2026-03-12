package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
)

//go:embed all:config
var configFS embed.FS

//go:embed bundle.tar.gz
var bundleData []byte

var version = "dev"

const nvimVersion = "0.11.1"

// SHA256 checksums for official Neovim v0.11.1 releases (used in unbundled mode).
var nvimChecksums = map[string]string{
	"darwin-arm64": "89a766fb41303dc101766898ad3c4eb6db556e19965582cc164419605a1d1f61",
	"darwin-amd64": "485d20138bb4b41206dbcf23a2069ad9560c83e9313fb8073cb3dde5560782e3",
	"linux-arm64":  "6943991e601415db6eed765aeb98f8ba70a4d74859e4cf5e99ca7eb2a1b5d384",
	"linux-amd64":  "92ecb2dbdfbd0c6d79b522e07c879f7743c5d395d0a4f13b0d4f668f8565527a",
}

// isBundled returns true if the embedded bundle contains actual data
// (i.e., this binary was built with a full bundle via scripts/package.sh).
func isBundled() bool {
	// An empty tar.gz created by `tar czf ... --files-from /dev/null` is ~20 bytes.
	// A real bundle will be many megabytes.
	return len(bundleData) > 1024
}

// bundleHash returns the first 12 hex characters of the SHA256 of the embedded bundle.
// Used as a content-addressed cache key for the temp extraction directory.
var (
	bundleHashOnce sync.Once
	bundleHashVal  string
)

func bundleHash() string {
	bundleHashOnce.Do(func() {
		h := sha256.Sum256(bundleData)
		bundleHashVal = hex.EncodeToString(h[:6])
	})
	return bundleHashVal
}

func main() {
	setup := flag.Bool("setup", false, "Force re-extract and re-run setup")
	clean := flag.Bool("clean", false, "Remove all portago data and exit")
	showVersion := flag.Bool("version", false, "Print version and exit")
	persist := flag.Bool("persist", false, "Use ~/.portago instead of temp cache")
	flag.Parse()

	if *showVersion {
		fmt.Printf("portago %s\n", version)
		if isBundled() {
			fmt.Printf("  (fully bundled, hash %s)\n", bundleHash())
		} else {
			fmt.Println("  (downloads dependencies on first run)")
		}
		return
	}

	if *clean {
		doClean()
		return
	}

	portagoHome, usingTempCache, err := portagoDir(*persist)
	if err != nil {
		fatal("cannot determine directories: %v", err)
	}

	// Mutable state (undo, shada, swap) lives inside portagoHome when using
	// ~/.portago, or in a separate persistent dir when using the temp cache.
	stateDir := filepath.Join(portagoHome, "state")
	if usingTempCache {
		stateDir, err = persistentStateDir()
		if err != nil {
			fatal("cannot determine state directory: %v", err)
		}
	}

	// First-run detection: check for stamp file
	stampFile := filepath.Join(portagoHome, ".setup-done")
	needsSetup := *setup
	if _, err := os.Stat(stampFile); os.IsNotExist(err) {
		needsSetup = true
	}

	if needsSetup {
		if isBundled() {
			if err := doSetupBundled(portagoHome, stateDir, stampFile); err != nil {
				fatal("%v", err)
			}
		} else {
			if err := doSetupOnline(portagoHome, stateDir, stampFile); err != nil {
				fatal("%v", err)
			}
		}
		if *setup {
			return
		}
	}

	launchNvim(portagoHome, stateDir, flag.Args())
}

// portagoDir returns the base directory for portago's extracted data and whether
// it uses a temp cache. Priority: --persist flag or unbundled mode → ~/.portago;
// bundled mode checks for existing ~/.portago/.setup-done, otherwise temp cache.
func portagoDir(persist bool) (dir string, isTemp bool, err error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false, err
	}
	persistDir := filepath.Join(home, ".portago")

	if persist || !isBundled() {
		return persistDir, false, nil
	}

	// Check if ~/.portago exists from a previous --persist run
	if _, err := os.Stat(filepath.Join(persistDir, ".setup-done")); err == nil {
		return persistDir, false, nil
	}

	return filepath.Join(os.TempDir(), "portago-"+bundleHash()), true, nil
}

// persistentStateDir returns a directory for mutable nvim state (undo, shada,
// swap) that survives temp cache rebuilds and reboots.
// Respects $XDG_STATE_HOME if set (must be absolute per XDG spec),
// otherwise defaults to ~/.local/state/portago.
func persistentStateDir() (string, error) {
	if xdgState := os.Getenv("XDG_STATE_HOME"); xdgState != "" {
		if !filepath.IsAbs(xdgState) {
			return "", fmt.Errorf("$XDG_STATE_HOME must be an absolute path, got: %s", xdgState)
		}
		return filepath.Join(xdgState, "portago"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "portago"), nil
}

// doClean removes all portago-related directories.
func doClean() {
	var dirs []string

	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "portago: warning: cannot determine home directory: %v\n", err)
		fmt.Fprintf(os.Stderr, "portago: only cleaning temp directories\n")
	} else {
		dirs = append(dirs, filepath.Join(home, ".portago"))
	}

	// Clean the persistent state dir (respects $XDG_STATE_HOME)
	if stateDir, err := persistentStateDir(); err == nil {
		dirs = append(dirs, stateDir)
	}

	// Temp cache directories: /tmp/portago-<12-hex-char-hash>
	if matches, err := filepath.Glob(filepath.Join(os.TempDir(), "portago-*")); err == nil {
		for _, m := range matches {
			suffix := strings.TrimPrefix(filepath.Base(m), "portago-")
			if len(suffix) == 12 && isHexString(suffix) {
				dirs = append(dirs, m)
			}
		}
	}

	removed := 0
	failed := 0
	for _, d := range dirs {
		if _, err := os.Stat(d); err == nil {
			fmt.Printf("==> Removing %s\n", d)
			if err := os.RemoveAll(d); err != nil {
				fmt.Fprintf(os.Stderr, "    warning: %v\n", err)
				failed++
			} else {
				removed++
			}
		}
	}

	if removed == 0 && failed == 0 {
		fmt.Println("==> Nothing to clean.")
	} else if failed > 0 {
		fmt.Fprintf(os.Stderr, "==> Clean finished with %d error(s).\n", failed)
	} else {
		fmt.Println("==> Clean complete.")
	}
}

func isHexString(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return len(s) > 0
}

// nvimBin returns the path to the nvim binary.
// Prefers the bundled copy at <portagoHome>/nvim/bin/nvim,
// falls back to system nvim via PATH lookup.
func nvimBin(portagoHome string) (string, error) {
	bundled := filepath.Join(portagoHome, "nvim", "bin", "nvim")
	if _, err := os.Stat(bundled); err == nil {
		return bundled, nil
	}
	if sysPath, err := exec.LookPath("nvim"); err == nil {
		return sysPath, nil
	}
	return "", fmt.Errorf("nvim not found: no bundled nvim at %s and no system nvim in PATH", bundled)
}

// ---------------------------------------------------------------------------
// Bundled setup: extract the embedded tar.gz containing nvim, config,
// plugins, Mason tools, and TreeSitter parsers. No internet needed.
// ---------------------------------------------------------------------------

func doSetupBundled(portagoHome, stateDir, stampFile string) error {
	fmt.Println("==> Extracting bundled portago (fully offline)...")

	// Create base directories
	for _, dir := range []string{filepath.Join(portagoHome, "cache"), stateDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("creating %s: %w", dir, err)
		}
	}

	// Extract the full bundle (nvim/, config/, data/) into portagoHome
	if err := extractTarGzToDir(bundleData, portagoHome); err != nil {
		return fmt.Errorf("extracting bundle: %w", err)
	}

	// Fix Mason wrapper scripts that contain a placeholder path from packaging
	if err := fixMasonWrapperScripts(portagoHome); err != nil {
		return fmt.Errorf("fixing mason wrapper scripts: %w", err)
	}

	// Write stamp file
	if err := os.WriteFile(stampFile, []byte(version+"\n"), 0o644); err != nil {
		return fmt.Errorf("writing stamp file: %w", err)
	}

	fmt.Println("==> Setup complete! (no downloads needed)")
	return nil
}

// ---------------------------------------------------------------------------
// Online setup: download nvim + install plugins/tools via headless nvim.
// Used when the binary was built without a bundle (e.g., via make package-flatpack).
// ---------------------------------------------------------------------------

func doSetupOnline(portagoHome, stateDir, stampFile string) error {
	// Download Neovim if not already present
	if err := downloadNvim(portagoHome); err != nil {
		return fmt.Errorf("downloading nvim: %w", err)
	}

	// Create runtime directories
	for _, sub := range []string{"data", "state", "cache"} {
		if err := os.MkdirAll(filepath.Join(portagoHome, sub), 0o755); err != nil {
			return fmt.Errorf("creating %s: %w", sub, err)
		}
	}

	// Extract embedded config
	fmt.Println("==> Extracting portago config...")
	configDir := filepath.Join(portagoHome, "config")
	if err := extractConfig(configDir); err != nil {
		return fmt.Errorf("extracting config: %w", err)
	}

	env := buildEnv(portagoHome, stateDir)

	// Install plugins
	fmt.Println("==> Installing plugins via lazy.nvim...")
	if err := runNvimHeadless(portagoHome, env, "+Lazy! sync", "+qa"); err != nil {
		return fmt.Errorf("installing plugins: %w", err)
	}

	// Install Mason tools
	fmt.Println("==> Installing Mason tools (gopls, delve, gomodifytags, impl)...")
	if err := runNvimHeadless(portagoHome, env, "+MasonToolsInstallSync", "+qa"); err != nil {
		return fmt.Errorf("installing mason tools: %w", err)
	}

	// Install tree-sitter-cli separately (only needed for online setup to compile parsers,
	// not included in ensure_installed since bundled mode ships pre-compiled parsers)
	fmt.Println("==> Installing tree-sitter-cli (for parser compilation)...")
	if err := runNvimHeadless(portagoHome, env, "+MasonInstall tree-sitter-cli", "+qa"); err != nil {
		return fmt.Errorf("installing tree-sitter-cli: %w", err)
	}

	// Wait for tree-sitter-cli to be available
	tsCLI := filepath.Join(portagoHome, "data", "config", "mason", "bin", "tree-sitter")
	fmt.Println("==> Waiting for tree-sitter-cli...")
	tsFound := false
	for i := 0; i < 30; i++ {
		if info, err := os.Stat(tsCLI); err == nil && info.Mode()&0o111 != 0 {
			tsFound = true
			break
		}
		time.Sleep(time.Second)
	}
	if !tsFound {
		return fmt.Errorf("tree-sitter-cli not found at %s after 30s; Mason installation may have failed", tsCLI)
	}

	// Install TreeSitter parsers
	fmt.Println("==> Installing TreeSitter parsers...")
	tsCmd := "lua require('nvim-treesitter').install({'bash','diff','go','lua','luadoc','markdown','markdown_inline','query','vim','vimdoc'})"
	if err := runNvimHeadless(portagoHome, env, "+"+tsCmd, "+qa"); err != nil {
		return fmt.Errorf("installing treesitter parsers: %w", err)
	}

	// Write stamp file
	if err := os.WriteFile(stampFile, []byte(version+"\n"), 0o644); err != nil {
		return fmt.Errorf("writing stamp file: %w", err)
	}

	fmt.Println("==> Setup complete!")
	return nil
}

// ---------------------------------------------------------------------------
// Neovim download (online mode only)
// ---------------------------------------------------------------------------

func nvimDownloadURL() (url, checksum string, err error) {
	key := runtime.GOOS + "-" + runtime.GOARCH
	checksum, ok := nvimChecksums[key]
	if !ok {
		return "", "", fmt.Errorf("unsupported platform: %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	osName := runtime.GOOS
	archName := runtime.GOARCH
	if osName == "darwin" {
		osName = "macos"
	}
	if archName == "amd64" {
		archName = "x86_64"
	}

	filename := fmt.Sprintf("nvim-%s-%s.tar.gz", osName, archName)
	url = fmt.Sprintf("https://github.com/neovim/neovim/releases/download/v%s/%s", nvimVersion, filename)
	return url, checksum, nil
}

func downloadNvim(portagoHome string) error {
	nvimBinary := filepath.Join(portagoHome, "nvim", "bin", "nvim")
	if _, err := os.Stat(nvimBinary); err == nil {
		fmt.Println("==> Neovim already present, skipping download.")
		return nil
	}

	url, expectedChecksum, err := nvimDownloadURL()
	if err != nil {
		return err
	}

	fmt.Printf("==> Downloading Neovim v%s for %s/%s...\n", nvimVersion, runtime.GOOS, runtime.GOARCH)

	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("downloading nvim: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("downloading nvim: HTTP %d from %s", resp.StatusCode, url)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading nvim download: %w", err)
	}

	hash := sha256.Sum256(body)
	got := hex.EncodeToString(hash[:])
	if got != expectedChecksum {
		return fmt.Errorf("nvim checksum mismatch: got %s, want %s", got, expectedChecksum)
	}
	fmt.Println("==> Checksum verified.")

	nvimDir := filepath.Join(portagoHome, "nvim")
	if err := os.RemoveAll(nvimDir); err != nil {
		return fmt.Errorf("removing old nvim directory %s: %w", nvimDir, err)
	}

	fmt.Println("==> Extracting Neovim...")
	if err := extractTarGzStripTopLevel(body, nvimDir); err != nil {
		return fmt.Errorf("extracting nvim: %w", err)
	}

	return nil
}

// ---------------------------------------------------------------------------
// Tar/Gzip extraction helpers
// ---------------------------------------------------------------------------

// extractTarGzToDir extracts a gzipped tarball directly into destDir,
// preserving the paths as-is (no stripping).
func extractTarGzToDir(data []byte, destDir string) error {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		target := filepath.Join(destDir, header.Name)

		if err := extractTarEntry(tr, header, target, destDir); err != nil {
			return err
		}
	}
	return nil
}

// extractTarGzStripTopLevel extracts a gzipped tarball, stripping the
// top-level directory, placing contents directly under destDir.
func extractTarGzStripTopLevel(data []byte, destDir string) error {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		// Strip the top-level directory (e.g., "nvim-macos-arm64/bin/nvim" -> "bin/nvim")
		parts := strings.SplitN(header.Name, "/", 2)
		if len(parts) < 2 || parts[1] == "" {
			continue
		}
		target := filepath.Join(destDir, parts[1])

		if err := extractTarEntry(tr, header, target, destDir); err != nil {
			return err
		}
	}
	return nil
}

// validatePathWithinDir ensures target stays within destDir (Zip Slip protection).
func validatePathWithinDir(target, destDir string) error {
	cleanTarget := filepath.Clean(target)
	cleanDest := filepath.Clean(destDir)
	if cleanTarget != cleanDest && !strings.HasPrefix(cleanTarget, cleanDest+string(os.PathSeparator)) {
		return fmt.Errorf("tar entry escapes destination directory: %s", cleanTarget)
	}
	return nil
}

func extractTarEntry(tr *tar.Reader, header *tar.Header, target, destDir string) error {
	if err := validatePathWithinDir(target, destDir); err != nil {
		return err
	}

	switch header.Typeflag {
	case tar.TypeDir:
		return os.MkdirAll(target, 0o755)
	case tar.TypeReg:
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(header.Mode))
		if err != nil {
			return err
		}
		if _, err := io.Copy(f, tr); err != nil {
			f.Close()
			return err
		}
		return f.Close()
	case tar.TypeSymlink:
		// Validate symlink target stays within destDir
		if filepath.IsAbs(header.Linkname) {
			return fmt.Errorf("tar symlink %q has absolute target %q", header.Name, header.Linkname)
		}
		resolved := filepath.Join(filepath.Dir(target), header.Linkname)
		if err := validatePathWithinDir(resolved, destDir); err != nil {
			return fmt.Errorf("tar symlink %q -> %q: %w", header.Name, header.Linkname, err)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("removing existing file before symlink %s: %w", target, err)
		}
		return os.Symlink(header.Linkname, target)
	default:
		fmt.Fprintf(os.Stderr, "portago: warning: skipping unsupported tar entry type %d for %s\n", header.Typeflag, header.Name)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Config extraction (online mode — extracts config/ from the go:embed configFS,
// not from the bundle tarball)
// ---------------------------------------------------------------------------

func extractConfig(destDir string) error {
	if err := os.RemoveAll(destDir); err != nil {
		return fmt.Errorf("removing old config directory %s: %w", destDir, err)
	}

	return fs.WalkDir(configFS, "config", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel("config", path)
		if err != nil {
			return fmt.Errorf("computing relative path for %s: %w", path, err)
		}
		target := filepath.Join(destDir, relPath)

		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}

		data, err := configFS.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}

// fixMasonWrapperScripts replaces the packaging placeholder in Mason wrapper
// scripts with the actual portagoHome path on this machine.
func fixMasonWrapperScripts(portagoHome string) error {
	masonBin := filepath.Join(portagoHome, "data", "config", "mason", "bin")
	entries, err := os.ReadDir(masonBin)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // No mason bin dir is acceptable
		}
		return fmt.Errorf("reading mason bin dir %s: %w", masonBin, err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		path := filepath.Join(masonBin, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading mason wrapper %s: %w", path, err)
		}
		if bytes.Contains(data, []byte("PORTAGO_HOME_PLACEHOLDER")) {
			fixed := bytes.ReplaceAll(data, []byte("PORTAGO_HOME_PLACEHOLDER"), []byte(portagoHome))
			if err := os.WriteFile(path, fixed, 0o755); err != nil {
				return fmt.Errorf("writing fixed mason wrapper %s: %w", path, err)
			}
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Nvim execution
// ---------------------------------------------------------------------------

func runNvimHeadless(portagoHome string, env []string, args ...string) error {
	nvimPath, err := nvimBin(portagoHome)
	if err != nil {
		return err
	}

	allArgs := append([]string{"--headless"}, args...)
	cmd := exec.Command(nvimPath, allArgs...)
	cmd.Env = env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func launchNvim(portagoHome, stateDir string, args []string) {
	nvimPath, err := nvimBin(portagoHome)
	if err != nil {
		fatal("%v", err)
	}

	env := buildEnv(portagoHome, stateDir)
	argv := append([]string{"nvim"}, args...)

	if err := syscall.Exec(nvimPath, argv, env); err != nil {
		fatal("exec nvim: %v", err)
	}
}

func buildEnv(portagoHome, stateDir string) []string {
	overrides := map[string]string{
		"XDG_CONFIG_HOME": portagoHome,
		"XDG_DATA_HOME":   filepath.Join(portagoHome, "data"),
		"XDG_STATE_HOME":  stateDir,
		"XDG_CACHE_HOME":  filepath.Join(portagoHome, "cache"),
		"NVIM_APPNAME":    "config",
	}

	var env []string
	for _, e := range os.Environ() {
		key, _, _ := strings.Cut(e, "=")
		if _, override := overrides[key]; !override {
			env = append(env, e)
		}
	}
	for k, v := range overrides {
		env = append(env, k+"="+v)
	}
	return env
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "portago: "+format+"\n", args...)
	os.Exit(1)
}
