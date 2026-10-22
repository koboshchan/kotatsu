#!/bin/sh
set -eu
VERSION=${VERSION:-$(git describe --tags --exact-match 2>/dev/null || echo v0.0.0-dev)}
COMMIT=$(git rev-parse HEAD)
case "$VERSION" in v[0-9]*.[0-9]*.[0-9]*) ;; *) echo "VERSION must start with v and contain a semantic version" >&2; exit 1 ;; esac
OUT=${OUT:-dist}
mkdir -p "$OUT"
SOURCE_DATE_EPOCH=${SOURCE_DATE_EPOCH:-$(git show -s --format=%ct HEAD)}
export SOURCE_DATE_EPOCH CGO_ENABLED=0
build() {
 NAME=$1 OS=$2 ARCH=$3 COMMAND=$4
 SUFFIX=
 [ "$OS" != windows ] || SUFFIX=.exe
 STAGE=$(mktemp -d)
 GOOS=$OS GOARCH=$ARCH go build -trimpath -buildvcs=false -ldflags="-s -w -buildid= -X github.com/koboshchan/kotatsu/internal/version.Version=$VERSION -X github.com/koboshchan/kotatsu/internal/version.Commit=$COMMIT" -o "$STAGE/$NAME$SUFFIX" "./cmd/$COMMAND"
 tar --sort=name --mtime="@$SOURCE_DATE_EPOCH" --owner=0 --group=0 --numeric-owner -C "$STAGE" -cf - "$NAME$SUFFIX" | gzip -n > "$OUT/${NAME}_${VERSION}_${OS}_${ARCH}.tar.gz"
 rm -rf "$STAGE"
}
for TARGET in linux/amd64 linux/arm64 darwin/arm64 windows/amd64 windows/arm64; do
 build kotatsu "${TARGET%/*}" "${TARGET#*/}" kotatsu
done
for ARCH in amd64 arm64; do build kotatsu-agent linux "$ARCH" agent; done
printf '{"version":"%s","commit":"%s"}\n' "$VERSION" "$COMMIT" > "$OUT/version.json"
(cd "$OUT"; sha256sum kotatsu*.tar.gz version.json | LC_ALL=C sort -k2 > SHA256SUMS)
echo "Built five CLI targets and two Linux agent targets in $OUT"
