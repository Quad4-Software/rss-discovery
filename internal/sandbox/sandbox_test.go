package sandbox

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestSplitDirsFiles(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "real.conf")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.conf")
	if err := os.Symlink(target, link); err != nil {
		t.Skip("symlinks unsupported")
	}
	missing := filepath.Join(dir, "nope")

	dirs, files := splitDirsFiles([]string{dir, file, link, missing})
	if !slices.Contains(dirs, dir) {
		t.Fatalf("dir not in dirs: %v", dirs)
	}
	for _, want := range []string{file, link, target} {
		if !slices.Contains(files, want) {
			t.Fatalf("%s not in files: %v", want, files)
		}
	}
	if slices.Contains(files, missing) || slices.Contains(dirs, missing) {
		t.Fatalf("missing path classified: %v %v", dirs, files)
	}
}

func TestListenPort(t *testing.T) {
	for addr, want := range map[string]uint16{
		"0.0.0.0:8787": 8787,
		":8788":        8788,
		"[::1]:9090":   9090,
	} {
		p, err := listenPort(addr)
		if err != nil || p != want {
			t.Fatalf("listenPort(%q) = %d, %v; want %d", addr, p, err, want)
		}
	}
	if _, err := listenPort("noport"); err == nil {
		t.Fatal("expected error for missing port")
	}
}
