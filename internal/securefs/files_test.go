package securefs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadFileRejectsSymlinkOutsideParent(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile(link); err == nil {
		t.Fatal("symlink escaping the configured directory was accepted")
	}
}

func TestWriteFileKeepsOwnerOnlyMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proxy.json")
	if err := WriteFile(path, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("mode = %o, want 600", got)
	}
}

func TestWriteFileExclusiveRejectsExistingPathAndSymlink(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "existing")
	if err := os.WriteFile(existing, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileExclusive(existing, []byte("replace"), 0600); err == nil {
		t.Fatal("exclusive write replaced an existing file")
	}
	content, _ := os.ReadFile(existing)
	if string(content) != "keep" {
		t.Fatal("existing content changed")
	}

	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileExclusive(link, []byte("replace"), 0600); err == nil {
		t.Fatal("exclusive write followed a symlink")
	}
}
