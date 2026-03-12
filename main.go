package main

import (
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

//go:embed all:config
var configFS embed.FS

var version = "dev"

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

	// Check nvim is available before doing anything
	if _, err := exec.LookPath("nvim"); err != nil {
		fatal("nvim not found in PATH. Please install Neovim >= 0.10")
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

func doSetup(portagoHome, stampFile string) error {
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
	if err := runNvimHeadless(env, "+Lazy! sync", "+qa"); err != nil {
		return fmt.Errorf("installing plugins: %w", err)
	}

	// Install Mason tools
	fmt.Println("==> Installing Mason tools (gopls, delve, stylua, tree-sitter-cli)...")
	if err := runNvimHeadless(env, "+MasonToolsInstallSync", "+qa"); err != nil {
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
	if err := runNvimHeadless(env, "+"+tsCmd, "+qa"); err != nil {
		return fmt.Errorf("installing treesitter parsers: %w", err)
	}

	// Write stamp file
	if err := os.WriteFile(stampFile, []byte(version+"\n"), 0o644); err != nil {
		return fmt.Errorf("writing stamp file: %w", err)
	}

	fmt.Println("==> Setup complete!")
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

func runNvimHeadless(env []string, args ...string) error {
	nvimPath, err := exec.LookPath("nvim")
	if err != nil {
		return fmt.Errorf("nvim not found: %w", err)
	}

	allArgs := append([]string{"--headless"}, args...)
	cmd := exec.Command(nvimPath, allArgs...)
	cmd.Env = env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func launchNvim(portagoHome string, args []string) {
	nvimPath, err := exec.LookPath("nvim")
	if err != nil {
		fatal("nvim not found: %v", err)
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
