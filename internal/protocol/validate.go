package protocol

import (
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strings"
	"time"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

var (
	carrierGradeNAT = netip.MustParsePrefix("100.64.0.0/10")
	futureIPv4      = netip.MustParsePrefix("240.0.0.0/4")
)

func ValidateTask(task Task, now time.Time) error {
	if task.Protocol != Version {
		return fmt.Errorf("unsupported protocol %q", task.Protocol)
	}
	for name, value := range map[string]string{"taskId": task.TaskID, "roundId": task.RoundID, "probeId": task.ProbeID, "leaseId": task.LeaseID} {
		if !uuidPattern.MatchString(value) {
			return fmt.Errorf("%s must be a UUID", name)
		}
	}
	if task.AddressVersion < 1 || task.ConfigVersion < 1 {
		return errors.New("addressVersion and configVersion must be positive")
	}
	if task.Deadline.IsZero() || !task.Deadline.After(now) {
		return errors.New("task deadline has expired")
	}
	addr, err := netip.ParseAddr(task.Address)
	if err != nil {
		return fmt.Errorf("address must be an IP literal: %w", err)
	}
	if addr.Is4In6() {
		return errors.New("IPv4-mapped IPv6 targets are forbidden")
	}
	if task.Family != 4 && task.Family != 6 {
		return errors.New("family must be 4 or 6")
	}
	if (task.Family == 4) != addr.Is4() {
		return errors.New("family does not match address")
	}
	if err := validateNetworkPolicy(task.NetworkPolicy); err != nil {
		return err
	}
	if permanentlyForbidden(addr) {
		return errors.New("target address is permanently forbidden")
	}
	if privateTarget(addr) && !policyAllows(task.NetworkPolicy, addr) {
		return errors.New("private target is not authorized by networkPolicy")
	}
	return validateCheck(task)
}

func validateNetworkPolicy(policy *NetworkPolicy) error {
	if policy == nil {
		return nil
	}
	if len(policy.AllowedPrivateCIDRs) < 1 || len(policy.AllowedPrivateCIDRs) > 64 {
		return errors.New("networkPolicy must contain between 1 and 64 CIDRs")
	}
	for _, raw := range policy.AllowedPrivateCIDRs {
		prefix, err := netip.ParsePrefix(raw)
		if err != nil || prefix != prefix.Masked() {
			return fmt.Errorf("networkPolicy contains invalid CIDR %q", raw)
		}
	}
	return nil
}

func permanentlyForbidden(addr netip.Addr) bool {
	return addr.IsUnspecified() || addr.IsLoopback() || addr.IsLinkLocalUnicast() ||
		addr.IsLinkLocalMulticast() || addr.IsMulticast() || !addr.IsGlobalUnicast() ||
		(addr.Is4() && futureIPv4.Contains(addr))
}

func privateTarget(addr netip.Addr) bool {
	return addr.IsPrivate() || (addr.Is4() && carrierGradeNAT.Contains(addr))
}

func policyAllows(policy *NetworkPolicy, addr netip.Addr) bool {
	if policy == nil || len(policy.AllowedPrivateCIDRs) == 0 {
		return false
	}
	for _, raw := range policy.AllowedPrivateCIDRs {
		prefix, err := netip.ParsePrefix(raw)
		if err == nil && prefix == prefix.Masked() && prefix.Contains(addr) {
			return true
		}
	}
	return false
}

func validateCheck(task Task) error {
	c := task.Config
	if c.TimeoutMS != 0 && (c.TimeoutMS < 100 || c.TimeoutMS > 60000) {
		return errors.New("timeoutMs must be between 100 and 60000")
	}
	switch c.Type {
	case "tcp":
		if c.Port < 1 || c.Port > 65535 {
			return errors.New("TCP check port must be between 1 and 65535")
		}
		if c.Protocol != "" || c.Method != "" || c.Path != "" {
			return errors.New("TCP check contains HTTP-only fields")
		}
		return nil
	case "http":
		if c.Port < 0 || c.Port > 65535 {
			return errors.New("HTTP check port must be between 1 and 65535 when set")
		}
		if c.Protocol != "" && c.Protocol != "http" && c.Protocol != "https" {
			return errors.New("HTTP check protocol must be http or https")
		}
		if c.Method != "" && c.Method != "GET" && c.Method != "HEAD" {
			return errors.New("HTTP check method must be GET or HEAD")
		}
		if c.Path != "" && !strings.HasPrefix(c.Path, "/") {
			return errors.New("HTTP check path must begin with /")
		}
		hostname := c.Hostname
		if hostname == "" {
			hostname = task.Hostname
		}
		if hostname == "" || len(hostname) > 255 {
			return errors.New("HTTP check hostname is required and must be at most 255 characters")
		}
		for _, status := range c.ExpectedStatuses {
			if status < 100 || status > 599 {
				return errors.New("expectedStatuses must contain HTTP status codes")
			}
		}
		if (c.ExpectedStatusMin != 0 && (c.ExpectedStatusMin < 100 || c.ExpectedStatusMin > 599)) ||
			(c.ExpectedStatusMax != 0 && (c.ExpectedStatusMax < 100 || c.ExpectedStatusMax > 599)) ||
			(c.ExpectedStatusMin != 0 && c.ExpectedStatusMax != 0 && c.ExpectedStatusMin > c.ExpectedStatusMax) {
			return errors.New("invalid expected status range")
		}
		if len(c.BodyContains) > 2048 || len(c.BodyPattern) > 2048 {
			return errors.New("body matcher exceeds 2048 characters")
		}
		if c.BodyPattern != "" {
			if _, err := regexp.Compile(c.BodyPattern); err != nil {
				return fmt.Errorf("invalid bodyPattern: %w", err)
			}
		}
		return nil
	default:
		return fmt.Errorf("unsupported check type %q", c.Type)
	}
}
