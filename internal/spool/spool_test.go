package spool

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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
	if err := os.WriteFile(filepath.Join(dir, resultsDirName, badName), []byte(`{"protocol":"wrong"}`), 0o600); err != nil {
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
		t.Fatalf("quarantine entries = %d, want 1", len(quarantined))
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
