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
  "probeId": "33333333-3333-4333-8333-333333333333",
  "tokenFile": "/etc/masterdns-agent/token",
  "stateDir": "/var/lib/masterdns-agent",
  "maxConcurrency": 8,
  "allowIpv4": true,
  "allowIpv6": true,
  "allowedPrivateCidrs": []
}
```

`serverUrl` must use HTTPS. `maxConcurrency` is limited to 100. Private targets
must be authorized by both `allowedPrivateCidrs` here and the leased task's
`networkPolicy`; loopback, link-local, multicast, future-use IPv4, and
IPv4-mapped IPv6 targets remain forbidden.

On Unix, the runtime token must be a regular file with no group or other access
(for example, mode `0600`). On Windows, keep it under the current user's profile
and restrict its ACL to that user. Do not put tokens in the configuration file,
URL, command line, or logs.

The `version` command prints the build version:

```sh
masterdns-agent version
```

The versioned wire contract and fixtures are in [`protocol/v1`](protocol/v1).
