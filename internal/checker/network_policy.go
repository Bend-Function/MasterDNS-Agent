package checker

import (
	"errors"
	"fmt"
	"net/netip"

	"github.com/Bend-Function/MasterDNS-Agent/internal/protocol"
)

var (
	carrierGradeNAT = netip.MustParsePrefix("100.64.0.0/10")
	futureIPv4      = netip.MustParsePrefix("240.0.0.0/4")
	alibabaMetadata = netip.MustParseAddr("100.100.100.200")
	awsIPv6Metadata = netip.MustParseAddr("fd00:ec2::254")
)

type NetworkPolicy struct {
	allowedPrivate []netip.Prefix
}

func NewNetworkPolicy(cidrs []string) (NetworkPolicy, error) {
	policy := NetworkPolicy{allowedPrivate: make([]netip.Prefix, 0, len(cidrs))}
	for _, raw := range cidrs {
		prefix, err := netip.ParsePrefix(raw)
		if err != nil || prefix != prefix.Masked() {
			return NetworkPolicy{}, fmt.Errorf("invalid local private CIDR %q", raw)
		}
		policy.allowedPrivate = append(policy.allowedPrivate, prefix)
	}
	return policy, nil
}

func (p NetworkPolicy) Validate(address netip.Addr, taskAllowsPrivate bool) error {
	if !address.IsValid() || permanentlyForbidden(address) {
		return errors.New("target address is permanently forbidden")
	}
	if !privateTarget(address) {
		return nil
	}
	if !taskAllowsPrivate {
		return errors.New("private target is not authorized by task")
	}
	for _, prefix := range p.allowedPrivate {
		if prefix.Contains(address) {
			return nil
		}
	}
	return errors.New("private target is not authorized by local policy")
}

func taskAllowsPrivate(task protocol.Task, address netip.Addr) bool {
	if task.NetworkPolicy == nil {
		return false
	}
	for _, raw := range task.NetworkPolicy.AllowedPrivateCIDRs {
		prefix, err := netip.ParsePrefix(raw)
		if err == nil && prefix.Contains(address) {
			return true
		}
	}
	return false
}

func privateTarget(address netip.Addr) bool {
	return address.IsPrivate() || (address.Is4() && carrierGradeNAT.Contains(address))
}

func permanentlyForbidden(address netip.Addr) bool {
	if address.Is4In6() || address.IsUnspecified() || address.IsLoopback() ||
		address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() ||
		address.IsMulticast() || !address.IsGlobalUnicast() {
		return true
	}
	return address == alibabaMetadata || address == awsIPv6Metadata ||
		(address.Is4() && futureIPv4.Contains(address))
}
