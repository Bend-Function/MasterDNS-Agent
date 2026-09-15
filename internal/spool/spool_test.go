package spool

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Bend-Function/MasterDNS-Agent/internal/protocol"
)

func TestResultSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	want := testResult("11111111-1111-4111-8111-111111111111")
	s, err := Open(dir, 2, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put(want); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(dir, 2, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.Batch(100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Fatalf("reopened batch = %#v, want %#v", got, want)
	}
}

func TestOpenExclusivelyOwnsDirectoryUntilClose(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, 2, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir, 2, 1<<20); !errors.Is(err, ErrInUse) {
		t.Fatalf("second Open error = %v, want ErrInUse", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(dir, 2, 1<<20)
	if err != nil {
		t.Fatalf("Open after Close: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestDirectoryLockIsReleasedWhenProcessExits(t *testing.T) {
	if os.Getenv("MASTERDNS_SPOOL_LOCK_HELPER") == "1" {
		s, err := Open(os.Getenv("MASTERDNS_SPOOL_LOCK_DIR"), 2, 1<<20)
		if err != nil {
			os.Exit(2)
		}
		_ = s
		os.Exit(0)
	}
	dir := t.TempDir()
	command := exec.Command(os.Args[0], "-test.run=TestDirectoryLockIsReleasedWhenProcessExits")
	command.Env = append(os.Environ(), "MASTERDNS_SPOOL_LOCK_HELPER=1", "MASTERDNS_SPOOL_LOCK_DIR="+dir)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("lock helper: %v: %s", err, output)
	}
	s, err := Open(dir, 2, 1<<20)
	if err != nil {
		t.Fatalf("Open after process exit: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestDuplicateKeepsFirstResult(t *testing.T) {
	s, err := Open(t.TempDir(), 2, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	first := testResult("11111111-1111-4111-8111-111111111111")
	second := first
	second.LeaseID = "55555555-5555-4555-8555-555555555555"
	second.Outcome = protocol.OutcomeFailure
	if err := s.Put(first); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(second); err != nil {
		t.Fatal(err)
	}
	batch, err := s.Batch(100)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch) != 1 || !reflect.DeepEqual(batch[0], first) {
		t.Fatalf("batch = %#v, want first result %#v", batch, first)
	}
}

func TestPutUsesAtomicResultFile(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, 2, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	result := testResult("11111111-1111-4111-8111-111111111111")
	if err := s.Put(result); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(filepath.Join(dir, resultsDirName))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != result.TaskID+resultFileSuffix {
		t.Fatalf("result directory entries = %#v", entryNames(entries))
	}
	info, err := entries[0].Info()
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("result mode = %o, want 600", info.Mode().Perm())
	}
}

func TestCrashLeftTemporaryFileIsIgnored(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, 2, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, resultsDirName, ".interrupted.tmp"), []byte(`{"partial":`), 0o600); err != nil {
		t.Fatal(err)
	}
	batch, err := s.Batch(100)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch) != 0 {
		t.Fatalf("batch contains temporary file: %#v", batch)
	}
}

func TestFullSpoolRejectsWithoutDeletingExistingResult(t *testing.T) {
	s, err := Open(t.TempDir(), 1, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	first := testResult("11111111-1111-4111-8111-111111111111")
	if err := s.Put(first); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(testResult("22222222-2222-4222-8222-222222222222")); !errors.Is(err, ErrFull) {
		t.Fatalf("second Put error = %v, want ErrFull", err)
	}
	batch, err := s.Batch(100)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch) != 1 || batch[0].TaskID != first.TaskID {
		t.Fatalf("batch = %#v", batch)
	}
}

func TestByteCapacityRejectsOversizedResult(t *testing.T) {
	result := testResult("11111111-1111-4111-8111-111111111111")
	payload, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open(t.TempDir(), 10, int64(len(payload)))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put(result); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(testResult("22222222-2222-4222-8222-222222222222")); !errors.Is(err, ErrFull) {
		t.Fatalf("Put error = %v, want ErrFull", err)
	}
}

func TestMalformedFileIsQuarantinedWithoutPoisoningBatch(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, 10, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	valid := testResult("11111111-1111-4111-8111-111111111111")
	if err := s.Put(valid); err != nil {
		t.Fatal(err)
	}
	badName := "22222222-2222-4222-8222-222222222222" + resultFileSuffix
	if err := os.WriteFile(filepath.Join(dir, resultsDirName, badName), []byte(`{"secret-corrupt-marker"`), 0o600); err != nil {
		t.Fatal(err)
	}

	batch, err := s.Batch(100)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch) != 1 || batch[0].TaskID != valid.TaskID {
		t.Fatalf("batch = %#v", batch)
	}
	if _, err := os.Stat(filepath.Join(dir, resultsDirName, badName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("malformed result remains in upload directory: %v", err)
	}
	quarantined, err := os.ReadDir(filepath.Join(dir, quarantineDirName))
	if err != nil {
		t.Fatal(err)
	}
	if len(quarantined) != 1 {
		t.Fatalf("quarantine entries = %d, want reason metadata", len(quarantined))
	}
	for _, entry := range quarantined {
		if !strings.HasSuffix(entry.Name(), quarantineMetadataSuffix) {
			continue
		}
		payload, err := os.ReadFile(filepath.Join(dir, quarantineDirName, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var metadata quarantineMetadata
		if err := json.Unmarshal(payload, &metadata); err != nil {
			t.Fatal(err)
		}
		if metadata.ReasonCode != "malformed_json" {
			t.Fatalf("reason code = %q", metadata.ReasonCode)
		}
		if strings.Contains(string(payload), "secret-corrupt-marker") {
			t.Fatal("quarantine metadata contains corrupt payload")
		}
	}
}

func TestNearNameLimitMalformedFileDoesNotPoisonValidBatch(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, 10, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	badName := strings.Repeat("0", 250) + resultFileSuffix
	if err := os.WriteFile(filepath.Join(dir, resultsDirName, badName), []byte(`{`), 0o600); err != nil {
		t.Fatal(err)
	}
	valid := testResult("11111111-1111-4111-8111-111111111111")
	if err := s.Put(valid); err != nil {
		t.Fatal(err)
	}
	batch, err := s.Batch(100)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch) != 1 || batch[0].TaskID != valid.TaskID {
		t.Fatalf("batch = %#v", batch)
	}
	entries, err := os.ReadDir(filepath.Join(dir, quarantineDirName))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || len(entries[0].Name()) > 64 {
		t.Fatalf("quarantine entries = %v", entryNames(entries))
	}
}

func TestDeleteFailurePreservesReasonAndDoesNotPoisonValidBatch(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, 10, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	badName := "00000000-0000-0000-0000-000000000000" + resultFileSuffix
	badPath := filepath.Join(dir, resultsDirName, badName)
	if err := os.WriteFile(badPath, []byte(`{`), 0o600); err != nil {
		t.Fatal(err)
	}
	valid := testResult("11111111-1111-4111-8111-111111111111")
	if err := s.Put(valid); err != nil {
		t.Fatal(err)
	}
	removeCalls := 0
	s.remove = func(path string) error {
		if path == badPath && removeCalls == 0 {
			removeCalls++
			return errors.New("injected delete failure")
		}
		return os.Remove(path)
	}
	batch, err := s.Batch(100)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch) != 1 || batch[0].TaskID != valid.TaskID {
		t.Fatalf("batch after quarantine delete failure = %#v", batch)
	}
	if _, err := os.Stat(badPath); err != nil {
		t.Fatalf("malformed source not preserved: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(dir, quarantineDirName))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("reason metadata entries = %v", entryNames(entries))
	}
	if _, err := s.Batch(100); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(badPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("malformed source remains after retry: %v", err)
	}
}

func TestRejectQuarantinesResultWithPayloadFreeReason(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, 2, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	result := testResult("11111111-1111-4111-8111-111111111111")
	result.ErrorCode = "secret-result-marker"
	if err := s.Put(result); err != nil {
		t.Fatal(err)
	}
	if err := s.Reject(result.TaskID, "lease_rejected"); err != nil {
		t.Fatal(err)
	}
	batch, err := s.Batch(100)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch) != 0 {
		t.Fatalf("rejected result remains uploadable: %#v", batch)
	}
	entries, err := os.ReadDir(filepath.Join(dir, quarantineDirName))
	if err != nil {
		t.Fatal(err)
	}
	foundReason := false
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), quarantineMetadataSuffix) {
			continue
		}
		payload, err := os.ReadFile(filepath.Join(dir, quarantineDirName, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(payload), "secret-result-marker") {
			t.Fatal("quarantine reason metadata contains result payload")
		}
		if strings.Contains(string(payload), "lease_rejected") {
			foundReason = true
		}
	}
	if !foundReason {
		t.Fatal("quarantine reason metadata not found")
	}
}

func TestRejectRequiresSafeReasonCode(t *testing.T) {
	s, err := Open(t.TempDir(), 2, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	result := testResult("11111111-1111-4111-8111-111111111111")
	if err := s.Put(result); err != nil {
		t.Fatal(err)
	}
	if err := s.Reject(result.TaskID, "payload: secret value"); err == nil {
		t.Fatal("Reject accepted unsafe reason code")
	}
	batch, err := s.Batch(100)
	if err != nil || len(batch) != 1 {
		t.Fatalf("unsafe rejection removed result: batch=%#v err=%v", batch, err)
	}
}

func TestMalformedOccupantDoesNotDiscardNewValidResult(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, 2, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	result := testResult("11111111-1111-4111-8111-111111111111")
	path := filepath.Join(dir, resultsDirName, result.TaskID+resultFileSuffix)
	if err := os.WriteFile(path, []byte(`{"protocol":"wrong"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(result); err != nil {
		t.Fatal(err)
	}
	batch, err := s.Batch(100)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch) != 1 || !reflect.DeepEqual(batch[0], result) {
		t.Fatalf("batch = %#v, want %#v", batch, result)
	}
}

func TestQuarantineIsBounded(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, 200, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxQuarantineItems+7; i++ {
		name := fmt.Sprintf("%08x-1111-4111-8111-%012x%s", i, i, resultFileSuffix)
		if err := os.WriteFile(filepath.Join(dir, resultsDirName, name), []byte(`{`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Batch(100); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(dir, quarantineDirName))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) > maxQuarantineItems {
		t.Fatalf("quarantine has %d entries", len(entries))
	}
}

func TestQuarantineIsByteBoundedAndOversizedPayloadIsRemoved(t *testing.T) {
	dir := t.TempDir()
	const maxBytes = int64(512)
	s, err := Open(dir, 20, maxBytes)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		name := fmt.Sprintf("%08x-1111-4111-8111-%012x%s", i, i, resultFileSuffix)
		payload := []byte(`{"invalid":"` + strings.Repeat("x", 180) + `"}`)
		if i == 0 {
			payload = []byte(strings.Repeat("x", int(maxBytes)+1))
		}
		if err := os.WriteFile(filepath.Join(dir, resultsDirName, name), payload, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Batch(100); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(dir, quarantineDirName))
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		total += info.Size()
	}
	if total > maxBytes {
		t.Fatalf("quarantine bytes = %d, limit = %d", total, maxBytes)
	}
	remaining, err := os.ReadDir(filepath.Join(dir, resultsDirName))
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 0 {
		t.Fatalf("malformed inputs remain in results: %v", entryNames(remaining))
	}
}

func TestResultRemainsUntilAcknowledged(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, 2, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	result := testResult("11111111-1111-4111-8111-111111111111")
	if err := s.Put(result); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		batch, err := s.Batch(100)
		if err != nil {
			t.Fatal(err)
		}
		if len(batch) != 1 {
			t.Fatalf("attempt %d batch length = %d", i, len(batch))
		}
	}
	if err := s.Ack(result.TaskID); err != nil {
		t.Fatal(err)
	}
	batch, err := s.Batch(100)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch) != 0 {
		t.Fatalf("acknowledged result remains: %#v", batch)
	}
	if err := s.Ack(result.TaskID); err != nil {
		t.Fatalf("duplicate Ack: %v", err)
	}
}

func TestBatchIsLimitedToOneHundred(t *testing.T) {
	s, err := Open(t.TempDir(), 101, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 101; i++ {
		if err := s.Put(testResult(fmt.Sprintf("%08x-1111-4111-8111-%012x", i, i))); err != nil {
			t.Fatal(err)
		}
	}
	batch, err := s.Batch(101)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch) != 100 {
		t.Fatalf("batch length = %d, want 100", len(batch))
	}
}

func TestRejectsInvalidIdentityAndResult(t *testing.T) {
	s, err := Open(t.TempDir(), 2, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	bad := testResult("../outside")
	if err := s.Put(bad); err == nil {
		t.Fatal("Put accepted path traversal task ID")
	}
	if err := s.Ack("../outside"); err == nil {
		t.Fatal("Ack accepted path traversal task ID")
	}
	bad = testResult("11111111-1111-4111-8111-111111111111")
	bad.MeasuredAt = time.Time{}
	if err := s.Put(bad); err == nil {
		t.Fatal("Put accepted missing measuredAt")
	}
}

func TestConcurrentPutBatchAndAck(t *testing.T) {
	s, err := Open(t.TempDir(), 64, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		id := fmt.Sprintf("%08x-1111-4111-8111-%012x", i, i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.Put(testResult(id)); err != nil {
				t.Errorf("Put: %v", err)
			}
			if _, err := s.Batch(10); err != nil {
				t.Errorf("Batch: %v", err)
			}
			if err := s.Ack(id); err != nil {
				t.Errorf("Ack: %v", err)
			}
		}()
	}
	wg.Wait()
}

func testResult(taskID string) protocol.Result {
	return protocol.Result{
		Protocol: protocol.Version, TaskID: taskID,
		LeaseID:        "44444444-4444-4444-8444-444444444444",
		AddressVersion: 3, ConfigVersion: 7,
		Outcome: protocol.OutcomeSuccess, LatencyMS: 42.5,
		MeasuredAt: time.Date(2026, 9, 15, 11, 59, 58, 0, time.UTC), StatusCode: 200,
	}
}

func entryNames(entries []os.DirEntry) []string {
	names := make([]string, len(entries))
	for i, entry := range entries {
		names[i] = entry.Name()
	}
	return names
}

func TestCapacityAndContainsReserveCompletedResults(t *testing.T) {
	s, err := Open(t.TempDir(), 2, 4096)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	items, bytes, err := s.Available()
	if err != nil || items != 2 || bytes != 4096 {
		t.Fatalf("empty capacity = %d %d %v", items, bytes, err)
	}
	result := protocol.Result{Protocol: protocol.Version, TaskID: "11111111-1111-4111-8111-111111111111", LeaseID: "22222222-2222-4222-8222-222222222222", AddressVersion: 1, ConfigVersion: 1, Outcome: protocol.OutcomeSuccess, MeasuredAt: time.Now().UTC()}
	if err := s.Put(result); err != nil {
		t.Fatal(err)
	}
	items, bytes, err = s.Available()
	if err != nil || items != 1 || bytes >= 4096 {
		t.Fatalf("used capacity = %d %d %v", items, bytes, err)
	}
	if found, err := s.Contains(result.TaskID); err != nil || !found {
		t.Fatalf("Contains = %v %v", found, err)
	}
	if _, err := s.Contains("../bad"); err == nil {
		t.Fatal("lookup accepted invalid identity")
	}
	s.Close()
	if _, _, err := s.Available(); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed capacity = %v", err)
	}
}

func TestReopenCleansOnlyAbandonedInternalTemporaryFiles(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, 2, 4096)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	want := testResult("11111111-1111-4111-8111-111111111111")
	if err := s.Put(want); err != nil {
		t.Fatal(err)
	}
	rejected := testResult("22222222-2222-4222-8222-222222222222")
	if err := s.Put(rejected); err != nil {
		t.Fatal(err)
	}
	if err := s.Reject(rejected.TaskID, "lease_rejected"); err != nil {
		t.Fatal(err)
	}
	metadata, err := os.ReadDir(s.quarantineDir)
	if err != nil || len(metadata) != 1 {
		t.Fatalf("metadata=%v err=%v", metadata, err)
	}
	reasonPath := filepath.Join(s.quarantineDir, metadata[0].Name())
	reason, err := os.ReadFile(reasonPath)
	if err != nil {
		t.Fatal(err)
	}
	orphans := []string{filepath.Join(s.resultsDir, ".result-123456.tmp"), filepath.Join(s.quarantineDir, ".quarantine-987654.tmp")}
	for _, path := range orphans {
		if err := os.WriteFile(path, []byte(strings.Repeat("x", 8192)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	unrelated := []string{filepath.Join(s.resultsDir, ".unrelated.tmp"), filepath.Join(s.resultsDir, ".result-not-internal.tmp"), filepath.Join(s.resultsDir, ".quarantine-123.tmp"), filepath.Join(s.quarantineDir, ".result-123.tmp")}
	for _, path := range unrelated {
		if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// A competing opener must not remove the active owner's temporary files.
	if other, err := Open(dir, 2, 4096); !errors.Is(err, ErrInUse) {
		if other != nil {
			other.Close()
		}
		t.Fatalf("competing Open=%v", err)
	}
	for _, path := range orphans {
		if _, err := os.Stat(path); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(dir, 2, 4096)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	for _, path := range orphans {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("orphan remains: %s: %v", path, err)
		}
	}
	for _, path := range unrelated {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != "keep" {
			t.Errorf("unrelated file changed: %s: %v", path, err)
		}
	}
	gotReason, err := os.ReadFile(reasonPath)
	if err != nil || string(gotReason) != string(reason) {
		t.Fatalf("committed reason changed: %v", err)
	}
	got, err := reopened.Batch(100)
	if err != nil || len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Fatalf("batch=%#v err=%v", got, err)
	}
	if err := reopened.Put(testResult("33333333-3333-4333-8333-333333333333")); err != nil {
		t.Fatal(err)
	}
	if err := reopened.Put(testResult("44444444-4444-4444-8444-444444444444")); !errors.Is(err, ErrFull) {
		t.Fatalf("capacity limit lost: %v", err)
	}
}
