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
	"syscall"
	"time"
)

//go:embed all:config
var configFS embed.FS

var version = "dev"

const nvimVersion = "0.11.1"

// SHA256 checksums for official Neovim v0.11.1 releases.
var nvimChecksums = map[string]string{
	"darwin-arm64": "89a766fb41303dc101766898ad3c4eb6db556e19965582cc164419605a1d1f61",
	"darwin-amd64": "485d20138bb4b41206dbcf23a2069ad9560c83e9313fb8073cb3dde5560782e3",
	"linux-arm64":  "6943991e601415db6eed765aeb98f8ba70a4d74859e4cf5e99ca7eb2a1b5d384",
	"linux-amd64":  "92ecb2dbdfbd0c6d79b522e07c879f7743c5d395d0a4f13b0d4f668f8565527a",
}

func main() {
	setup := flag.Bool("setup", false, "Force re-extract config and re-run setup")
	clean := flag.Bool("clean", false, "Remove ~/.portago and exit (fresh start)")
	showVersion := flag.Bool("version", false, "Print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("portago %s\n", version)
		return
	}

	portagoHome, err := portagoDir()
	if err != nil {
		fatal("cannot determine home directory: %v", err)
	}

	if *clean {
		fmt.Printf("==> Removing %s...\n", portagoHome)
		if err := os.RemoveAll(portagoHome); err != nil {
			fatal("removing %s: %v", portagoHome, err)
		}
		fmt.Println("==> Clean. Run portago again for a fresh setup.")
		return
	}

	// First-run detection: check for stamp file
	stampFile := filepath.Join(portagoHome, ".setup-done")
	needsSetup := *setup
	if _, err := os.Stat(stampFile); os.IsNotExist(err) {
		needsSetup = true
	}

	if needsSetup {
		if err := doSetup(portagoHome, stampFile); err != nil {
			fatal("%v", err)
		}
	}

	launchNvim(portagoHome, flag.Args())
}

func portagoDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".portago"), nil
}

// nvimBin returns the path to the nvim binary.
// Prefers the bundled copy at ~/.portago/nvim/bin/nvim,
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

func doSetup(portagoHome, stampFile string) error {
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

	env := buildEnv(portagoHome)

	// Install plugins
	fmt.Println("==> Installing plugins via lazy.nvim...")
	if err := runNvimHeadless(portagoHome, env, "+Lazy! sync", "+qa"); err != nil {
		return fmt.Errorf("installing plugins: %w", err)
	}

	// Install Mason tools
	fmt.Println("==> Installing Mason tools (gopls, delve, stylua, tree-sitter-cli)...")
	if err := runNvimHeadless(portagoHome, env, "+MasonToolsInstallSync", "+qa"); err != nil {
		return fmt.Errorf("installing mason tools: %w", err)
	}

	// Wait for tree-sitter-cli to be available (installed by Mason)
	tsCLI := filepath.Join(portagoHome, "data", "config", "mason", "bin", "tree-sitter")
	fmt.Println("==> Waiting for tree-sitter-cli...")
	for i := 0; i < 30; i++ {
		if info, err := os.Stat(tsCLI); err == nil && info.Mode()&0o111 != 0 {
			break
		}
		time.Sleep(time.Second)
	}

	// Install TreeSitter parsers
	fmt.Println("==> Installing TreeSitter parsers...")
	tsCmd := "lua require('nvim-treesitter').install({'bash','c','diff','go','html','lua','luadoc','markdown','markdown_inline','query','vim','vimdoc'})"
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

// nvimDownloadURL returns the download URL and expected checksum for the
// Neovim release matching the current OS and architecture.
func nvimDownloadURL() (url, checksum string, err error) {
	key := runtime.GOOS + "-" + runtime.GOARCH
	checksum, ok := nvimChecksums[key]
	if !ok {
		return "", "", fmt.Errorf("unsupported platform: %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	// Map Go naming to Neovim release naming
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

// downloadNvim downloads and extracts the Neovim release into portagoHome/nvim/.
func downloadNvim(portagoHome string) error {
	// Skip if already downloaded
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

	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("downloading nvim: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("downloading nvim: HTTP %d from %s", resp.StatusCode, url)
	}

	// Read entire body for checksum verification before extracting (~15MB)
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading nvim download: %w", err)
	}

	// Verify checksum
	hash := sha256.Sum256(body)
	got := hex.EncodeToString(hash[:])
	if got != expectedChecksum {
		return fmt.Errorf("nvim checksum mismatch: got %s, want %s", got, expectedChecksum)
	}
	fmt.Println("==> Checksum verified.")

	// Remove any partial previous extraction
	nvimDir := filepath.Join(portagoHome, "nvim")
	os.RemoveAll(nvimDir)

	// Extract tarball
	fmt.Println("==> Extracting Neovim...")
	if err := extractTarGz(body, portagoHome); err != nil {
		return fmt.Errorf("extracting nvim: %w", err)
	}

	return nil
}

// extractTarGz extracts a gzipped tarball, stripping the top-level directory
// and placing contents under destDir/nvim/.
func extractTarGz(data []byte, destDir string) error {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	nvimDir := filepath.Join(destDir, "nvim")

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		// Strip the top-level directory (e.g., "nvim-macos-arm64/")
		// so "nvim-macos-arm64/bin/nvim" becomes "nvim/bin/nvim"
		parts := strings.SplitN(header.Name, "/", 2)
		if len(parts) < 2 || parts[1] == "" {
			continue
		}
		relPath := parts[1]
		target := filepath.Join(nvimDir, relPath)

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
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
			f.Close()
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			os.Remove(target)
			if err := os.Symlink(header.Linkname, target); err != nil {
				return err
			}
		}
	}
	return nil
}

func extractConfig(destDir string) error {
	// Remove old config to ensure clean state
	os.RemoveAll(destDir)

	return fs.WalkDir(configFS, "config", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// Strip the "config" prefix from the embedded path
		relPath, _ := filepath.Rel("config", path)
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

func launchNvim(portagoHome string, args []string) {
	nvimPath, err := nvimBin(portagoHome)
	if err != nil {
		fatal("%v", err)
	}

	env := buildEnv(portagoHome)
	argv := append([]string{"nvim"}, args...)

	if err := syscall.Exec(nvimPath, argv, env); err != nil {
		fatal("exec nvim: %v", err)
	}
}

func buildEnv(portagoHome string) []string {
	overrides := map[string]string{
		"XDG_CONFIG_HOME": portagoHome,
		"XDG_DATA_HOME":   filepath.Join(portagoHome, "data"),
		"XDG_STATE_HOME":  filepath.Join(portagoHome, "state"),
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
