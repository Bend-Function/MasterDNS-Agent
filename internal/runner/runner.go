// Package runner coordinates bounded, single-attempt checks and durable uploads.
package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/Bend-Function/MasterDNS-Agent/internal/client"
	"github.com/Bend-Function/MasterDNS-Agent/internal/config"
	"github.com/Bend-Function/MasterDNS-Agent/internal/protocol"
	"github.com/Bend-Function/MasterDNS-Agent/internal/spool"
)

const (
	heartbeatInterval = 30 * time.Second
	idleInterval      = 2 * time.Second
	shutdownTimeout   = 5 * time.Second
	// More than the complete serialized result with a 64-character error code.
	resultReservation  = 1024
	maxRememberedTasks = 1000
)

type Client interface {
	Lease(context.Context, int) (client.LeaseResponse, error)
	Submit(context.Context, []protocol.Result) ([]client.Ack, error)
	Heartbeat(context.Context, client.Capabilities) error
}
type Checker interface {
	Check(context.Context, protocol.Task) protocol.Result
}
type Spool interface {
	Put(protocol.Result) error
	Batch(int) ([]protocol.Result, error)
	Ack(string) error
	Reject(string, string) error
	Contains(string) (bool, error)
	Available() (int, int64, error)
}

// Runner is single-use. The caller owns and closes the spool after Run returns.
// Checkers must honor context cancellation; the production checker does so.
type Runner struct {
	client                                Client
	checker                               Checker
	spool                                 Spool
	cfg                                   config.Config
	clock                                 Clock
	capabilities                          client.Capabilities
	checkCtx                              context.Context
	cancelChecks                          context.CancelFunc
	completed                             chan protocol.Result
	active                                map[string]struct{}
	seen                                  map[string]time.Time
	pending                               []protocol.Result
	nextHeartbeat, nextLease, nextAttempt time.Time
	backoff                               time.Duration
}

func New(platform Client, checker Checker, disk Spool, cfg config.Config, clock Clock) (*Runner, error) {
	if platform == nil || checker == nil || disk == nil {
		return nil, errors.New("runner requires client, checker, and spool")
	}
	if cfg.MaxConcurrency == 0 {
		cfg.MaxConcurrency = 8
	}
	if cfg.MaxConcurrency < 1 || cfg.MaxConcurrency > 64 {
		return nil, errors.New("maxConcurrency must be between 1 and 64")
	}
	if !protocol.ValidID(cfg.ProbeID) {
		return nil, errors.New("runner probeId must be a UUID")
	}
	if clock == nil {
		clock = realClock{}
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Runner{client: platform, checker: checker, spool: disk, cfg: cfg, clock: clock, checkCtx: ctx, cancelChecks: cancel,
		completed: make(chan protocol.Result, cfg.MaxConcurrency), active: make(map[string]struct{}), seen: make(map[string]time.Time),
		capabilities: client.Capabilities{AgentVersion: "dev", IPv4: cfg.AllowIPv4, IPv6: cfg.AllowIPv6, MaxConcurrency: cfg.MaxConcurrency}}, nil
}

func (r *Runner) SetAgentVersion(version string) { r.capabilities.AgentVersion = version }

func (r *Runner) Run(ctx context.Context) (err error) {
	defer func() { err = errors.Join(err, r.shutdown()) }()
	for {
		if ctx.Err() != nil {
			return nil
		}
		if err := r.step(ctx); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		timer := r.clock.NewTimer(r.waitDuration())
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case result := <-r.completed:
			timer.Stop()
			r.accept(result)
		case <-timer.C():
		}
	}
}

func (r *Runner) accept(result protocol.Result) {
	delete(r.active, result.TaskID)
	r.pending = append(r.pending, result)
}

func (r *Runner) persist() error {
	for len(r.pending) > 0 {
		result := r.pending[0]
		payload, err := json.Marshal(result)
		if err != nil || len(payload) > resultReservation {
			return errors.New("check result exceeds reserved spool space")
		}
		if err := r.spool.Put(result); err != nil {
			return err
		}
		r.pending = r.pending[1:]
	}
	return nil
}

func (r *Runner) step(ctx context.Context) error {
	// Drain completed work before making any further network requests.
	for {
		select {
		case result := <-r.completed:
			r.accept(result)
		default:
			goto drained
		}
	}
drained:
	if err := r.persist(); err != nil && !errors.Is(err, spool.ErrFull) {
		return fmt.Errorf("persist check result: %w", err)
	}
	now := r.clock.Now()
	if now.Before(r.nextAttempt) {
		return nil
	}
	r.nextAttempt = time.Time{}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	batch, err := r.spool.Batch(100)
	if err != nil {
		return err
	}
	if len(batch) > 0 {
		acks, err := r.client.Submit(ctx, batch)
		if err != nil {
			return r.retry(err)
		}
		if err := r.acknowledge(batch, acks); err != nil {
			return err
		}
		if err := r.persist(); err != nil && !errors.Is(err, spool.ErrFull) {
			return err
		}
	}
	if !now.Before(r.nextHeartbeat) {
		if err := r.client.Heartbeat(ctx, r.capabilities); err != nil {
			return r.retry(err)
		}
		r.nextHeartbeat = r.clock.Now().Add(heartbeatInterval)
	}
	if !now.Before(r.nextLease) {
		r.nextLease = r.clock.Now().Add(idleInterval)
		if err := r.lease(ctx); err != nil {
			return err
		}
	}
	if !r.nextAttempt.IsZero() {
		return nil
	}
	r.backoff = 0
	return nil
}

func (r *Runner) acknowledge(batch []protocol.Result, acks []client.Ack) error {
	expected := make(map[string]bool, len(batch))
	for _, result := range batch {
		expected[result.TaskID] = true
	}
	if len(acks) != len(batch) {
		return errors.New("invalid acknowledgement count")
	}
	for _, ack := range acks {
		if !expected[ack.TaskID] {
			return errors.New("invalid acknowledgement identity")
		}
		delete(expected, ack.TaskID)
		switch ack.Status {
		case protocol.AckAccepted, protocol.AckDuplicate, protocol.AckStale, protocol.AckRejected:
		default:
			return errors.New("invalid acknowledgement status")
		}
	}
	for _, ack := range acks {
		var err error
		if ack.Status == protocol.AckRejected {
			err = r.spool.Reject(ack.TaskID, "platform_rejected")
		} else {
			err = r.spool.Ack(ack.TaskID)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *Runner) lease(ctx context.Context) error {
	now := r.clock.Now()
	for id, expiry := range r.seen {
		if !expiry.After(now) {
			delete(r.seen, id)
		}
	}
	items, bytes, err := r.spool.Available()
	if err != nil {
		return err
	}
	reserved := len(r.active) + len(r.pending)
	capacity := min(r.cfg.MaxConcurrency-reserved, items-reserved, int(bytes/resultReservation)-reserved, maxRememberedTasks-len(r.seen))
	if capacity <= 0 || len(r.pending) > 0 {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	response, err := r.client.Lease(ctx, capacity)
	if err != nil {
		r.nextLease = time.Time{}
		return r.retry(err)
	}
	if response.ServerTime.IsZero() || len(response.Tasks) > capacity || response.RetryAfterMS < 0 || response.RetryAfterMS > 3_600_000 {
		r.nextLease = time.Time{}
		return r.retry(errors.New("invalid lease response"))
	}
	r.nextLease = r.clock.Now().Add(max(idleInterval, time.Duration(response.RetryAfterMS)*time.Millisecond))
	for _, task := range response.Tasks {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if task.ProbeID != r.cfg.ProbeID || protocol.ValidateTask(task, response.ServerTime) != nil {
			continue
		}
		if _, exists := r.active[task.TaskID]; exists {
			continue
		}
		if _, exists := r.seen[task.TaskID]; exists {
			continue
		}
		stored, err := r.spool.Contains(task.TaskID)
		if err != nil {
			return err
		}
		if stored {
			continue
		}
		now = r.clock.Now()
		remaining := response.Remaining(task, now)
		if remaining <= 0 {
			continue
		}
		r.seen[task.TaskID] = now.Add(remaining)
		timeout := time.Duration(task.Config.TimeoutMS) * time.Millisecond
		if timeout <= 0 {
			timeout = 3 * time.Second
		}
		budget := min(remaining, timeout)
		// The checker validates a local deadline; preserve wire identity, translating
		// only the deadline from server time to a local monotonic instant.
		task.Deadline = now.Add(budget)
		r.active[task.TaskID] = struct{}{}
		checkCtx, cancel := context.WithDeadline(r.checkCtx, task.Deadline)
		go func(task protocol.Task) {
			defer cancel()
			result := r.checker.Check(checkCtx, task)
			result.Protocol = protocol.Version
			result.TaskID = task.TaskID
			result.LeaseID = task.LeaseID
			result.AddressVersion = task.AddressVersion
			result.ConfigVersion = task.ConfigVersion
			if len(result.ErrorCode) > 64 {
				result.ErrorCode = "checker_error"
			}
			r.completed <- result
		}(task)
	}
	return nil
}

// The client makes one request; this is the only retry loop. A failed upload
// pauses leasing, preserving its original task and lease identity on disk.
func (r *Runner) retry(err error) error {
	if errors.Is(err, client.ErrUnauthorized) {
		return client.ErrUnauthorized
	}
	if r.backoff == 0 {
		r.backoff = time.Second
	} else {
		r.backoff = min(60*time.Second, 2*r.backoff)
	}
	delay := time.Duration(float64(r.backoff) * (0.8 + rand.Float64()*0.2))
	var retry *client.RetryError
	if errors.As(err, &retry) {
		delay = max(delay, min(time.Hour, retry.After))
	}
	r.nextAttempt = r.clock.Now().Add(delay)
	return nil
}

func (r *Runner) waitDuration() time.Duration {
	now := r.clock.Now()
	if now.Before(r.nextAttempt) {
		return r.nextAttempt.Sub(now)
	}
	next := r.nextLease
	if r.nextHeartbeat.Before(next) {
		next = r.nextHeartbeat
	}
	return max(0, next.Sub(now))
}

func (r *Runner) shutdown() error {
	r.cancelChecks()
	timer := r.clock.NewTimer(shutdownTimeout)
	defer timer.Stop()
	for {
		err := r.persist()
		if len(r.active) == 0 {
			if err != nil {
				return fmt.Errorf("shutdown could not persist %d completed results: %w", len(r.pending), err)
			}
			return nil
		}
		select {
		case result := <-r.completed:
			r.accept(result)
		case <-timer.C():
			return fmt.Errorf("shutdown deadline: %d checks unfinished, %d results not durable", len(r.active), len(r.pending))
		}
	}
}
