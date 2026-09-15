package spool

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Bend-Function/MasterDNS-Agent/internal/protocol"
)

const (
	DefaultMaxItems    = 1000
	DefaultMaxBytes    = 10 << 20
	maxBatchItems      = 100
	maxQuarantineItems = 100
	resultsDirName     = "results"
	quarantineDirName  = "quarantine"
	resultFileSuffix   = ".json"
)

var ErrFull = errors.New("result spool is full")

type Spool struct {
	mu            sync.Mutex
	resultsDir    string
	quarantineDir string
	maxItems      int
	maxBytes      int64
}

func Open(dir string, maxItems int, maxBytes int64) (*Spool, error) {
	if dir == "" {
		return nil, errors.New("spool directory is required")
	}
	if maxItems < 0 || maxBytes < 0 {
		return nil, errors.New("spool limits cannot be negative")
	}
	if maxItems == 0 {
		maxItems = DefaultMaxItems
	}
	if maxBytes == 0 {
		maxBytes = DefaultMaxBytes
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create spool directory: %w", err)
	}
	resultsDir := filepath.Join(dir, resultsDirName)
	quarantineDir := filepath.Join(dir, quarantineDirName)
	for _, path := range []string{resultsDir, quarantineDir} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return nil, fmt.Errorf("create spool subdirectory: %w", err)
		}
		if err := os.Chmod(path, 0o700); err != nil {
			return nil, fmt.Errorf("restrict spool subdirectory: %w", err)
		}
	}
	return &Spool{resultsDir: resultsDir, quarantineDir: quarantineDir, maxItems: maxItems, maxBytes: maxBytes}, nil
}

func (s *Spool) Put(result protocol.Result) error {
	if err := validateResult(result); err != nil {
		return fmt.Errorf("invalid result: %w", err)
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("encode result: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	finalPath := s.resultPath(result.TaskID)
	if info, err := os.Stat(finalPath); err == nil {
		valid, err := existingResultIsValid(finalPath, info, result.TaskID, s.maxBytes)
		if err != nil {
			return fmt.Errorf("inspect existing result: %w", err)
		}
		if valid {
			return nil
		}
		if err := s.quarantine(finalPath, filepath.Base(finalPath)); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect existing result: %w", err)
	}
	items, usedBytes, err := s.usage()
	if err != nil {
		return err
	}
	if items >= s.maxItems || int64(len(payload)) > s.maxBytes-usedBytes {
		return ErrFull
	}

	temporary, err := os.CreateTemp(s.resultsDir, ".result-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary result: %w", err)
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("restrict temporary result: %w", err)
	}
	if _, err := temporary.Write(payload); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write temporary result: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync temporary result: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary result: %w", err)
	}
	if err := os.Rename(temporaryPath, finalPath); err != nil {
		return fmt.Errorf("commit result: %w", err)
	}
	committed = true
	if err := syncDir(s.resultsDir); err != nil {
		return fmt.Errorf("sync result directory: %w", err)
	}
	return nil
}

func (s *Spool) Batch(limit int) ([]protocol.Result, error) {
	if limit < 1 {
		return nil, errors.New("batch limit must be positive")
	}
	if limit > maxBatchItems {
		limit = maxBatchItems
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := os.ReadDir(s.resultsDir)
	if err != nil {
		return nil, fmt.Errorf("read result spool: %w", err)
	}
	results := make([]protocol.Result, 0, min(limit, len(entries)))
	for _, entry := range entries {
		if len(results) == limit {
			break
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), resultFileSuffix) {
			continue
		}
		path := filepath.Join(s.resultsDir, entry.Name())
		info, statErr := entry.Info()
		if statErr != nil {
			return nil, fmt.Errorf("inspect spooled result %q: %w", entry.Name(), statErr)
		}
		if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > s.maxBytes {
			if err := s.quarantine(path, entry.Name()); err != nil {
				return nil, err
			}
			continue
		}
		payload, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil, fmt.Errorf("read spooled result %q: %w", entry.Name(), readErr)
		}
		var result protocol.Result
		if decodeResult(payload, &result) != nil || result.TaskID+resultFileSuffix != entry.Name() || validateResult(result) != nil {
			if err := s.quarantine(path, entry.Name()); err != nil {
				return nil, err
			}
			continue
		}
		results = append(results, result)
	}
	return results, nil
}

func (s *Spool) Ack(taskID string) error {
	if !protocol.ValidID(taskID) {
		return errors.New("task ID must be a UUID")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.Remove(s.resultPath(taskID)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("remove acknowledged result: %w", err)
	}
	if err := syncDir(s.resultsDir); err != nil {
		return fmt.Errorf("sync acknowledged result deletion: %w", err)
	}
	return nil
}

func (s *Spool) resultPath(taskID string) string {
	return filepath.Join(s.resultsDir, taskID+resultFileSuffix)
}

func (s *Spool) usage() (int, int64, error) {
	entries, err := os.ReadDir(s.resultsDir)
	if err != nil {
		return 0, 0, fmt.Errorf("read result spool: %w", err)
	}
	var items int
	var usedBytes int64
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), resultFileSuffix) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return 0, 0, fmt.Errorf("inspect spooled result %q: %w", entry.Name(), err)
		}
		items++
		usedBytes += info.Size()
	}
	return items, usedBytes, nil
}

func (s *Spool) quarantine(sourcePath, sourceName string) error {
	if err := s.pruneQuarantine(maxQuarantineItems - 1); err != nil {
		return err
	}
	for attempt := 0; attempt < 10; attempt++ {
		name := fmt.Sprintf("%d-%d-%s.bad", time.Now().UnixNano(), attempt, sourceName)
		destination := filepath.Join(s.quarantineDir, name)
		if _, err := os.Stat(destination); err == nil {
			continue
		}
		if err := os.Rename(sourcePath, destination); err != nil {
			return fmt.Errorf("quarantine malformed result %q: %w", sourceName, err)
		}
		if err := syncDir(s.resultsDir); err != nil {
			return fmt.Errorf("sync result quarantine: %w", err)
		}
		if err := syncDir(s.quarantineDir); err != nil {
			return fmt.Errorf("sync quarantine directory: %w", err)
		}
		return nil
	}
	return fmt.Errorf("quarantine malformed result %q: cannot allocate destination", sourceName)
}

func (s *Spool) pruneQuarantine(keep int) error {
	entries, err := os.ReadDir(s.quarantineDir)
	if err != nil {
		return fmt.Errorf("read quarantine: %w", err)
	}
	type candidate struct {
		name     string
		modified time.Time
	}
	files := make([]candidate, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect quarantine entry %q: %w", entry.Name(), err)
		}
		files = append(files, candidate{name: entry.Name(), modified: info.ModTime()})
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].modified.Equal(files[j].modified) {
			return files[i].name < files[j].name
		}
		return files[i].modified.Before(files[j].modified)
	})
	for len(files) > keep {
		if err := os.Remove(filepath.Join(s.quarantineDir, files[0].name)); err != nil {
			return fmt.Errorf("prune quarantine entry %q: %w", files[0].name, err)
		}
		files = files[1:]
	}
	return nil
}

func decodeResult(payload []byte, result *protocol.Result) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(result); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func existingResultIsValid(path string, info os.FileInfo, taskID string, maxBytes int64) (bool, error) {
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxBytes {
		return false, nil
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	var result protocol.Result
	if decodeResult(payload, &result) != nil || validateResult(result) != nil {
		return false, nil
	}
	return result.TaskID == taskID, nil
}

func validateResult(result protocol.Result) error {
	if result.Protocol != protocol.Version {
		return errors.New("unsupported protocol")
	}
	if !protocol.ValidID(result.TaskID) {
		return errors.New("task ID must be a UUID")
	}
	if !protocol.ValidID(result.LeaseID) {
		return errors.New("lease ID must be a UUID")
	}
	if result.AddressVersion < 1 || result.ConfigVersion < 1 {
		return errors.New("versions must be positive")
	}
	switch result.Outcome {
	case protocol.OutcomeSuccess, protocol.OutcomeFailure, protocol.OutcomeUnavailable:
	default:
		return errors.New("invalid outcome")
	}
	if math.IsNaN(result.LatencyMS) || math.IsInf(result.LatencyMS, 0) || result.LatencyMS < 0 {
		return errors.New("latency must be finite and nonnegative")
	}
	if result.MeasuredAt.IsZero() {
		return errors.New("measuredAt is required")
	}
	if result.StatusCode != 0 && (result.StatusCode < 100 || result.StatusCode > 599) {
		return errors.New("status code must be zero or an HTTP status")
	}
	return nil
}

func syncDir(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
