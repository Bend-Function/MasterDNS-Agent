package checker

import (
	"context"
	"errors"
	"net"
	"strconv"
	"syscall"
	"time"

	"github.com/Bend-Function/MasterDNS-Agent/internal/protocol"
)

func (c *Checker) checkTCP(ctx context.Context, task protocol.Task, result protocol.Result, started time.Time) protocol.Result {
	timeout := time.Duration(task.Config.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	deadline := started.Add(timeout)
	if task.Deadline.Before(deadline) {
		deadline = task.Deadline
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	network := "tcp4"
	if task.Family == 6 {
		network = "tcp6"
	}
	target := net.JoinHostPort(task.Address, strconv.Itoa(task.Config.Port))
	connection, err := c.dial(ctx, network, target)
	result.LatencyMS = latencyMilliseconds(time.Since(started))
	if err == nil {
		_ = connection.Close()
		result.Outcome = protocol.OutcomeSuccess
		return result
	}
	if localNetworkUnavailable(err) {
		result.Outcome = protocol.OutcomeUnavailable
		result.ErrorCode = "network_unavailable"
		return result
	}
	result.Outcome = protocol.OutcomeFailure
	result.ErrorCode = "tcp_failed"
	return result
}

func latencyMilliseconds(elapsed time.Duration) float64 {
	const maximum = 60_000
	latency := float64(elapsed) / float64(time.Millisecond)
	if latency > maximum {
		return maximum
	}
	return latency
}

func localNetworkUnavailable(err error) bool {
	return errors.Is(err, syscall.ENETUNREACH) || errors.Is(err, syscall.EHOSTUNREACH) ||
		errors.Is(err, syscall.EAFNOSUPPORT) || errors.Is(err, syscall.EADDRNOTAVAIL)
}
