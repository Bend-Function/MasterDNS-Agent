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
Windows; those jobs have not been executed by this development task. Linux
systemd unit validation is configured on native amd64 and arm64 runners. An IPv6
test may report skipped when its native runner has no usable IPv6 loopback or
route; a skip is not recorded as IPv6 acceptance.

Full cross-repository P7/P9/P10 registration, probe, aggregation, and rotation
integration remains pending. P12b will update the shared validation record with
the final platform and agent commit IDs. Cross-compilation is build evidence
only and is never represented as a native runtime result.
