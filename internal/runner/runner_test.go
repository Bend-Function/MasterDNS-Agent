package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Bend-Function/MasterDNS-Agent/internal/client"
	"github.com/Bend-Function/MasterDNS-Agent/internal/config"
	"github.com/Bend-Function/MasterDNS-Agent/internal/protocol"
	"github.com/Bend-Function/MasterDNS-Agent/internal/spool"
)

const probeID = "11111111-1111-4111-8111-111111111111"

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time                 { return c.now }
func (c *fakeClock) NewTimer(d time.Duration) Timer { return realClock{}.NewTimer(d) }

type fakeClient struct {
	response                            protocol.LeaseResponse
	leaseCalls, submitCalls, heartbeats int
	capacity                            int
	err, submitErr                      error
	status                              string
	uploaded                            []protocol.Result
}

func (c *fakeClient) Lease(_ context.Context, capacity int) (client.LeaseResponse, error) {
	c.leaseCalls++
	c.capacity = capacity
	return c.response, c.err
}
func (c *fakeClient) Heartbeat(context.Context, client.Capabilities) error {
	c.heartbeats++
	return c.err
}
func (c *fakeClient) Submit(_ context.Context, results []protocol.Result) ([]client.Ack, error) {
	c.submitCalls++
	c.uploaded = append(c.uploaded, results...)
	if c.submitErr != nil {
		return nil, c.submitErr
	}
	acks := make([]client.Ack, len(results))
	for i, r := range results {
		acks[i] = client.Ack{TaskID: r.TaskID, Status: c.status}
	}
	return acks, nil
}

type fakeChecker struct {
	calls   atomic.Int32
	started chan protocol.Task
	release chan struct{}
}

func (c *fakeChecker) Check(ctx context.Context, task protocol.Task) protocol.Result {
	c.calls.Add(1)
	c.started <- task
	select {
	case <-c.release:
	case <-ctx.Done():
	}
	return protocol.Result{Protocol: protocol.Version, TaskID: task.TaskID, LeaseID: task.LeaseID, AddressVersion: task.AddressVersion, ConfigVersion: task.ConfigVersion, Outcome: protocol.OutcomeSuccess, MeasuredAt: time.Now().UTC()}
}

type fixture struct {
	t       *testing.T
	run     *Runner
	clock   *fakeClock
	client  *fakeClient
	checker *fakeChecker
	spool   *spool.Spool
}

func newRunnerFixture(t *testing.T) *fixture {
	t.Helper()
	disk, err := spool.Open(t.TempDir(), 4, 8192)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = disk.Close() })
	clock := &fakeClock{now: time.Now()}
	platform := &fakeClient{status: protocol.AckAccepted, response: protocol.LeaseResponse{ServerTime: clock.Now()}}
	check := &fakeChecker{started: make(chan protocol.Task, 64), release: make(chan struct{})}
	run, err := New(platform, check, disk, config.Config{ProbeID: probeID, MaxConcurrency: 2, AllowIPv4: true}, clock)
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{t, run, clock, platform, check, disk}
}
func taskAt(now time.Time, n int) protocol.Task {
	return protocol.Task{Protocol: protocol.Version, TaskID: fmt.Sprintf("%08d-1111-4111-8111-111111111111", n), RoundID: probeID, ProbeID: probeID, LeaseID: "22222222-2222-4222-8222-222222222222", AddressVersion: 1, ConfigVersion: 2, Address: "8.8.8.8", Family: 4, Config: protocol.CheckConfig{Type: "tcp", Port: 443, TimeoutMS: 10000}, Deadline: now.Add(30 * time.Second)}
}
func (f *fixture) Enqueue(tasks ...protocol.Task) {
	f.client.response = protocol.LeaseResponse{ServerTime: f.clock.Now(), Tasks: tasks}
	f.client.response.SetReceivedAt(f.clock.Now())
}
func (f *fixture) Tick() {
	f.t.Helper()
	if err := f.run.step(context.Background()); err != nil {
		f.t.Fatal(err)
	}
}
func (f *fixture) CheckCalls() int { return int(f.checker.calls.Load()) }
func (f *fixture) started() protocol.Task {
	f.t.Helper()
	select {
	case task := <-f.checker.started:
		return task
	case <-time.After(time.Second):
		f.t.Fatal("check never started")
		return protocol.Task{}
	}
}
func (f *fixture) finish(n int) {
	f.t.Helper()
	close(f.checker.release)
	for range n {
		select {
		case result := <-f.run.completed:
			f.run.accept(result)
		case <-time.After(time.Second):
			f.t.Fatal("check never completed")
		}
	}
}

func TestExpiredTaskIsNotExecuted(t *testing.T) {
	f := newRunnerFixture(t)
	task := taskAt(f.clock.Now(), 1)
	task.Deadline = f.clock.Now()
	f.Enqueue(task)
	f.Tick()
	if f.CheckCalls() != 0 || len(f.run.active) != 0 {
		t.Fatal("executed expired lease")
	}
}
func TestServerTimeBudgetAndIdentity(t *testing.T) {
	f := newRunnerFixture(t)
	task := taskAt(f.clock.Now().Add(-24*time.Hour), 1)
	f.client.response = protocol.LeaseResponse{ServerTime: f.clock.Now().Add(-24 * time.Hour), Tasks: []protocol.Task{task}}
	f.client.response.SetReceivedAt(f.clock.Now())
	f.Tick()
	started := f.started()
	if d := started.Deadline.Sub(f.clock.Now()); d != 10*time.Second {
		t.Fatalf("local check deadline = %s", d)
	}
	f.finish(1)
	f.Tick()
	if len(f.client.uploaded) != 1 || f.client.uploaded[0].LeaseID != task.LeaseID {
		t.Fatal("result identity changed")
	}
	bad := taskAt(f.clock.Now(), 2)
	bad.ProbeID = "33333333-3333-4333-8333-333333333333"
	f.clock.now = f.clock.now.Add(2 * time.Second)
	f.Enqueue(bad)
	f.Tick()
	if f.CheckCalls() != 1 || len(f.run.active) != 0 {
		t.Fatal("executed another probe's task")
	}
}
func TestConcurrencyDuplicatesAndUploadRetry(t *testing.T) {
	f := newRunnerFixture(t)
	a, b := taskAt(f.clock.Now(), 1), taskAt(f.clock.Now(), 2)
	f.Enqueue(a, b)
	f.Tick()
	f.started()
	f.started()
	f.clock.now = f.clock.now.Add(2 * time.Second)
	f.Enqueue(a, b)
	f.Tick()
	if f.client.leaseCalls != 1 || f.CheckCalls() != 2 {
		t.Fatal("exceeded in-flight capacity")
	}
	f.finish(2)
	f.client.submitErr = errors.New("offline")
	f.Tick()
	if f.client.submitCalls != 1 {
		t.Fatal("did not upload completed results")
	}
	f.Tick()
	if f.client.submitCalls != 1 {
		t.Fatal("retried without backoff")
	}
	f.client.submitErr = nil
	f.clock.now = f.clock.now.Add(2 * time.Second)
	f.Tick()
	if f.CheckCalls() != 2 || f.client.submitCalls != 2 {
		t.Fatal("upload retry rechecked tasks")
	}
	batch, err := f.spool.Batch(100)
	if err != nil || len(batch) != 0 {
		t.Fatalf("spool after ack = %v, %v", batch, err)
	}
}
func TestDuplicateWithinBatchAndNewLeaseNeverRecheck(t *testing.T) {
	f := newRunnerFixture(t)
	a := taskAt(f.clock.Now(), 1)
	b := a
	b.LeaseID = "33333333-3333-4333-8333-333333333333"
	f.Enqueue(a, b)
	f.Tick()
	f.started()
	f.finish(1)
	f.Tick()
	f.clock.now = f.clock.now.Add(2 * time.Second)
	f.Tick()
	if f.CheckCalls() != 1 || len(f.client.uploaded) != 1 || f.client.uploaded[0].LeaseID != a.LeaseID {
		t.Fatal("duplicate re-executed or lease grafted")
	}
}
func TestSpoolReservationBoundsDisconnectedWork(t *testing.T) {
	f := newRunnerFixture(t)
	f.Enqueue(taskAt(f.clock.Now(), 1), taskAt(f.clock.Now(), 2))
	f.Tick()
	f.started()
	f.started()
	f.finish(2)
	f.client.submitErr = errors.New("offline")
	f.Tick()
	f.clock.now = f.clock.now.Add(time.Minute)
	f.Tick()
	if f.CheckCalls() != 2 || f.client.leaseCalls != 1 {
		t.Fatal("leased while uploads failed")
	}
	batch, _ := f.spool.Batch(100)
	if len(batch) != 2 {
		t.Fatal("lost disconnected results")
	}
}
func TestAcknowledgements(t *testing.T) {
	for _, status := range []string{protocol.AckAccepted, protocol.AckDuplicate, protocol.AckStale, protocol.AckRejected} {
		t.Run(status, func(t *testing.T) {
			f := newRunnerFixture(t)
			dir := t.TempDir()
			disk, err := spool.Open(dir, 4, 8192)
			if err != nil {
				t.Fatal(err)
			}
			defer disk.Close()
			f.run.spool = disk
			f.spool = disk
			f.client.status = status
			f.Enqueue(taskAt(f.clock.Now(), 1))
			f.Tick()
			f.started()
			f.finish(1)
			f.Tick()
			batch, err := f.spool.Batch(100)
			if err != nil || len(batch) != 0 {
				t.Fatalf("terminal acknowledgement retained result: %v", err)
			}
			entries, err := os.ReadDir(filepath.Join(dir, "quarantine"))
			if err != nil {
				t.Fatal(err)
			}
			if (status == protocol.AckRejected) != (len(entries) == 1) {
				t.Fatal("incorrect rejection quarantine routing")
			}
		})
	}
}
func TestUnauthorizedStopsLeasing(t *testing.T) {
	f := newRunnerFixture(t)
	f.client.err = client.ErrUnauthorized
	if err := f.run.Run(context.Background()); !errors.Is(err, client.ErrUnauthorized) {
		t.Fatalf("Run error = %v", err)
	}
	if f.client.leaseCalls != 0 {
		t.Fatal("leased after authentication rejection")
	}
}
func TestCanceledRunPersistsInFlightResult(t *testing.T) {
	f := newRunnerFixture(t)
	f.Enqueue(taskAt(f.clock.Now(), 1))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.run.Run(ctx) }()
	f.started()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown exceeded bound")
	}
	batch, err := f.spool.Batch(100)
	if err != nil || len(batch) != 1 {
		t.Fatalf("shutdown lost result: %v, %v", batch, err)
	}
}
func TestRunnerRejectsConcurrencyAboveHardLimit(t *testing.T) {
	f := newRunnerFixture(t)
	if _, err := New(f.client, f.checker, f.spool, config.Config{ProbeID: probeID, MaxConcurrency: 65}, f.clock); err == nil {
		t.Fatal("accepted concurrency above 64")
	}
}

func TestLeaseFailuresBackOffWithoutNestedRetries(t *testing.T) {
	f := newRunnerFixture(t)
	f.Tick()
	f.run.nextHeartbeat = f.clock.Now().Add(time.Hour)
	f.client.err = errors.New("offline")
	f.clock.now = f.clock.now.Add(2 * time.Second)
	for _, minimum := range []time.Duration{800 * time.Millisecond, 1600 * time.Millisecond, 3200 * time.Millisecond, 6400 * time.Millisecond, 12800 * time.Millisecond, 25600 * time.Millisecond, 48 * time.Second, 48 * time.Second} {
		before := f.client.leaseCalls
		f.Tick()
		if f.client.leaseCalls != before+1 {
			t.Fatal("wrong number of lease attempts")
		}
		wait := f.run.waitDuration()
		if wait < minimum || wait > 60*time.Second {
			t.Fatalf("retry wait %s < %s", wait, minimum)
		}
		f.Tick()
		if f.client.leaseCalls != before+1 {
			t.Fatal("retried without advancing clock")
		}
		f.clock.now = f.clock.now.Add(wait)
	}
}

func TestServerRetryAfterAndHeartbeatInterval(t *testing.T) {
	f := newRunnerFixture(t)
	f.Enqueue()
	f.client.response.RetryAfterMS = 35000
	f.Tick()
	f.clock.now = f.clock.now.Add(29 * time.Second)
	f.Tick()
	if f.client.heartbeats != 1 || f.client.leaseCalls != 1 {
		t.Fatal("polled before server delay or heartbeat interval")
	}
	f.clock.now = f.clock.now.Add(time.Second)
	f.Tick()
	if f.client.heartbeats != 2 || f.client.leaseCalls != 1 {
		t.Fatal("heartbeat should continue during lease delay")
	}
	f.clock.now = f.clock.now.Add(5 * time.Second)
	f.Tick()
	if f.client.leaseCalls != 2 {
		t.Fatal("did not honor retryAfterMs")
	}
}

func TestResultSpaceReservedBeforeLeasing(t *testing.T) {
	f := newRunnerFixture(t)
	disk, err := spool.Open(t.TempDir(), 1, 1024)
	if err != nil {
		t.Fatal(err)
	}
	defer disk.Close()
	f.run.spool = disk
	f.Enqueue(taskAt(f.clock.Now(), 1))
	f.Tick()
	f.started()
	if f.client.capacity != 1 {
		t.Fatalf("lease capacity = %d", f.client.capacity)
	}
	f.finish(1)
	if err := f.run.persist(); err != nil {
		t.Fatalf("reserved result did not fit: %v", err)
	}
	f.clock.now = f.clock.now.Add(2 * time.Second)
	f.client.submitErr = errors.New("offline")
	f.Tick()
	if f.client.leaseCalls != 1 {
		t.Fatal("leased beyond disk reservation")
	}
}

func TestRestartDoesNotAttachOldResultToNewLease(t *testing.T) {
	f := newRunnerFixture(t)
	task := taskAt(f.clock.Now(), 1)
	old := protocol.Result{Protocol: protocol.Version, TaskID: task.TaskID, LeaseID: "44444444-4444-4444-8444-444444444444", AddressVersion: 1, ConfigVersion: 1, Outcome: protocol.OutcomeSuccess, MeasuredAt: time.Now()}
	if err := f.spool.Put(old); err != nil {
		t.Fatal(err)
	}
	// Exercise lease admission directly with an old result still on disk.
	found, err := f.spool.Contains(task.TaskID)
	if err != nil || !found {
		t.Fatal("old result missing")
	}
	f.Enqueue(task)
	f.run.nextHeartbeat = f.clock.Now().Add(time.Hour)
	if err := f.run.lease(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.CheckCalls() != 0 || len(f.run.active) != 0 {
		t.Fatal("rechecked persisted task with new lease")
	}
	f.Tick()
	if len(f.client.uploaded) != 1 || f.client.uploaded[0].LeaseID != old.LeaseID {
		t.Fatal("old lease was grafted")
	}
}

type pressuredSpool struct {
	Spool
	full bool
}

func (s *pressuredSpool) Put(result protocol.Result) error {
	if s.full {
		return spool.ErrFull
	}
	return s.Spool.Put(result)
}

func TestUnexpectedSpoolFullRetainsResultAndStopsLeasing(t *testing.T) {
	f := newRunnerFixture(t)
	disk := &pressuredSpool{Spool: f.spool, full: true}
	f.run.spool = disk
	f.Enqueue(taskAt(f.clock.Now(), 1))
	f.Tick()
	f.started()
	f.finish(1)
	f.Tick()
	f.clock.now = f.clock.now.Add(10 * time.Second)
	f.Tick()
	if f.client.leaseCalls != 1 || len(f.run.pending) != 1 {
		t.Fatal("spool pressure lost result or admitted more work")
	}
	disk.full = false
	f.Tick()
	batch, _ := f.spool.Batch(100)
	if len(f.run.pending) != 0 || len(batch) != 0 || len(f.client.uploaded) != 1 {
		t.Fatal("recovered spool did not upload held result")
	}
}
func TestShutdownReportsUndurableCompletedResults(t *testing.T) {
	f := newRunnerFixture(t)
	f.run.spool = &pressuredSpool{Spool: f.spool, full: true}
	f.Enqueue(taskAt(f.clock.Now(), 1))
	f.Tick()
	f.started()
	f.finish(1)
	if err := f.run.shutdown(); !errors.Is(err, spool.ErrFull) {
		t.Fatalf("shutdown silently discarded completed result: %v", err)
	}
	if len(f.run.pending) != 1 {
		t.Fatal("failed persistence removed result")
	}
}

func TestLeaseBudgetAccountsForTimeSinceReceipt(t *testing.T) {
	f := newRunnerFixture(t)
	f.Enqueue(taskAt(f.clock.Now(), 1))
	f.client.response.SetReceivedAt(f.clock.Now().Add(-time.Minute))
	f.Tick()
	if f.CheckCalls() != 0 || len(f.run.active) != 0 {
		t.Fatal("executed lease expired since receipt")
	}
}

func TestUnauthorizedFromLeaseAndUploadIsTerminal(t *testing.T) {
	t.Run("lease", func(t *testing.T) {
		f := newRunnerFixture(t)
		f.run.nextHeartbeat = f.clock.Now().Add(time.Hour)
		f.client.err = client.ErrUnauthorized
		if err := f.run.Run(context.Background()); !errors.Is(err, client.ErrUnauthorized) {
			t.Fatalf("Run = %v", err)
		}
		if f.client.leaseCalls != 1 {
			t.Fatal("retried unauthorized lease")
		}
	})
	t.Run("upload", func(t *testing.T) {
		f := newRunnerFixture(t)
		f.Enqueue(taskAt(f.clock.Now(), 1))
		f.Tick()
		f.started()
		f.finish(1)
		f.client.submitErr = client.ErrUnauthorized
		if err := f.run.Run(context.Background()); !errors.Is(err, client.ErrUnauthorized) {
			t.Fatalf("Run = %v", err)
		}
		batch, _ := f.spool.Batch(100)
		if len(batch) != 1 || f.client.leaseCalls != 1 || f.client.submitCalls != 1 {
			t.Fatal("unauthorized upload lost result or continued polling")
		}
	})
}

type manualTimer struct {
	events   chan time.Time
	duration time.Duration
}

func (t *manualTimer) C() <-chan time.Time { return t.events }
func (t *manualTimer) Stop() bool          { return true }

type manualClock struct {
	now    time.Time
	timers chan *manualTimer
}

func (c *manualClock) Now() time.Time { return c.now }
func (c *manualClock) NewTimer(d time.Duration) Timer {
	timer := &manualTimer{make(chan time.Time, 1), d}
	c.timers <- timer
	return timer
}

type stuckChecker struct{ started, release, finished chan struct{} }

func (c *stuckChecker) Check(_ context.Context, task protocol.Task) protocol.Result {
	close(c.started)
	<-c.release
	defer close(c.finished)
	return protocol.Result{Protocol: protocol.Version, TaskID: task.TaskID, LeaseID: task.LeaseID, AddressVersion: 1, ConfigVersion: 1, Outcome: protocol.OutcomeUnavailable, MeasuredAt: time.Now()}
}
func TestShutdownDeadlineWithUncooperativeChecker(t *testing.T) {
	f := newRunnerFixture(t)
	clock := &manualClock{now: f.clock.Now(), timers: make(chan *manualTimer, 10)}
	f.run.clock = clock
	check := &stuckChecker{make(chan struct{}), make(chan struct{}), make(chan struct{})}
	f.run.checker = check
	defer func() { close(check.release); <-check.finished }()
	f.Enqueue(taskAt(clock.Now(), 1))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- f.run.Run(ctx) }()
	select {
	case <-check.started:
	case <-time.After(time.Second):
		t.Fatal("check did not start")
	}
	cancel()
	for {
		select {
		case timer := <-clock.timers:
			if timer.duration == 5*time.Second {
				timer.events <- clock.Now().Add(5 * time.Second)
				goto fired
			}
		case <-time.After(time.Second):
			t.Fatal("shutdown did not install its deadline")
		}
	}
fired:
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("unfinished check was not reported")
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown ignored deadline")
	}
}
