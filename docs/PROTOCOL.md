# Agent protocol and compatibility

MasterDNS-Agent implements `probe-agent/v1`. All platform calls are outbound
HTTPS POST requests. Authenticated calls use the runtime token in the
`Authorization: Bearer` header; tokens never appear in request URLs.

## Enrollment

`POST /api/v1/probe-agent/exchange`

```json
{"installToken":"<single-use-install-token>"}
```

```json
{
  "probeId":"33333333-3333-4333-8333-333333333333",
  "runtimeToken":"<probe-runtime-token>",
  "protocol":"probe-agent/v1"
}
```

The install token is single use. The agent atomically writes the runtime token
to its protected token file and writes the assigned probe ID to the config.

## Heartbeat

`POST /api/v1/probe-agent/heartbeat`

```json
{
  "protocol":"probe-agent/v1",
  "agentVersion":"v1.2.3",
  "capabilities":{"ipv4":true,"ipv6":true},
  "maxConcurrency":8
}
```

```json
{}
```

## Lease

`POST /api/v1/probe-agent/tasks/lease`

```json
{"protocol":"probe-agent/v1","capacity":8}
```

```json
{
  "serverTime":"2026-09-15T11:59:55Z",
  "tasks":[{
    "protocol":"probe-agent/v1",
    "taskId":"11111111-1111-4111-8111-111111111111",
    "roundId":"22222222-2222-4222-8222-222222222222",
    "probeId":"33333333-3333-4333-8333-333333333333",
    "leaseId":"44444444-4444-4444-8444-444444444444",
    "addressVersion":1,
    "configVersion":1,
    "address":"192.0.2.10",
    "family":4,
    "hostname":"service.example.com",
    "config":{"type":"tcp","port":443,"timeoutMs":3000},
    "deadline":"2026-09-15T12:00:00Z"
  }],
  "retryAfterMs":2000
}
```

The address is always a literal IPv4 or IPv6 address. HTTP Host and TLS SNI use
the configured hostname while the socket connects directly to that address.
An expired lease is not executed. A missing local address family produces an
`unavailable` result.

## Result submission

`POST /api/v1/probe-agent/results`

```json
{
  "protocol":"probe-agent/v1",
  "results":[{
    "protocol":"probe-agent/v1",
    "taskId":"11111111-1111-4111-8111-111111111111",
    "leaseId":"44444444-4444-4444-8444-444444444444",
    "addressVersion":1,
    "configVersion":1,
    "outcome":"success",
    "latencyMs":42.5,
    "measuredAt":"2026-09-15T11:59:58Z",
    "statusCode":200
  }]
}
```

```json
{
  "results":[{
    "taskId":"11111111-1111-4111-8111-111111111111",
    "status":"accepted"
  }]
}
```

Retries retain the original task ID, lease ID, address version, and config
version. `accepted`, `duplicate`, and `stale` acknowledgements remove a buffered
result. A rejected result is quarantined. The durable buffer is bounded to 1,000
results and 10 MiB; pressure pauses new leasing rather than deleting observations
or repeating probes.

## Compatibility and check behavior

Payloads use strict JSON decoding and reject unknown fields. A different
protocol string, stale immutable version, invalid identifier, or invalid field
combination is incompatible. Update both platform and agent implementations and
their fixtures together before using a new protocol version.

TCP and HTTP/HTTPS GET or HEAD are supported. Body patterns use Go's RE2 syntax,
so lookbehind and backreferences are not supported. Target HTTP redirects are
limited to five and may retain only the original scheme, hostname, and port;
every redirected request still connects to the leased literal IP. Platform API
redirects are rejected entirely.

HTTP 401 is terminal and requires enrollment. HTTP 429 honors `Retry-After`.
Network and 5xx errors use bounded exponential backoff. Disconnection never
causes local address changes or an unbounded probe loop.

The canonical schema, fixtures, and detailed field constraints are in
[`protocol/v1`](../protocol/v1/).
