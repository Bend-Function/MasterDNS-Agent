package checker

import (
	"net/netip"
	"testing"
)

func TestNetworkPolicyRequiresLocalAndTaskPrivateAuthorization(t *testing.T) {
	policy, err := NewNetworkPolicy([]string{"10.0.0.0/8"})
	if err != nil {
		t.Fatal(err)
	}
	address := netip.MustParseAddr("10.1.2.3")
	if err := policy.Validate(address, false); err == nil {
		t.Fatal("accepted private address without task authorization")
	}
	if err := policy.Validate(address, true); err != nil {
		t.Fatalf("rejected jointly authorized private address: %v", err)
	}

	other, err := NewNetworkPolicy([]string{"192.168.0.0/16"})
	if err != nil {
		t.Fatal(err)
	}
	if err := other.Validate(address, true); err == nil {
		t.Fatal("accepted private address outside local allowlist")
	}
}

func TestNetworkPolicyAlwaysRejectsForbiddenAddresses(t *testing.T) {
	policy, err := NewNetworkPolicy([]string{"0.0.0.0/0", "::/0"})
	if err != nil {
		t.Fatal(err)
	}
	tests := []string{
		"::ffff:192.0.2.1",
		"127.0.0.1",
		"::1",
		"169.254.1.1",
		"fe80::1",
		"224.0.0.1",
		"ff02::1",
		"100.100.100.200",
		"fd00:ec2::254",
	}
	for _, raw := range tests {
		t.Run(raw, func(t *testing.T) {
			if err := policy.Validate(netip.MustParseAddr(raw), true); err == nil {
				t.Fatalf("accepted permanently forbidden address %s", raw)
			}
		})
	}
}

func TestNetworkPolicyRejectsInvalidLocalCIDR(t *testing.T) {
	if _, err := NewNetworkPolicy([]string{"10.1.0.0/8"}); err == nil {
		t.Fatal("accepted unmasked local CIDR")
	}
}
