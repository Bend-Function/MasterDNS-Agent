#!/bin/sh
set -eu

REPO_ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
DIST_DIR=$REPO_ROOT/dist
GO_COMMAND=${GO_COMMAND:-go}
VERSION=${VERSION:-dev}
COMMIT=${COMMIT:-$(git -C "$REPO_ROOT" rev-parse --short=12 HEAD 2>/dev/null || printf unknown)}

case "$VERSION$COMMIT" in *[!A-Za-z0-9._+-]*) echo "version and commit must not contain whitespace or shell metacharacters" >&2; exit 1 ;; esac

mkdir -p "$DIST_DIR"
for target in \
	masterdns-agent-linux-amd64 \
	masterdns-agent-linux-arm64 \
	masterdns-agent-darwin-amd64 \
	masterdns-agent-darwin-arm64 \
	masterdns-agent-windows-amd64.exe \
	masterdns-agent-windows-arm64.exe \
	SHA256SUMS
do
	rm -f "$DIST_DIR/$target"
done

build() {
	os=$1
	arch=$2
	suffix=$3
	output=$DIST_DIR/masterdns-agent-$os-$arch$suffix
	CGO_ENABLED=0 GOOS=$os GOARCH=$arch "$GO_COMMAND" build \
		-trimpath \
		-ldflags "-s -w -X main.version=$VERSION -X main.commit=$COMMIT" \
		-o "$output" ./cmd/masterdns-agent
}

cd "$REPO_ROOT"
build linux amd64 ''
build linux arm64 ''
build darwin amd64 ''
build darwin arm64 ''
build windows amd64 .exe
build windows arm64 .exe

cd "$DIST_DIR"
if command -v sha256sum >/dev/null 2>&1; then
	sha256sum masterdns-agent-* >SHA256SUMS
else
	shasum -a 256 masterdns-agent-* >SHA256SUMS
fi
