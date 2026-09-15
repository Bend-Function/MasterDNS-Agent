//go:build !windows

package spool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenRejectsSymlinkedSpoolSubdirectory(t *testing.T) {
	for _, name := range []string{resultsDirName, quarantineDirName} {
		t.Run(name, func(t *testing.T) {
			dir, outside := t.TempDir(), t.TempDir()
			target := filepath.Join(outside, ".result-123.tmp")
			if err := os.WriteFile(target, []byte("outside"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(dir, name)); err != nil {
				t.Fatal(err)
			}
			s, err := Open(dir, 2, 4096)
			if s != nil {
				s.Close()
			}
			if err == nil {
				t.Error("Open followed a symlinked spool subdirectory")
			}
			if got, err := os.ReadFile(target); err != nil || string(got) != "outside" {
				t.Fatalf("external file changed: %v", err)
			}
			if err := os.Remove(filepath.Join(dir, name)); err != nil {
				t.Fatal(err)
			}
			s, err = Open(dir, 2, 4096)
			if err != nil {
				t.Fatalf("failed Open retained lock: %v", err)
			}
			s.Close()
		})
	}
}

func TestReopenDoesNotFollowTemporarySymlinksOrDirectories(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	s, err := Open(dir, 2, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(outside, "data")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"results/.result-123.tmp", "quarantine/.quarantine-123.tmp"} {
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(dir, strings.Replace(name, "123", "456", 1)), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	s, err = Open(dir, 2, 4096)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got, err := os.ReadFile(target); err != nil || string(got) != "keep" {
		t.Fatalf("external file changed: %v", err)
	}
	for _, name := range []string{"results/.result-123.tmp", "quarantine/.quarantine-123.tmp"} {
		if info, err := os.Lstat(filepath.Join(dir, name)); err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("symlink changed: %v", err)
		}
		if info, err := os.Lstat(filepath.Join(dir, strings.Replace(name, "123", "456", 1))); err != nil || !info.IsDir() {
			t.Fatalf("directory changed: %v", err)
		}
	}
}
