#!/bin/sh
set -eu

RELEASE_ORIGIN=https://github.com/Bend-Function/MasterDNS-Agent/releases/download
SERVICE_NAME=masterdns-agent
SERVICE_USER=masterdns-agent
SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
TEST_ROOT=${MASTERDNS_TEST_ROOT:-}
VERSION=
SERVER_URL=
PURGE=0
WORK_DIR=

usage() {
	cat >&2 <<'EOF'
usage:
  install.sh install --version VERSION --server-url HTTPS_URL
  install.sh status
  install.sh update --version VERSION
  install.sh uninstall [--purge]
EOF
	exit 2
}

die() {
	echo "masterdns-agent installer: $*" >&2
	exit 1
}

cleanup() {
	if [ -n "$WORK_DIR" ] && [ -d "$WORK_DIR" ]; then
		rm -rf "$WORK_DIR"
	fi
}
trap cleanup EXIT HUP INT TERM

[ "$#" -ge 1 ] || usage
COMMAND=$1
shift
while [ "$#" -gt 0 ]; do
	case "$1" in
	--version)
		[ "$#" -ge 2 ] || usage
		VERSION=$2
		shift 2
		;;
	--server-url)
		[ "$#" -ge 2 ] || usage
		SERVER_URL=$2
		shift 2
		;;
	--purge)
		PURGE=1
		shift
		;;
	*) usage ;;
	esac
done

case "$COMMAND" in
install|update)
	[ -n "$VERSION" ] || die "$COMMAND requires --version"
		case "$VERSION" in *[!A-Za-z0-9._+-]*|.|..) die "invalid version" ;; esac
	;;
status|uninstall) [ -z "$VERSION$SERVER_URL" ] || usage ;;
*) usage ;;
esac
[ "$COMMAND" = install ] || [ -z "$SERVER_URL" ] || usage
[ "$COMMAND" = uninstall ] || [ "$PURGE" -eq 0 ] || usage

if [ -n "$TEST_ROOT" ]; then
	[ "$TEST_ROOT" != / ] || die "test root must not be /"
	case "$TEST_ROOT" in /*) ;; *) die "test root must be absolute" ;; esac
	OS=${MASTERDNS_TEST_OS:-$(uname -s | tr '[:upper:]' '[:lower:]')}
	ARCH=${MASTERDNS_TEST_ARCH:-$(uname -m)}
	SYSTEMCTL=${MASTERDNS_TEST_SYSTEMCTL:-:}
	RELEASE_DIR=${MASTERDNS_TEST_RELEASE_DIR:-}
else
	OS=$(uname -s | tr '[:upper:]' '[:lower:]')
	ARCH=$(uname -m)
	SYSTEMCTL=systemctl
	RELEASE_DIR=
	TEST_ROOT=
fi

case "$ARCH" in
x86_64|amd64) ARCH=amd64 ;;
aarch64|arm64) ARCH=arm64 ;;
*) die "unsupported architecture: $ARCH" ;;
esac

root_path() {
	printf '%s%s\n' "$TEST_ROOT" "$1"
}

BIN_DIR=$(root_path /usr/local/bin)
BINARY=$BIN_DIR/masterdns-agent
CONFIG_DIR=$(root_path /etc/masterdns-agent)
CONFIG=$CONFIG_DIR/config.json
TOKEN_FILE=$CONFIG_DIR/runtime-token
STATE_DIR=$(root_path /var/lib/masterdns-agent)
SYSTEMD_DIR=$(root_path /etc/systemd/system)
SERVICE_FILE=$SYSTEMD_DIR/masterdns-agent.service

require_mutation_access() {
	[ "$OS" = linux ] || die "installation is supported on Linux only"
	if [ -z "$TEST_ROOT" ] && [ "$(id -u)" -ne 0 ]; then
		die "run as root"
	fi
}

run_systemctl() {
	"$SYSTEMCTL" "$@"
}

fetch() {
	name=$1
	destination=$2
	if [ -n "$TEST_ROOT" ] && [ -n "$RELEASE_DIR" ]; then
		cp "$RELEASE_DIR/$VERSION/$name" "$destination"
	else
		curl -fsSL --proto '=https' --tlsv1.2 \
			"$RELEASE_ORIGIN/$VERSION/$name" -o "$destination"
	fi
}

sha256_file() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{print $1}'
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | awk '{print $1}'
	else
		die "sha256sum or shasum is required"
	fi
}

download_binary() {
	artifact="masterdns-agent-$OS-$ARCH"
	[ "$OS" != windows ] || artifact="$artifact.exe"
	mkdir -p "$BIN_DIR"
	WORK_DIR=$(mktemp -d "$BIN_DIR/.masterdns-agent-install.XXXXXX")
	manifest=$WORK_DIR/SHA256SUMS
	candidate=$WORK_DIR/masterdns-agent
	fetch SHA256SUMS "$manifest"
	fetch "$artifact" "$candidate"
	expected=$(awk -v name="$artifact" '$2 == name || $2 == "*" name { print $1 }' "$manifest")
	[ "${#expected}" -eq 64 ] || die "release manifest has no unique checksum for $artifact"
	case "$expected" in *[!0-9A-Fa-f]*) die "release manifest has an invalid checksum" ;; esac
	actual=$(sha256_file "$candidate")
	actual=$(printf '%s' "$actual" | tr '[:upper:]' '[:lower:]')
	expected=$(printf '%s' "$expected" | tr '[:upper:]' '[:lower:]')
	[ "$actual" = "$expected" ] || die "SHA-256 verification failed for $artifact"
	chmod 0755 "$candidate"
	reported_version=$("$candidate" version) || die "downloaded binary failed its version check"
	case "$reported_version" in
	"masterdns-agent $VERSION ("*")") ;;
	*) die "downloaded binary does not report requested version $VERSION" ;;
	esac
	DOWNLOADED_BINARY=$candidate
}

create_service_user() {
	[ -n "$TEST_ROOT" ] && return
	if ! id "$SERVICE_USER" >/dev/null 2>&1; then
		nologin=/usr/sbin/nologin
		[ -x "$nologin" ] || nologin=/sbin/nologin
		useradd --system --home-dir /nonexistent --shell "$nologin" "$SERVICE_USER"
	fi
}

create_directories() {
	install -d -m 0755 "$BIN_DIR" "$SYSTEMD_DIR"
	install -d -m 0700 "$CONFIG_DIR" "$STATE_DIR"
	if [ -z "$TEST_ROOT" ]; then
		chown "$SERVICE_USER:$SERVICE_USER" "$CONFIG_DIR" "$STATE_DIR"
	fi
}

install_service() {
	unit_source=$SCRIPT_DIR/../packaging/systemd/masterdns-agent.service
	if [ -r "$unit_source" ]; then
		install -m 0644 "$unit_source" "$SERVICE_FILE"
	else
		unit_source=$WORK_DIR/masterdns-agent.service
		fetch masterdns-agent.service "$unit_source"
		install -m 0644 "$unit_source" "$SERVICE_FILE"
	fi
	run_systemctl daemon-reload
}

write_initial_config() {
	[ -n "$SERVER_URL" ] || die "install requires --server-url when configuration does not exist"
	case "$SERVER_URL" in
	*"
"*) die "server URL must be a single line" ;;
	esac
	printf '%s\n' "$SERVER_URL" | grep -Eq '^https://[^[:space:]]+$' || die "server URL must use HTTPS"
	escaped_url=$(printf '%s' "$SERVER_URL" | sed 's/\\/\\\\/g; s/"/\\"/g')
	umask 077
	cat >"$CONFIG" <<EOF
{
  "serverUrl": "$escaped_url",
  "probeId": "",
  "tokenFile": "$TOKEN_FILE",
  "stateDir": "$STATE_DIR",
  "maxConcurrency": 8,
  "allowIpv4": true,
  "allowIpv6": true,
  "allowedPrivateCidrs": []
}
EOF
	chmod 0600 "$CONFIG"
	if [ -z "$TEST_ROOT" ]; then
		chown "$SERVICE_USER:$SERVICE_USER" "$CONFIG"
	fi
}

install_agent() {
	require_mutation_access
	[ ! -e "$BINARY" ] || die "agent is already installed; use update"
	create_service_user
	create_directories
	download_binary
	if [ ! -e "$CONFIG" ]; then
		write_initial_config
	fi
	mv "$DOWNLOADED_BINARY" "$BINARY"
	chmod 0755 "$BINARY"
	install_service
	run_systemctl enable "$SERVICE_NAME.service"
	echo "installed $SERVICE_NAME $VERSION; enroll before starting the service"
}

update_agent() {
	require_mutation_access
	[ -x "$BINARY" ] || die "agent is not installed"
	[ -r "$CONFIG" ] || die "configuration is missing"
	download_binary
	"$DOWNLOADED_BINARY" config-check --config "$CONFIG" >/dev/null
	backup=$BIN_DIR/.masterdns-agent.backup.$$
	cp -p "$BINARY" "$backup"
	was_active=0
	if run_systemctl is-active --quiet "$SERVICE_NAME.service" >/dev/null 2>&1; then
		was_active=1
		run_systemctl stop "$SERVICE_NAME.service" || {
			rm -f "$backup"
			die "could not stop service"
		}
	fi
	mv "$DOWNLOADED_BINARY" "$BINARY"
	chmod 0755 "$BINARY"
	if [ "$was_active" -eq 1 ] &&
		{ ! run_systemctl start "$SERVICE_NAME.service" || ! run_systemctl is-active --quiet "$SERVICE_NAME.service"; }
	then
		mv "$backup" "$BINARY"
		if ! run_systemctl start "$SERVICE_NAME.service" || ! run_systemctl is-active --quiet "$SERVICE_NAME.service"; then
			echo "masterdns-agent installer: restored old binary but service restart still failed" >&2
		fi
		die "updated service failed to start; restored old binary"
	fi
	rm -f "$backup"
	echo "updated $SERVICE_NAME to $VERSION"
}

uninstall_agent() {
	require_mutation_access
	run_systemctl disable --now "$SERVICE_NAME.service" >/dev/null 2>&1 || true
	rm -f "$SERVICE_FILE" "$BINARY"
	run_systemctl daemon-reload >/dev/null 2>&1 || true
	if [ "$PURGE" -eq 1 ]; then
		rm -rf "$CONFIG_DIR" "$STATE_DIR"
		echo "uninstalled $SERVICE_NAME and purged configuration, credentials, and state"
	else
		echo "uninstalled $SERVICE_NAME; preserved $CONFIG_DIR and $STATE_DIR"
	fi
}

case "$COMMAND" in
install) install_agent ;;
update) update_agent ;;
status) run_systemctl status "$SERVICE_NAME.service" ;;
uninstall) uninstall_agent ;;
esac
