# Probe Agent Protocol v1

`probe-agent/v1` is the versioned HTTPS contract between MasterDNS and an external probe agent. JSON request and response objects are strict: clients and servers reject unknown fields. IDs are UUIDs, timestamps are RFC 3339 timestamps with an offset, versions are positive integers, and durations are milliseconds.

The canonical examples are [`fixtures/probe-task-v1.json`](fixtures/probe-task-v1.json) and [`fixtures/probe-result-v1.json`](fixtures/probe-result-v1.json). Their fixed timestamps are test data only; production and integration tests must inject a clock rather than treat them as current. The machine-readable task and result schema is [`probe-agent-v1.schema.json`](probe-agent-v1.schema.json).

## Authentication and errors

Except for token exchange, requests use the probe's runtime bearer token. A runtime token identifies one probe and cannot read another probe's tasks or access cloud and DNS credentials.

- `401 Unauthorized`: the token is invalid, expired, or revoked. The agent stops leasing work until it is registered again.
- `409 Conflict`: the protocol, task version, address version, config version, or lease is stale or incompatible.
- `429 Too Many Requests`: the agent waits for the HTTP `Retry-After` value before retrying.

Retries use the same task and lease identifiers. The server decides freshness from its own clock and the lease deadline; `measuredAt` is supporting observation data.

## Install token exchange

`POST /api/v1/probe-agent/exchange`

Request: `{ "installToken": string }`

Response: `{ "probeId": UUID, "runtimeToken": string, "protocol": "probe-agent/v1" }`

An install token is single use and time limited. The returned runtime token is shown only in this response.

## Heartbeat

`POST /api/v1/probe-agent/heartbeat`

Request:

```json
{
  "protocol": "probe-agent/v1",
  "agentVersion": "1.0.0",
  "capabilities": { "ipv4": true, "ipv6": true },
  "maxConcurrency": 8
}
```

The capabilities describe usable local address families. Lack of an address family produces an `unavailable` result; it is not a target failure.

## Lease tasks

`POST /api/v1/probe-agent/tasks/lease`

Request: `{ "protocol": "probe-agent/v1", "capacity": 8 }`

Response: `{ "serverTime": RFC3339, "tasks": ProbeTask[], "retryAfterMs": number }`

`capacity` and the returned task batch are limited to 100. A task carries immutable task, round, probe, lease, address, and configuration versions. Agents must not execute work after `deadline`.

Targets are public by default. An RFC 1918, carrier-grade NAT, or IPv6 unique-local target is valid only when `networkPolicy.allowedPrivateCIDRs` is nonempty and contains that address. Unspecified, loopback, link-local (including cloud metadata), multicast, IPv4 future-use, and IPv4-mapped IPv6 targets are always rejected, even when an allowlist contains them. This policy is produced from an administrator-authorized probe configuration. Lease requests never accept a network policy, so an agent cannot expand its own target range. Agents also enforce their local network policy.

## Submit results

`POST /api/v1/probe-agent/results`

Request: `{ "protocol": "probe-agent/v1", "results": ProbeResult[] }`

The batch contains 1 to 100 results. Each response item is `{ "taskId": UUID, "status": "accepted" | "duplicate" | "stale" | "rejected" }`. A duplicate is an idempotent acknowledgement. Stale and rejected observations may be retained for diagnostics but cannot update health state.

`outcome` is `success`, `failure`, or `unavailable`. `latencyMs` is finite and between 0 and 60000. `errorCode` is at most 128 characters. A result must retain the task's ID, lease ID, address version, and configuration version.

## Address-family behavior

The declared `family` must match the literal target address. Agents connect directly to `address` using that family and never fall back to another family. For HTTP and HTTPS, `hostname` supplies HTTP Host and TLS SNI while the connection still targets `address`; IPv6 URL literals use brackets.
