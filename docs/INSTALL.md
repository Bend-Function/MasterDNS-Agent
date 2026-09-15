# Installing MasterDNS-Agent

Release `VERSION` below means an explicit tag such as `v1.2.3`. The installer
does not resolve `latest`; pinning makes rollouts and rollback repeatable.

## Linux service installation

Download `install.sh` from the chosen release, inspect it, then install the
matching amd64 or arm64 binary:

```sh
VERSION=v1.2.3
curl -fLO "https://github.com/Bend-Function/MasterDNS-Agent/releases/download/$VERSION/install.sh"
sudo sh install.sh install --version "$VERSION" --server-url https://masterdns.example
```

The script creates the unprivileged `masterdns-agent` user and these paths:

| Path | Owner/mode | Purpose |
| --- | --- | --- |
| `/usr/local/bin/masterdns-agent` | root, `0755` | executable |
| `/etc/masterdns-agent` | masterdns-agent, `0700` | configuration and runtime token |
| `/etc/masterdns-agent/config.json` | masterdns-agent, `0600` | agent configuration |
| `/var/lib/masterdns-agent` | masterdns-agent, `0700` | bounded durable result buffer |

Installation writes an enrollment-ready configuration and enables the service,
but does not start it. Exchange the one-time install token through standard
input, then start the service:

```sh
sudo -u masterdns-agent /usr/local/bin/masterdns-agent enroll \
  --config /etc/masterdns-agent/config.json
sudo systemctl start masterdns-agent
sudo sh install.sh status
```

The enrollment command also accepts
`--install-token-file /path/to/protected-file`. The file path may appear in the
process list; the token value does not. Do not put a token in a URL, command-line
argument, configuration value, or shell history.

Logs go to the systemd journal and exclude tokens and response bodies:

```sh
journalctl -u masterdns-agent
```

The unit requires no privileged network capability. It sets
`NoNewPrivileges`, uses a strict read-only system view, and permits writes only
under `/var/lib/masterdns-agent`.

## Updates and removal

An update downloads `SHA256SUMS` and the architecture-specific binary from the
fixed HTTPS release origin. It checks SHA-256 once, runs `version` and
`config-check`, then atomically replaces the executable. An active service is
stopped and restarted; if startup fails, the previous executable is restored.
Configuration and credentials are never overwritten.

```sh
sudo sh install.sh update --version "$VERSION"
sudo sh install.sh uninstall
```

Normal uninstall removes the executable and unit while preserving configuration,
credentials, and buffered results. Removing those files requires an explicit
purge:

```sh
sudo sh install.sh uninstall --purge
```

`SHA256SUMS` detects a damaged or mismatched download from the same release. It
is not an independent signature; HTTPS and the trusted
`Bend-Function/MasterDNS-Agent` release repository are the trust boundary.

## macOS and Windows

Releases include foreground binaries for amd64 and arm64:

- `masterdns-agent-darwin-amd64`
- `masterdns-agent-darwin-arm64`
- `masterdns-agent-windows-amd64.exe`
- `masterdns-agent-windows-arm64.exe`

Verify the chosen file against `SHA256SUMS`, rename it to `masterdns-agent`
(`masterdns-agent.exe` on Windows), and run `version`, `config-check`, `enroll`,
or `run` from a terminal. On Windows, keep the token inside the current user's
profile and restrict its ACL to that user. On macOS and other Unix systems, use
mode `0600` for token files.

An enabled address family declares local capability. When IPv6 is disabled or
unusable, IPv6 tasks return `unavailable`; they do not report the target as
failed. Set `allowIpv6` only on hosts with working IPv6 connectivity.
