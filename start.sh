#!/bin/sh
# Start gonje using the highest-version binary in bin/ that matches this
# machine's OS and CPU. Any arguments are passed through to gonje, e.g.
#   ./start.sh -c test/nodeA.cnf -split
set -eu

cd "$(dirname "$0")"

# Map uname output to Go's GOOS/GOARCH names used in the binary names.
case "$(uname -s)" in
    Darwin) os=darwin ;;
    Linux)  os=linux ;;
    *) echo "start.sh: unsupported OS $(uname -s)" >&2; exit 1 ;;
esac
case "$(uname -m)" in
    x86_64|amd64)  arch=amd64 ;;
    arm64|aarch64) arch=arm64 ;;
    *) echo "start.sh: unsupported CPU $(uname -m)" >&2; exit 1 ;;
esac

# Binaries are named bin/gonje-<version>-<os>-<arch>. Pull out the
# version part and sort it as a version number (so 0.10 > 0.9), keeping
# the highest. sort -V works with both GNU and macOS sort.
version=$(ls bin/ 2>/dev/null \
    | sed -n "s/^gonje-\(.*\)-$os-$arch\$/\1/p" \
    | sort -V | tail -n 1)

if [ -z "$version" ]; then
    echo "start.sh: no gonje binary for $os-$arch in bin/;" \
        "run ./build.sh first" >&2
    exit 1
fi

bin="bin/gonje-$version-$os-$arch"
echo "starting $bin"
exec "./$bin" "$@"
