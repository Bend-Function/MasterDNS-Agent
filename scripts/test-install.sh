#!/bin/sh
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
TEST_TMP=$(mktemp -d "${TMPDIR:-/tmp}/masterdns-agent-install-test.XXXXXX")
TEST_ROOT="$TEST_TMP/root"
RELEASE_DIR="$TEST_TMP/releases"
SYSTEMCTL_LOG="$TEST_TMP/systemctl.log"
OUTPUT="$TEST_TMP/output"
SECRET="install-token-must-stay-on-stdin"
trap 'rm -rf "$TEST_TMP"' EXIT HUP INT TERM

fail() {
	echo "FAIL: $*" >&2
	exit 1
}

mode() {
	if stat -c '%a' "$1" >/dev/null 2>&1; then
		stat -c '%a' "$1"
	else
		stat -f '%Lp' "$1"
	fi
}

sha256() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{print $1}'
	else
		shasum -a 256 "$1" | awk '{print $1}'
	fi
}

make_release() {
	release=$1
	digest=${2:-good}
	reported_release=${3:-$release}
	dir="$RELEASE_DIR/$release"
	mkdir -p "$dir"
	artifact="$dir/masterdns-agent-linux-amd64"
	cat >"$artifact" <<EOF
#!/bin/sh
case "\${1:-}" in
version) echo "masterdns-agent $reported_release (test)" ;;
config-check)
	shift
	[ "\${1:-}" = "--config" ] && [ -r "\${2:-}" ] && grep -q '11111111-1111-4111-8111-111111111111' "\$2"
	;;
*) exit 1 ;;
esac
EOF
	chmod 0755 "$artifact"
	if [ "$digest" = good ]; then
		digest=$(sha256 "$artifact")
	fi
	printf '%s  %s\n' "$digest" "masterdns-agent-linux-amd64" >"$dir/SHA256SUMS"
	printf '[Unit]\nDescription=trusted release unit %s\n' "$release" >"$dir/masterdns-agent.service"
}

cat >"$TEST_TMP/systemctl" <<'EOF'
#!/bin/sh
set -eu
printf '%s\n' "$*" >>"$MASTERDNS_TEST_SYSTEMCTL_LOG"
case "${1:-}" in
is-active)
	if [ -e "$MASTERDNS_TEST_ROOT/report-inactive" ]; then
		rm "$MASTERDNS_TEST_ROOT/report-inactive"
		exit 1
	fi
	if [ -e "$MASTERDNS_TEST_ROOT/delayed-inactive-count" ]; then
		count=$(cat "$MASTERDNS_TEST_ROOT/delayed-inactive-count")
		count=$((count + 1))
		if [ "$count" -ge 3 ]; then
			rm "$MASTERDNS_TEST_ROOT/delayed-inactive-count"
			exit 1
		fi
		printf '%s\n' "$count" >"$MASTERDNS_TEST_ROOT/delayed-inactive-count"
	fi
	[ ! -e "$MASTERDNS_TEST_ROOT/service-inactive" ] || exit 3
	exit 0
	;;
start)
	if [ -e "$MASTERDNS_TEST_ROOT/fail-next-start" ]; then
		rm "$MASTERDNS_TEST_ROOT/fail-next-start"
		exit 1
	fi
	if [ -e "$MASTERDNS_TEST_ROOT/start-inactive" ]; then
		mv "$MASTERDNS_TEST_ROOT/start-inactive" "$MASTERDNS_TEST_ROOT/report-inactive"
	fi
	if [ -e "$MASTERDNS_TEST_ROOT/start-delayed-inactive" ]; then
		rm "$MASTERDNS_TEST_ROOT/start-delayed-inactive"
		printf '0\n' >"$MASTERDNS_TEST_ROOT/delayed-inactive-count"
	fi
	rm -f "$MASTERDNS_TEST_ROOT/service-inactive"
	;;
stop)
	touch "$MASTERDNS_TEST_ROOT/service-inactive"
	;;
disable)
	if [ -e "$MASTERDNS_TEST_ROOT/fail-disable" ]; then
		exit 1
	fi
	if [ "${2:-}" = "--now" ]; then
		touch "$MASTERDNS_TEST_ROOT/service-inactive"
	fi
	;;
status)
	[ ! -e "$MASTERDNS_TEST_ROOT/service-inactive" ] || exit 3
	;;
esac
EOF
chmod 0755 "$TEST_TMP/systemctl"

run_installer() {
	run_installer_at_root "$TEST_ROOT" "$@"
}

run_installer_at_root() {
	root=$1
	shift
	env \
		MASTERDNS_TEST_MODE=1 \
		MASTERDNS_TEST_ROOT="$root" \
		MASTERDNS_TEST_RELEASE_DIR="$RELEASE_DIR" \
		MASTERDNS_TEST_OS=linux \
		MASTERDNS_TEST_ARCH=amd64 \
		MASTERDNS_TEST_SYSTEMCTL="$TEST_TMP/systemctl" \
		MASTERDNS_TEST_SYSTEMCTL_LOG="$SYSTEMCTL_LOG" \
		MASTERDNS_TEST_STARTUP_CHECKS=4 \
		MASTERDNS_TEST_STARTUP_DELAY=0 \
		MASTERDNS_TEST_FAIL_AFTER_STOP="${MASTERDNS_TEST_FAIL_AFTER_STOP:-}" \
		MASTERDNS_TEST_INTERRUPT_AFTER_STOP="${MASTERDNS_TEST_INTERRUPT_AFTER_STOP:-}" \
		MASTERDNS_TEST_FAIL_NEXT_REPLACE="${MASTERDNS_TEST_FAIL_NEXT_REPLACE:-}" \
		sh "$SCRIPT_DIR/install.sh" "$@"
}

make_release v1.0.0
SYMLINK_ROOT="$TEST_TMP/symlink-root"
OUTSIDE_CONFIG="$TEST_TMP/outside-config"
mkdir -p "$SYMLINK_ROOT/etc/masterdns-agent"
ln -s "$OUTSIDE_CONFIG" "$SYMLINK_ROOT/etc/masterdns-agent/config.json"
if run_installer_at_root "$SYMLINK_ROOT" install --version v1.0.0 --server-url https://masterdns.example >"$OUTPUT" 2>&1; then
	fail "install accepted a dangling configuration symlink"
fi
[ ! -e "$OUTSIDE_CONFIG" ] || fail "install followed configuration symlink outside managed path"
[ -L "$SYMLINK_ROOT/etc/masterdns-agent/config.json" ] || fail "install replaced unsafe configuration symlink"
echo "PASS: dangling configuration symlink is rejected"

DIRECTORY_LINK_ROOT="$TEST_TMP/directory-link-root"
OUTSIDE_DIRECTORY="$TEST_TMP/outside-directory"
mkdir -p "$DIRECTORY_LINK_ROOT/etc" "$OUTSIDE_DIRECTORY"
printf 'preserve\n' >"$OUTSIDE_DIRECTORY/sentinel"
ln -s "$OUTSIDE_DIRECTORY" "$DIRECTORY_LINK_ROOT/etc/masterdns-agent"
if run_installer_at_root "$DIRECTORY_LINK_ROOT" install --version v1.0.0 --server-url https://masterdns.example >"$OUTPUT" 2>&1; then
	fail "install accepted a symbolic-link configuration directory"
fi
[ "$(cat "$OUTSIDE_DIRECTORY/sentinel")" = preserve ] || fail "install changed symlinked directory target"
[ ! -e "$OUTSIDE_DIRECTORY/config.json" ] || fail "install wrote through symbolic-link directory"
echo "PASS: symbolic-link managed directory is rejected"

BAD_ROOT="$TEST_TMP/bad-root"
if env \
	MASTERDNS_TEST_MODE=1 \
	MASTERDNS_TEST_ROOT="$BAD_ROOT" \
	MASTERDNS_TEST_RELEASE_DIR="$RELEASE_DIR" \
	MASTERDNS_TEST_OS=linux \
	MASTERDNS_TEST_ARCH=amd64 \
	MASTERDNS_TEST_SYSTEMCTL="$TEST_TMP/systemctl" \
	MASTERDNS_TEST_SYSTEMCTL_LOG="$SYSTEMCTL_LOG" \
	sh "$SCRIPT_DIR/install.sh" install --version v1.0.0 --server-url "$(printf 'https://masterdns.example\ninjected')" >"$OUTPUT" 2>&1
then
	fail "install accepted a server URL containing a newline"
fi
echo "PASS: invalid server URL is rejected"

make_release v1.0.1 good v9.9.9
BAD_VERSION_ROOT="$TEST_TMP/bad-version-root"
if env \
	MASTERDNS_TEST_MODE=1 \
	MASTERDNS_TEST_ROOT="$BAD_VERSION_ROOT" \
	MASTERDNS_TEST_RELEASE_DIR="$RELEASE_DIR" \
	MASTERDNS_TEST_OS=linux \
	MASTERDNS_TEST_ARCH=amd64 \
	MASTERDNS_TEST_SYSTEMCTL="$TEST_TMP/systemctl" \
	MASTERDNS_TEST_SYSTEMCTL_LOG="$SYSTEMCTL_LOG" \
	sh "$SCRIPT_DIR/install.sh" install --version v1.0.1 --server-url https://masterdns.example >"$OUTPUT" 2>&1
then
	fail "install accepted a binary reporting the wrong version"
fi
echo "PASS: mismatched release version is rejected"

printf '%s\n' "$SECRET" | run_installer install --version v1.0.0 --server-url https://masterdns.example >"$OUTPUT" 2>&1

BIN="$TEST_ROOT/usr/local/bin/masterdns-agent"
CONFIG_DIR="$TEST_ROOT/etc/masterdns-agent"
CONFIG="$CONFIG_DIR/config.json"
TOKEN="$CONFIG_DIR/runtime-token"
STATE="$TEST_ROOT/var/lib/masterdns-agent"
SERVICE="$TEST_ROOT/etc/systemd/system/masterdns-agent.service"

[ -x "$BIN" ] || fail "binary was not installed"
[ "$(mode "$BIN")" = 755 ] || fail "binary mode is $(mode "$BIN")"
[ "$(mode "$CONFIG_DIR")" = 700 ] || fail "config directory mode is $(mode "$CONFIG_DIR")"
[ "$(mode "$CONFIG")" = 600 ] || fail "config mode is $(mode "$CONFIG")"
[ "$(mode "$STATE")" = 700 ] || fail "state directory mode is $(mode "$STATE")"
[ "$(mode "$SERVICE")" = 644 ] || fail "service mode is $(mode "$SERVICE")"
grep -q 'trusted release unit v1.0.0' "$SERVICE" || fail "installer did not use pinned release unit"
if grep -F "$SECRET" "$OUTPUT" "$SYSTEMCTL_LOG" "$CONFIG" >/dev/null 2>&1; then
	fail "install token appeared in output, subprocess arguments, or config"
fi
echo "PASS: install permissions and token handling"

cat >"$CONFIG" <<EOF
{
  "serverUrl": "https://masterdns.example",
  "probeId": "11111111-1111-4111-8111-111111111111",
  "tokenFile": "$TOKEN",
  "stateDir": "$STATE",
  "maxConcurrency": 8,
  "allowIpv4": true,
  "allowIpv6": true,
  "allowedPrivateCidrs": []
}
EOF
chmod 0600 "$CONFIG"
printf '%s\n' runtime-secret >"$TOKEN"
chmod 0600 "$TOKEN"
printf '%s\n' buffered-result >"$STATE/preserved"
CONFIG_BEFORE=$(sha256 "$CONFIG")

make_release v1.1.0 "0000000000000000000000000000000000000000000000000000000000000000"
if run_installer update --version v1.1.0 >"$OUTPUT" 2>&1; then
	fail "update accepted a bad checksum"
fi
"$BIN" version | grep -q 'v1.0.0' || fail "checksum failure replaced the binary"
echo "PASS: checksum failure preserves installed binary"

make_release v1.2.0
touch "$TEST_ROOT/fail-next-start"
if run_installer update --version v1.2.0 >"$OUTPUT" 2>&1; then
	fail "update succeeded after service start failure"
fi
"$BIN" version | grep -q 'v1.0.0' || fail "service failure did not roll back the binary"
echo "PASS: service failure rolls back installed binary"

make_release v1.2.1
touch "$TEST_ROOT/start-inactive"
if run_installer update --version v1.2.1 >"$OUTPUT" 2>&1; then
	fail "update succeeded when service became inactive after start"
fi
"$BIN" version | grep -q 'v1.0.0' || fail "inactive service did not roll back the binary"
echo "PASS: inactive service rolls back installed binary"

make_release v1.2.2
touch "$TEST_ROOT/start-delayed-inactive"
if run_installer update --version v1.2.2 >"$OUTPUT" 2>&1; then
	fail "update succeeded when service failed during startup window"
fi
"$BIN" version | grep -q 'v1.0.0' || fail "delayed service failure did not roll back the binary"
echo "PASS: delayed service failure rolls back installed binary"

make_release v1.2.3
if MASTERDNS_TEST_FAIL_AFTER_STOP=1 run_installer update --version v1.2.3 >"$OUTPUT" 2>&1; then
	fail "update succeeded after injected post-stop failure"
fi
unset MASTERDNS_TEST_FAIL_AFTER_STOP
"$BIN" version | grep -q 'v1.0.0' || fail "post-stop failure did not preserve old binary"
run_installer status >/dev/null 2>&1 || fail "post-stop failure left service inactive"
echo "PASS: post-stop failure restores binary and service"

make_release v1.2.4
if MASTERDNS_TEST_INTERRUPT_AFTER_STOP=1 run_installer update --version v1.2.4 >"$OUTPUT" 2>&1; then
	fail "update succeeded after injected termination"
fi
unset MASTERDNS_TEST_INTERRUPT_AFTER_STOP
"$BIN" version | grep -q 'v1.0.0' || fail "termination did not preserve old binary"
run_installer status >/dev/null 2>&1 || fail "termination left service inactive"
echo "PASS: termination restores binary and service"

make_release v1.2.5
if MASTERDNS_TEST_FAIL_NEXT_REPLACE=1 run_installer update --version v1.2.5 >"$OUTPUT" 2>&1; then
	fail "update succeeded after injected replacement failure"
fi
unset MASTERDNS_TEST_FAIL_NEXT_REPLACE
"$BIN" version | grep -q 'v1.0.0' || fail "replacement failure did not preserve old binary"
run_installer status >/dev/null 2>&1 || fail "replacement failure left service inactive"
echo "PASS: replacement failure restores binary and service"

make_release v1.3.0
if ! run_installer update --version v1.3.0 >"$OUTPUT" 2>&1; then
	cat "$OUTPUT" >&2
	fail "valid update failed"
fi
"$BIN" version | grep -q 'v1.3.0' || fail "valid update was not installed"
[ "$(sha256 "$CONFIG")" = "$CONFIG_BEFORE" ] || fail "update changed configuration"
echo "PASS: validated update preserves configuration"

touch "$TEST_ROOT/fail-disable"
if run_installer uninstall --purge >"$OUTPUT" 2>&1; then
	fail "purge succeeded after service disable failure"
fi
[ -x "$BIN" ] || fail "failed purge removed binary"
[ -e "$CONFIG" ] || fail "failed purge removed configuration"
[ -e "$TOKEN" ] || fail "failed purge removed credentials"
[ -e "$STATE/preserved" ] || fail "failed purge removed state"
rm "$TEST_ROOT/fail-disable"
echo "PASS: uninstall aborts when active service cannot be stopped"

run_installer uninstall >"$OUTPUT" 2>&1
[ ! -e "$BIN" ] || fail "uninstall retained binary"
[ -e "$CONFIG" ] || fail "uninstall removed configuration"
[ -e "$TOKEN" ] || fail "uninstall removed credentials without --purge"
[ -e "$STATE/preserved" ] || fail "uninstall removed state"
echo "PASS: uninstall preserves configuration and state"

run_installer uninstall --purge >"$OUTPUT" 2>&1
[ ! -e "$CONFIG_DIR" ] || fail "--purge retained credentials"
[ ! -e "$STATE" ] || fail "--purge retained state"
echo "PASS: explicit purge removes credentials and state"
