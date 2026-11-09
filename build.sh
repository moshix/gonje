#!/bin/sh
# Build gonje for Linux amd64, Linux arm64 and macOS arm64 into bin/.
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
for target in linux/amd64 linux/arm64 darwin/arm64; do
    os=${target%/*}
    arch=${target#*/}
    out="bin/gonje-$version-$os-$arch"
    echo "building $out"
    CGO_ENABLED=0 GOOS=$os GOARCH=$arch \
        go build -trimpath -ldflags "-s -w" -o "$out" ./cmd/gonje
done

echo "done"
