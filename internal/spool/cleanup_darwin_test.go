package spool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestOpenReportsTemporaryCleanupFailureAndReleasesLock(t *testing.T) {
	for _, name := range []string{"results/.result-123.tmp", "quarantine/.quarantine-123.tmp"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			s, err := Open(dir, 2, 4096)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, name)
			if err := os.WriteFile(path, []byte("orphan"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := unix.Chflags(path, unix.UF_IMMUTABLE); err != nil {
				t.Fatalf("make cleanup failure fixture: %v", err)
			}
			defer unix.Chflags(path, 0)
			s, err = Open(dir, 2, 4096)
			if s != nil {
				s.Close()
			}
			if err == nil || !strings.Contains(err.Error(), "remove abandoned spool write") {
				t.Fatalf("Open suppressed cleanup failure: %v", err)
			}
			if got, err := os.ReadFile(path); err != nil || string(got) != "orphan" {
				t.Fatalf("orphan changed: %v", err)
			}
			if err := unix.Chflags(path, 0); err != nil {
				t.Fatal(err)
			}
			s, err = Open(dir, 2, 4096)
			if err != nil {
				t.Fatalf("retry after cleanup failure: %v", err)
			}
			defer s.Close()
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				t.Fatalf("retry did not remove orphan: %v", err)
			}
		})
	}
}
