# Task 3 Report: Dual-Stack TCP and Network Policy

## Outcome

Implemented A3 on `codex/external-health-agent`, starting from `70534d5`.

- Added explicit `tcp4` and `tcp6` dialing to IP literals built with
  `net.JoinHostPort`; targets are never resolved through DNS.
- Added bounded checks using the earlier of the task deadline and TCP timeout.
- Capped reported latency at the protocol schema's 60,000 ms maximum.
- Classified a disabled family and local route/family errors as `unavailable`.
  Connection refusal, timeout, and other reachable-family target errors are
  `failure`.
- Added local private CIDR enforcement. Private targets must be contained by
  both the task's `networkPolicy.allowedPrivateCIDRs` and the local list.
- Retained the protocol validator as the first check and independently denied
  mapped IPv6, loopback, link-local, multicast, non-global, future IPv4, and
  the two metadata addresses at the local policy boundary.
- Added real local IPv4 and IPv6 listeners behind an injected redirect dialer.
  The tasks still carry public IP literals, so production loopback policy is
  never bypassed.

## Interfaces

```go
type DialContextFunc func(context.Context, string, string) (net.Conn, error)

func New(allowIPv4, allowIPv6 bool, allowedPrivateCIDRs []string, dial DialContextFunc) (*Checker, error)
func (c *Checker) Check(ctx context.Context, task protocol.Task) protocol.Result

func NewNetworkPolicy(cidrs []string) (NetworkPolicy, error)
func (p NetworkPolicy) Validate(address netip.Addr, taskAllowsPrivate bool) error
```

Passing `nil` as the dial function selects `net.Dialer.DialContext`.

Result codes introduced by the checker are `invalid_task`,
`unsupported_check`, `ipv4_unavailable`, `ipv6_unavailable`,
`target_forbidden`, `network_unavailable`, and `tcp_failed`.

## TDD Evidence

`go test ./internal/checker` initially failed because `Checker`,
`DialContextFunc`, `New`, and `NewNetworkPolicy` did not exist. After the first
green implementation, the timeout test was strengthened to wait for the actual
context deadline and verify the configured timeout budget. A later latency
bound test failed because the cap helper did not exist, then passed after the
wire value was bounded to the protocol maximum.

No protocol snapshot update was needed: the existing v1 task type, schema, and
validator already include `networkPolicy.allowedPrivateCIDRs`, reject a missing
private-target allowance, and permanently deny mapped IPv6 and metadata
targets.

## Verification

All Go commands used `/private/tmp/masterdns-go-toolchain/go/bin/go` with
`GOCACHE=/private/tmp/masterdns-go-cache` and `GOTELEMETRY=off`.

- `go test -v ./internal/checker`: pass, including the real `::1` listener test
  with no skips.
- `go test -race ./...`: pass for all packages.
- `go vet ./...`: pass.
- `CGO_ENABLED=0 go build -o /private/tmp/masterdns-agent-a3
  ./cmd/masterdns-agent` with `GOPATH=/private/tmp/masterdns-go-path`: pass.
- Windows amd64 checker test cross-compile: pass.
- Linux arm64 checker test cross-compile: pass.
- `git diff --check`: pass.

No AWS or DNS operation was added or performed.
