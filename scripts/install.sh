#!/bin/sh
set -eu

RELEASE_ORIGIN=https://github.com/Bend-Function/MasterDNS-Agent/releases/download
SERVICE_NAME=masterdns-agent
SERVICE_USER=masterdns-agent
TEST_MODE=${MASTERDNS_TEST_MODE:-0}
TEST_ROOT=${MASTERDNS_TEST_ROOT:-}
VERSION=
SERVER_URL=
PURGE=0
WORK_DIR=
UPDATE_BACKUP=
UPDATE_PENDING=0
UPDATE_RESTART=0

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
	status=$1
	trap - EXIT HUP INT TERM
	if [ "$UPDATE_PENDING" -eq 1 ]; then
		if [ -f "$UPDATE_BACKUP" ] && [ ! -L "$UPDATE_BACKUP" ]; then
			if [ "$UPDATE_RESTART" -eq 1 ]; then
				run_systemctl stop "$SERVICE_NAME.service" >/dev/null 2>&1 || true
			fi
			if atomic_replace "$UPDATE_BACKUP" "$BINARY"; then
				UPDATE_BACKUP=
				if [ "$UPDATE_RESTART" -eq 1 ] && ! start_service_checked; then
					echo "masterdns-agent installer: restored old binary but service restart failed" >&2
					status=1
				fi
			else
				echo "masterdns-agent installer: failed to restore old binary" >&2
				status=1
			fi
		else
			echo "masterdns-agent installer: update backup is missing or unsafe" >&2
			status=1
		fi
	elif [ -n "$UPDATE_BACKUP" ]; then
		rm -f "$UPDATE_BACKUP"
	fi
	if [ -n "$WORK_DIR" ] && [ -d "$WORK_DIR" ]; then
		rm -rf "$WORK_DIR"
	fi
	exit "$status"
}
trap 'cleanup $?' EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

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

case "$TEST_MODE" in 0|1) ;; *) die "MASTERDNS_TEST_MODE must be 0 or 1" ;; esac
if [ -n "$TEST_ROOT" ] && [ "$TEST_MODE" -ne 1 ]; then
	die "MASTERDNS_TEST_ROOT requires MASTERDNS_TEST_MODE=1"
fi

if [ "$TEST_MODE" -eq 1 ]; then
	if [ -n "$TEST_ROOT" ]; then
		[ "$TEST_ROOT" != / ] || die "test root must not be /"
		case "$TEST_ROOT" in /*) ;; *) die "test root must be absolute" ;; esac
	fi
	OS=${MASTERDNS_TEST_OS:-$(uname -s | tr '[:upper:]' '[:lower:]')}
	ARCH=${MASTERDNS_TEST_ARCH:-$(uname -m)}
	if [ -n "$TEST_ROOT" ]; then
		SYSTEMCTL=${MASTERDNS_TEST_SYSTEMCTL:-:}
	else
		SYSTEMCTL=${MASTERDNS_TEST_SYSTEMCTL:-systemctl}
	fi
	RELEASE_DIR=${MASTERDNS_TEST_RELEASE_DIR:-}
	STARTUP_CHECKS=${MASTERDNS_TEST_STARTUP_CHECKS:-11}
	STARTUP_DELAY=${MASTERDNS_TEST_STARTUP_DELAY:-1}
else
	OS=$(uname -s | tr '[:upper:]' '[:lower:]')
	ARCH=$(uname -m)
	SYSTEMCTL=systemctl
	RELEASE_DIR=
	TEST_ROOT=
	STARTUP_CHECKS=11
	STARTUP_DELAY=1
	if [ -n "${MASTERDNS_TEST_RELEASE_DIR:-}${MASTERDNS_TEST_SYSTEMCTL:-}${MASTERDNS_TEST_OS:-}${MASTERDNS_TEST_ARCH:-}" ]; then
		die "test overrides require MASTERDNS_TEST_MODE=1"
	fi
fi

case "$STARTUP_CHECKS:$STARTUP_DELAY" in
*[!0-9:]*) die "invalid startup verification settings" ;;
esac
[ "$STARTUP_CHECKS" -gt 0 ] || die "startup verification requires at least one check"

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

start_service_checked() {
	run_systemctl start "$SERVICE_NAME.service" || return 1
	check=0
	while [ "$check" -lt "$STARTUP_CHECKS" ]; do
		run_systemctl is-active --quiet "$SERVICE_NAME.service" || return 1
		check=$((check + 1))
		if [ "$check" -lt "$STARTUP_CHECKS" ] && [ "$STARTUP_DELAY" -gt 0 ]; then
			sleep "$STARTUP_DELAY"
		fi
	done
}

fetch() {
	name=$1
	destination=$2
	if [ "$TEST_MODE" -eq 1 ] && [ -n "$RELEASE_DIR" ]; then
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

ensure_directory() {
	path=$1
	mode=$2
	if [ -L "$path" ]; then
		die "refusing symbolic-link directory: $path"
	fi
	if [ -e "$path" ]; then
		[ -d "$path" ] || die "managed path is not a directory: $path"
	else
		install -d -m "$mode" "$path"
	fi
	chmod "$mode" "$path"
}

ensure_regular_or_absent() {
	path=$1
	if [ -L "$path" ]; then
		die "refusing symbolic-link file: $path"
	fi
	if [ -e "$path" ] && [ ! -f "$path" ]; then
		die "managed path is not a regular file: $path"
	fi
}

atomic_replace() {
	source=$1
	destination=$2
	if [ "$TEST_MODE" -eq 1 ] && [ "${MASTERDNS_TEST_FAIL_NEXT_REPLACE:-}" = 1 ]; then
		MASTERDNS_TEST_FAIL_NEXT_REPLACE=0
		return 1
	fi
	if [ -n "$TEST_ROOT" ]; then
		mv -f "$source" "$destination"
	else
		mv -fT -- "$source" "$destination"
	fi
}

require_directory() {
	path=$1
	[ ! -L "$path" ] || die "refusing symbolic-link directory: $path"
	[ -d "$path" ] || die "managed directory is missing or unsafe: $path"
}

create_directories() {
	ensure_directory "$BIN_DIR" 0755
	ensure_directory "$SYSTEMD_DIR" 0755
	ensure_directory "$CONFIG_DIR" 0700
	ensure_directory "$STATE_DIR" 0700
	if [ -z "$TEST_ROOT" ]; then
		chown "$SERVICE_USER:$SERVICE_USER" "$CONFIG_DIR" "$STATE_DIR"
	fi
}

install_service() {
	unit_source=$WORK_DIR/masterdns-agent.service
	fetch masterdns-agent.service "$unit_source"
	chmod 0644 "$unit_source"
	ensure_regular_or_absent "$SERVICE_FILE"
	atomic_replace "$unit_source" "$SERVICE_FILE"
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
	config_candidate=$WORK_DIR/config.json
	cat >"$config_candidate" <<EOF
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
	chmod 0600 "$config_candidate"
	if [ -z "$TEST_ROOT" ]; then
		chown "$SERVICE_USER:$SERVICE_USER" "$config_candidate"
	fi
	ensure_regular_or_absent "$CONFIG"
	[ ! -e "$CONFIG" ] || die "configuration appeared during installation"
	atomic_replace "$config_candidate" "$CONFIG"
}

install_agent() {
	require_mutation_access
	ensure_regular_or_absent "$BINARY"
	[ ! -e "$BINARY" ] || die "agent is already installed; use update"
	create_service_user
	create_directories
	ensure_regular_or_absent "$CONFIG"
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
	require_directory "$BIN_DIR"
	require_directory "$CONFIG_DIR"
	require_directory "$STATE_DIR"
	ensure_regular_or_absent "$BINARY"
	[ -x "$BINARY" ] || die "agent is not installed"
	ensure_regular_or_absent "$CONFIG"
	[ -r "$CONFIG" ] || die "configuration is missing"
	download_binary
	"$DOWNLOADED_BINARY" config-check --config "$CONFIG" >/dev/null
	UPDATE_BACKUP=$(mktemp "$BIN_DIR/.masterdns-agent.backup.XXXXXX")
	cp -p "$BINARY" "$UPDATE_BACKUP"
	UPDATE_PENDING=1
	was_active=0
	if run_systemctl is-active --quiet "$SERVICE_NAME.service" >/dev/null 2>&1; then
		was_active=1
		UPDATE_RESTART=1
		run_systemctl stop "$SERVICE_NAME.service" || die "could not stop service"
	else
		state=$?
		case "$state" in 3|4) ;; *) die "could not determine service state" ;; esac
	fi
	if [ "$TEST_MODE" -eq 1 ] && [ "${MASTERDNS_TEST_FAIL_AFTER_STOP:-}" = 1 ]; then
		die "injected post-stop failure"
	fi
	if [ "$TEST_MODE" -eq 1 ] && [ "${MASTERDNS_TEST_INTERRUPT_AFTER_STOP:-}" = 1 ]; then
		kill -TERM "$$"
	fi
	atomic_replace "$DOWNLOADED_BINARY" "$BINARY"
	if [ "$was_active" -eq 1 ] && ! start_service_checked; then
		die "updated service failed during startup verification"
	fi
	UPDATE_PENDING=0
	rm -f "$UPDATE_BACKUP"
	UPDATE_BACKUP=
	echo "updated $SERVICE_NAME to $VERSION"
}

uninstall_agent() {
	require_mutation_access
	if run_systemctl is-active --quiet "$SERVICE_NAME.service" >/dev/null 2>&1; then
		run_systemctl disable --now "$SERVICE_NAME.service" >/dev/null 2>&1 || die "could not stop and disable active service"
		if run_systemctl is-active --quiet "$SERVICE_NAME.service" >/dev/null 2>&1; then
			die "service remained active after disable --now"
		else
			state=$?
			case "$state" in 3|4) ;; *) die "could not verify stopped service" ;; esac
		fi
	else
		state=$?
		case "$state" in
		3)
			if [ -e "$SERVICE_FILE" ] || [ -L "$SERVICE_FILE" ]; then
				run_systemctl disable --now "$SERVICE_NAME.service" >/dev/null 2>&1 || die "could not stop and disable installed service"
				if run_systemctl is-active --quiet "$SERVICE_NAME.service" >/dev/null 2>&1; then
					die "service remained active after disable --now"
				else
					state=$?
					case "$state" in 3|4) ;; *) die "could not verify stopped service" ;; esac
				fi
			fi
			;;
		4) ;;
		*) die "could not determine service state" ;;
		esac
	fi
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
