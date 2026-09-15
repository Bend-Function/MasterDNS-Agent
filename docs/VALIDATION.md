# Validation record

This record is dated 2026-09-15 and covers protocol `probe-agent/v1`.

| Component | Reviewed snapshot |
| --- | --- |
| MasterDNS platform | `2d2a4b4606656b2c8f11462995ac8fc03f3a3d30` on `codex/multicloud-ip-rotation` |
| MasterDNS-Agent | A7 branch based on `5ec49f294b51ad66d47c18fc1a0db31e8335f2ba`; release binaries expose their exact commit through `version` |

Local validation covers strict protocol fixtures, client behavior, TCP and
HTTP/HTTPS checks, bounded spool and runner behavior, installer failure and
rollback paths, Go race tests and vet, and cross-compilation for all six release
targets. The exact commands and results are recorded in the A7 task report.

On this Darwin arm64 host, Go 1.27.1 completed `go test -race ./...` and
`go vet ./...` successfully. The real `tcp6` loopback listener test passed
without a skip. `scripts/test-install.sh` passed all temp-root install, update,
rollback, preservation, and purge cases. All six cross-build outputs matched
their generated `SHA256SUMS`; only the Darwin arm64 output was run natively.

The Darwin arm64 development host can run native macOS arm64 tests. It cannot
provide native Windows, macOS amd64, or Linux service acceptance. The checked-in
CI workflow assigns native runners for both architectures of Linux, macOS, and
Windows; those jobs have not been executed by this development task. Each Linux
amd64 and arm64 job invokes the installer against real system paths, enrolls the
installed binary over TLS, starts the downloaded unit with systemd, and requires
successful TCP probes to reserved documentation IPv4 and IPv6 addresses assigned
to loopback. Failure to configure or probe either family fails that job; it is
not recorded as a skip.

Full cross-repository P7/P9/P10 registration, probe, aggregation, and rotation
integration remains pending. P12b will update the shared validation record with
the final platform and agent commit IDs. Cross-compilation is build evidence
only and is never represented as a native runtime result.

The controller's P12a binary integration used platform test commit
`0da881dabf4dae75c088a74ec00c2afed8463472` (product base `2d2a4b4`, integrated
as `2d566f6`) and an Agent binary built from
`5ec49f294b51ad66d47c18fc1a0db31e8335f2ba` with version
`p12-test-5ec49f2`. It passed the actual Nest/P5 API protocol, IPv4 and IPv6 TCP,
verified HTTPS, and revocation behavior. It did not exercise the pending
P7/P9/P10 full rotation chain.
