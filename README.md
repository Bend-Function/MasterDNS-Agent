# MasterDNS-Agent

External TCP and HTTP/HTTPS probe agent for MasterDNS. The agent makes outbound
HTTPS requests to lease work and submit results; it does not listen for inbound
connections or hold cloud and DNS credentials.

## Build

Go 1.26 or newer is required.

```sh
go test ./...
CGO_ENABLED=0 go build ./cmd/masterdns-agent
```

## Configuration

Run the agent with an explicit JSON configuration file:

```sh
masterdns-agent run --config /etc/masterdns-agent/config.json
```

```json
{
  "serverUrl": "https://masterdns.example",
  "caFile": "/etc/masterdns-agent/private-ca.pem",
  "probeId": "33333333-3333-4333-8333-333333333333",
  "tokenFile": "/etc/masterdns-agent/token",
  "stateDir": "/var/lib/masterdns-agent",
  "maxConcurrency": 8,
  "allowIpv4": true,
  "allowIpv6": true,
  "allowedPrivateCidrs": []
}
```

`serverUrl` must use HTTPS. The optional `caFile` appends a private PEM CA to
the system trust roots; platform TLS verification is always enabled.
`maxConcurrency` defaults to 8 when omitted and must be between 1 and 64. Private targets
must be authorized by both `allowedPrivateCidrs` here and the leased task's
`networkPolicy`; loopback, link-local, multicast, future-use IPv4, and
IPv4-mapped IPv6 targets remain forbidden.

On Unix, the runtime token must be a regular file with no group or other access
(for example, mode `0600`). On Windows, keep it under the current user's profile
and restrict its ACL to that user. Do not put tokens in the configuration file,
URL, command line, or logs.

Before the first run, exchange a single-use install token from standard input or
a protected file. Enrollment writes the runtime token with mode `0600` and adds
the issued probe ID to the configuration atomically:

```sh
masterdns-agent enroll --config /etc/masterdns-agent/config.json
masterdns-agent enroll --config /etc/masterdns-agent/config.json \
  --install-token-file /etc/masterdns-agent/install-token
```

`run` stays in the foreground, sends heartbeats every 30 seconds, and polls
for tasks every 2 seconds unless the platform requests a longer delay. Failed
uploads retry with exponential backoff and jitter without repeating checks.
The durable spool is limited to 1,000 results and 10 MiB; disk reservations and
upload failures pause new leases. Only one process can use a state directory.

SIGINT or SIGTERM stops leasing and cancels active checks, allowing up to 5 seconds
to persist their results. Unsent results are uploaded on the next run. Exit code
`0` means a clean stop, `1` means configuration, startup, or persistence failure,
and `3` means platform authentication was rejected and enrollment is required.
A storage failure that prevents durable shutdown is reported as an error.

The `version` command prints the build version:

```sh
masterdns-agent version
```

The versioned wire contract and fixtures are in [`protocol/v1`](protocol/v1).
