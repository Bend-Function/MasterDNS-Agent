package checker

import (
	"context"
	"net"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/Bend-Function/MasterDNS-Agent/internal/protocol"
)

func TestTCP4ConnectsToExplicitAddressAndPort(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	dialer := redirectDialer{target: listener.Addr().String()}
	checker := mustChecker(t, true, false, dialer.DialContext)
	got := checker.Check(context.Background(), tcpTask("192.0.2.10", 4, listener.Addr().(*net.TCPAddr).Port))
	if got.Outcome != protocol.OutcomeSuccess || got.LatencyMS < 0 {
		t.Fatalf("Check() = %#v", got)
	}
	if dialer.network != "tcp4" || dialer.address != net.JoinHostPort("192.0.2.10", portString(listener)) {
		t.Fatalf("dialed %q %q", dialer.network, dialer.address)
	}
}

func TestTCP6ConnectsToExplicitAddressAndPort(t *testing.T) {
	listener, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skipf("IPv6 listener unavailable: %v", err)
	}
	defer listener.Close()

	dialer := redirectDialer{target: listener.Addr().String()}
	checker := mustChecker(t, false, true, dialer.DialContext)
	got := checker.Check(context.Background(), tcpTask("2001:db8::10", 6, listener.Addr().(*net.TCPAddr).Port))
	if got.Outcome != protocol.OutcomeSuccess {
		t.Fatalf("Check() = %#v", got)
	}
	if dialer.network != "tcp6" || dialer.address != net.JoinHostPort("2001:db8::10", portString(listener)) {
		t.Fatalf("dialed %q %q", dialer.network, dialer.address)
	}
}

func TestNoIPv6IsUnavailable(t *testing.T) {
	checker := mustChecker(t, true, false, nil)
	got := checker.Check(context.Background(), tcpTask("2001:db8::10", 6, 443))
	if got.Outcome != protocol.OutcomeUnavailable || got.ErrorCode != "ipv6_unavailable" {
		t.Fatalf("Check() = %#v", got)
	}
}

func TestNoIPv4IsUnavailable(t *testing.T) {
	checker := mustChecker(t, false, true, nil)
	got := checker.Check(context.Background(), tcpTask("192.0.2.10", 4, 443))
	if got.Outcome != protocol.OutcomeUnavailable || got.ErrorCode != "ipv4_unavailable" {
		t.Fatalf("Check() = %#v", got)
	}
}

func TestTCPRefusalIsFailure(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	target := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	checker := mustChecker(t, true, false, (&redirectDialer{target: target}).DialContext)
	got := checker.Check(context.Background(), tcpTask("192.0.2.10", 4, 443))
	if got.Outcome != protocol.OutcomeFailure || got.ErrorCode != "tcp_failed" {
		t.Fatalf("Check() = %#v", got)
	}
}

func TestReportedLatencyIsCappedAtProtocolMaximum(t *testing.T) {
	if got := latencyMilliseconds(60*time.Second + time.Millisecond); got != 60000 {
		t.Fatalf("latencyMilliseconds() = %f", got)
	}
}

func TestTCPTimeoutIsFailure(t *testing.T) {
	checker := mustChecker(t, true, false, func(ctx context.Context, _, _ string) (net.Conn, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	task := tcpTask("192.0.2.10", 4, 443)
	task.Config.TimeoutMS = 100
	got := checker.Check(context.Background(), task)
	if got.Outcome != protocol.OutcomeFailure || got.ErrorCode != "tcp_failed" {
		t.Fatalf("Check() = %#v", got)
	}
	if got.LatencyMS < 90 || got.LatencyMS > 500 {
		t.Fatalf("latencyMs = %f", got.LatencyMS)
	}
}

func TestLocalRoutingFailureIsUnavailable(t *testing.T) {
	for _, err := range []error{syscall.ENETUNREACH, syscall.EHOSTUNREACH, syscall.EAFNOSUPPORT, syscall.EADDRNOTAVAIL} {
		checker := mustChecker(t, true, false, func(context.Context, string, string) (net.Conn, error) { return nil, err })
		got := checker.Check(context.Background(), tcpTask("192.0.2.10", 4, 443))
		if got.Outcome != protocol.OutcomeUnavailable || got.ErrorCode != "network_unavailable" {
			t.Fatalf("error %v: Check() = %#v", err, got)
		}
	}
}

func TestCheckerEnforcesTaskAndLocalPrivateAllowLists(t *testing.T) {
	called := false
	checker := mustCheckerWithCIDRs(t, true, false, []string{"10.0.0.0/8"}, func(context.Context, string, string) (net.Conn, error) {
		called = true
		return nil, syscall.ECONNREFUSED
	})
	task := tcpTask("10.1.2.3", 4, 443)
	got := checker.Check(context.Background(), task)
	if got.Outcome != protocol.OutcomeUnavailable || called {
		t.Fatalf("unapproved Check() = %#v, dial called=%v", got, called)
	}
	task.NetworkPolicy = &protocol.NetworkPolicy{AllowedPrivateCIDRs: []string{"10.1.0.0/16"}}
	got = checker.Check(context.Background(), task)
	if got.Outcome != protocol.OutcomeFailure || !called {
		t.Fatalf("approved Check() = %#v, dial called=%v", got, called)
	}
}

type redirectDialer struct {
	target  string
	network string
	address string
}

func (d *redirectDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	d.network, d.address = network, address
	return (&net.Dialer{}).DialContext(ctx, network, d.target)
}

func mustChecker(t *testing.T, ipv4, ipv6 bool, dial DialContextFunc) *Checker {
	t.Helper()
	return mustCheckerWithCIDRs(t, ipv4, ipv6, nil, dial)
}

func mustCheckerWithCIDRs(t *testing.T, ipv4, ipv6 bool, cidrs []string, dial DialContextFunc) *Checker {
	t.Helper()
	checker, err := New(ipv4, ipv6, cidrs, dial)
	if err != nil {
		t.Fatal(err)
	}
	return checker
}

func tcpTask(address string, family, port int) protocol.Task {
	return protocol.Task{
		Protocol: protocol.Version,
		TaskID:   "11111111-1111-4111-8111-111111111111", RoundID: "22222222-2222-4222-8222-222222222222",
		ProbeID: "33333333-3333-4333-8333-333333333333", LeaseID: "44444444-4444-4444-8444-444444444444",
		AddressVersion: 1, ConfigVersion: 1, Address: address, Family: family,
		Config: protocol.CheckConfig{Type: "tcp", Port: port, TimeoutMS: 1000}, Deadline: time.Now().Add(time.Minute),
	}
}

func portString(listener net.Listener) string {
	return strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
}
