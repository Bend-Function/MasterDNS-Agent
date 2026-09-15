package checker

import (
	"context"
	"crypto/x509"
	"net"
	"net/netip"
	"time"

	"github.com/Bend-Function/MasterDNS-Agent/internal/protocol"
)

type DialContextFunc func(context.Context, string, string) (net.Conn, error)

type Checker struct {
	allowIPv4 bool
	allowIPv6 bool
	policy    NetworkPolicy
	dial      DialContextFunc
	rootCAs   *x509.CertPool
}

func New(allowIPv4, allowIPv6 bool, allowedPrivateCIDRs []string, dial DialContextFunc) (*Checker, error) {
	policy, err := NewNetworkPolicy(allowedPrivateCIDRs)
	if err != nil {
		return nil, err
	}
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	return &Checker{allowIPv4: allowIPv4, allowIPv6: allowIPv6, policy: policy, dial: dial}, nil
}

func (c *Checker) Check(ctx context.Context, task protocol.Task) protocol.Result {
	measuredAt := time.Now().UTC()
	result := protocol.Result{
		Protocol: protocol.Version, TaskID: task.TaskID, LeaseID: task.LeaseID,
		AddressVersion: task.AddressVersion, ConfigVersion: task.ConfigVersion,
		Outcome: protocol.OutcomeUnavailable, MeasuredAt: measuredAt,
	}
	if err := protocol.ValidateTask(task, measuredAt); err != nil {
		result.ErrorCode = "invalid_task"
		return result
	}
	if task.Config.Type != "tcp" && task.Config.Type != "http" {
		result.ErrorCode = "unsupported_check"
		return result
	}
	if task.Family == 4 && !c.allowIPv4 {
		result.ErrorCode = "ipv4_unavailable"
		return result
	}
	if task.Family == 6 && !c.allowIPv6 {
		result.ErrorCode = "ipv6_unavailable"
		return result
	}
	address, _ := netip.ParseAddr(task.Address)
	if err := c.policy.Validate(address, taskAllowsPrivate(task, address)); err != nil {
		result.ErrorCode = "target_forbidden"
		return result
	}
	if task.Config.Type == "http" {
		return c.checkHTTP(ctx, task, result, measuredAt)
	}
	return c.checkTCP(ctx, task, result, measuredAt)
}
