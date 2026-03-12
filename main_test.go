package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// syncOnceZero returns a zero-value sync.Once for resetting bundleHashOnce in tests.
func syncOnceZero() sync.Once { return sync.Once{} }

// createTarGz creates a gzipped tarball in memory from the given entries.
type tarEntry struct {
	Name     string
	Body     string
	Mode     int64
	TypeFlag byte
	Linkname string
}

func createTarGz(t *testing.T, entries []tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	for _, e := range entries {
		typeflag := e.TypeFlag
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}
		mode := e.Mode
		if mode == 0 {
			mode = 0o644
		}
		hdr := &tar.Header{
			Name:     e.Name,
			Mode:     mode,
			Size:     int64(len(e.Body)),
			Typeflag: typeflag,
			Linkname: e.Linkname,
		}
		if typeflag == tar.TypeDir {
			hdr.Size = 0
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("writing tar header for %s: %v", e.Name, err)
		}
		if typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte(e.Body)); err != nil {
				t.Fatalf("writing tar body for %s: %v", e.Name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("closing tar writer: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("closing gzip writer: %v", err)
	}
	return buf.Bytes()
}

func TestExtractTarGzToDir(t *testing.T) {
	data := createTarGz(t, []tarEntry{
		{Name: "dir/", TypeFlag: tar.TypeDir},
		{Name: "dir/file.txt", Body: "hello world", Mode: 0o644},
		{Name: "dir/exec.sh", Body: "#!/bin/sh\necho hi", Mode: 0o755},
		{Name: "dir/link", TypeFlag: tar.TypeSymlink, Linkname: "file.txt"},
	})

	dest := t.TempDir()
	if err := extractTarGzToDir(data, dest); err != nil {
		t.Fatalf("extractTarGzToDir: %v", err)
	}

	// Verify regular file
	content, err := os.ReadFile(filepath.Join(dest, "dir", "file.txt"))
	if err != nil {
		t.Fatalf("reading file.txt: %v", err)
	}
	if string(content) != "hello world" {
		t.Errorf("file.txt content = %q, want %q", content, "hello world")
	}

	// Verify executable permissions
	info, err := os.Stat(filepath.Join(dest, "dir", "exec.sh"))
	if err != nil {
		t.Fatalf("stat exec.sh: %v", err)
	}
	if info.Mode()&0o111 == 0 {
		t.Errorf("exec.sh should be executable, got mode %v", info.Mode())
	}

	// Verify symlink
	linkTarget, err := os.Readlink(filepath.Join(dest, "dir", "link"))
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if linkTarget != "file.txt" {
		t.Errorf("link target = %q, want %q", linkTarget, "file.txt")
	}
}

func TestExtractTarGzStripTopLevel(t *testing.T) {
	data := createTarGz(t, []tarEntry{
		{Name: "toplevel/", TypeFlag: tar.TypeDir},
		{Name: "toplevel/sub/", TypeFlag: tar.TypeDir},
		{Name: "toplevel/sub/file.txt", Body: "stripped"},
	})

	dest := t.TempDir()
	if err := extractTarGzStripTopLevel(data, dest); err != nil {
		t.Fatalf("extractTarGzStripTopLevel: %v", err)
	}

	// File should be at dest/sub/file.txt, not dest/toplevel/sub/file.txt
	content, err := os.ReadFile(filepath.Join(dest, "sub", "file.txt"))
	if err != nil {
		t.Fatalf("reading stripped file: %v", err)
	}
	if string(content) != "stripped" {
		t.Errorf("content = %q, want %q", content, "stripped")
	}

	// toplevel dir should not exist
	if _, err := os.Stat(filepath.Join(dest, "toplevel")); !os.IsNotExist(err) {
		t.Errorf("toplevel directory should not exist after stripping")
	}
}

func TestExtractTarGz_PathTraversal(t *testing.T) {
	data := createTarGz(t, []tarEntry{
		{Name: "../../../etc/evil", Body: "malicious"},
	})

	dest := t.TempDir()
	err := extractTarGzToDir(data, dest)
	if err == nil {
		t.Fatal("expected error for path traversal, got nil")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("escapes destination")) {
		t.Errorf("error should mention escaping destination, got: %v", err)
	}
}

func TestExtractTarGz_SymlinkPathTraversal(t *testing.T) {
	// Absolute symlink target
	data := createTarGz(t, []tarEntry{
		{Name: "evil-link", TypeFlag: tar.TypeSymlink, Linkname: "/etc/passwd"},
	})

	dest := t.TempDir()
	err := extractTarGzToDir(data, dest)
	if err == nil {
		t.Fatal("expected error for absolute symlink target, got nil")
	}

	// Relative symlink that escapes
	data2 := createTarGz(t, []tarEntry{
		{Name: "sub/", TypeFlag: tar.TypeDir},
		{Name: "sub/escape", TypeFlag: tar.TypeSymlink, Linkname: "../../etc/passwd"},
	})

	dest2 := t.TempDir()
	err = extractTarGzToDir(data2, dest2)
	if err == nil {
		t.Fatal("expected error for escaping symlink target, got nil")
	}
}

func TestExtractTarGz_StripTopLevel_PathTraversal(t *testing.T) {
	data := createTarGz(t, []tarEntry{
		{Name: "top/../../etc/evil", Body: "malicious"},
	})

	dest := t.TempDir()
	err := extractTarGzStripTopLevel(data, dest)
	if err == nil {
		t.Fatal("expected error for path traversal in strip mode, got nil")
	}
}

func TestExtractTarGz_CorruptedData(t *testing.T) {
	err := extractTarGzToDir([]byte("not valid gzip"), t.TempDir())
	if err == nil {
		t.Fatal("expected error for corrupted gzip data, got nil")
	}
}

func TestExtractTarGz_EmptyArchive(t *testing.T) {
	data := createTarGz(t, nil)
	dest := t.TempDir()
	if err := extractTarGzToDir(data, dest); err != nil {
		t.Fatalf("empty archive should extract successfully, got: %v", err)
	}
}

func TestFixMasonWrapperScripts(t *testing.T) {
	dir := t.TempDir()
	masonBin := filepath.Join(dir, "data", "config", "mason", "bin")
	if err := os.MkdirAll(masonBin, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write a script with the placeholder
	script := "#!/bin/bash\nexec PORTAGO_HOME_PLACEHOLDER/bin/gopls \"$@\"\n"
	if err := os.WriteFile(filepath.Join(masonBin, "gopls"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	// Write a script without the placeholder (should be left alone)
	other := "#!/bin/bash\necho hello\n"
	if err := os.WriteFile(filepath.Join(masonBin, "other"), []byte(other), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := fixMasonWrapperScripts(dir); err != nil {
		t.Fatalf("fixMasonWrapperScripts: %v", err)
	}

	// Check placeholder was replaced
	got, _ := os.ReadFile(filepath.Join(masonBin, "gopls"))
	expected := "#!/bin/bash\nexec " + dir + "/bin/gopls \"$@\"\n"
	if string(got) != expected {
		t.Errorf("gopls script = %q, want %q", got, expected)
	}

	// Check other script was not modified
	got2, _ := os.ReadFile(filepath.Join(masonBin, "other"))
	if string(got2) != other {
		t.Errorf("other script was modified: %q", got2)
	}
}

func TestFixMasonWrapperScripts_NoDir(t *testing.T) {
	// Non-existent mason dir should not error
	if err := fixMasonWrapperScripts(t.TempDir()); err != nil {
		t.Fatalf("expected no error for missing mason dir, got: %v", err)
	}
}

func TestBuildEnv(t *testing.T) {
	env := buildEnv("/fake/home", "/fake/state")

	lookup := make(map[string]string)
	for _, e := range env {
		k, v, _ := cut(e, "=")
		lookup[k] = v
	}

	checks := map[string]string{
		"XDG_CONFIG_HOME": "/fake/home",
		"XDG_DATA_HOME":   "/fake/home/data",
		"XDG_STATE_HOME":  "/fake/state",
		"XDG_CACHE_HOME":  "/fake/home/cache",
		"NVIM_APPNAME":    "config",
	}
	for k, want := range checks {
		if got := lookup[k]; got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}

func cut(s, sep string) (string, string, bool) {
	i := bytes.Index([]byte(s), []byte(sep))
	if i < 0 {
		return s, "", false
	}
	return s[:i], s[i+len(sep):], true
}

func TestIsBundled(t *testing.T) {
	orig := bundleData
	defer func() { bundleData = orig }()

	bundleData = nil
	if isBundled() {
		t.Error("nil bundleData should not be bundled")
	}

	bundleData = make([]byte, 20)
	if isBundled() {
		t.Error("20-byte bundleData should not be bundled")
	}

	bundleData = make([]byte, 1025)
	if !isBundled() {
		t.Error("1025-byte bundleData should be bundled")
	}
}

func TestBundleHash(t *testing.T) {
	orig := bundleData
	defer func() { bundleData = orig }()

	bundleData = []byte("test data for hashing")
	// Reset the sync.Once so we get a fresh hash
	bundleHashOnce = syncOnceZero()
	h := bundleHash()

	if len(h) != 12 {
		t.Errorf("bundleHash length = %d, want 12", len(h))
	}
	if !isHexString(h) {
		t.Errorf("bundleHash %q is not valid hex", h)
	}

	// Same input should produce same output
	bundleHashOnce = syncOnceZero()
	h2 := bundleHash()
	if h != h2 {
		t.Errorf("bundleHash not deterministic: %q != %q", h, h2)
	}
}

func TestIsHexString(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"0123456789abcdef", true},
		{"abc123", true},
		{"", false},
		{"xyz", false},
		{"ABC", false}, // uppercase not valid
		{"12 34", false},
	}
	for _, tt := range tests {
		if got := isHexString(tt.input); got != tt.want {
			t.Errorf("isHexString(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestValidatePathWithinDir(t *testing.T) {
	tests := []struct {
		target  string
		destDir string
		wantErr bool
	}{
		{"/tmp/dest/file.txt", "/tmp/dest", false},
		{"/tmp/dest/sub/file.txt", "/tmp/dest", false},
		{"/tmp/dest", "/tmp/dest", false},
		{"/tmp/dest/../etc/passwd", "/tmp/dest", true},
		{"/tmp/other/file.txt", "/tmp/dest", true},
		{"/tmp/destextra/file.txt", "/tmp/dest", true},
	}
	for _, tt := range tests {
		err := validatePathWithinDir(tt.target, tt.destDir)
		if (err != nil) != tt.wantErr {
			t.Errorf("validatePathWithinDir(%q, %q) error = %v, wantErr %v", tt.target, tt.destDir, err, tt.wantErr)
		}
	}
}
