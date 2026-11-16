#!/bin/sh
# Build gonje for Linux amd64, Linux arm64, Android arm64 (Termux) and
# macOS arm64 into bin/.
# The version is taken from the global `version` variable in
# cmd/gonje/main.go and put into each binary's name.
# Usage: ./build.sh
set -eu

cd "$(dirname "$0")"

version=$(sed -n 's/^var version = "\(.*\)"$/\1/p' cmd/gonje/main.go)
if [ -z "$version" ]; then
    echo "build.sh: no 'var version = \"...\"' in cmd/gonje/main.go" >&2
    exit 1
fi

mkdir -p bin

# CGO off so the Linux binaries are static and cross-compile from macOS.
# android/arm64 is separate from linux/arm64: Android's bionic loader
# rejects plain Linux arm64 binaries (e.g. "TLS segment is underaligned"),
# while GOOS=android builds a PIE that runs under /system/bin/linker64
# and so works in Termux.
for target in linux/amd64 linux/arm64 android/arm64 darwin/arm64; do
    os=${target%/*}
    arch=${target#*/}
    out="bin/gonje-$version-$os-$arch"
    echo "building $out"
    CGO_ENABLED=0 GOOS=$os GOARCH=$arch \
        go build -trimpath -ldflags "-s -w" -o "$out" ./cmd/gonje
done

echo "done"
