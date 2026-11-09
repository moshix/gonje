#!/bin/sh
# Build gonje remotely on moshix.tech from the source already deployed to
# /root/gonje (see deploy.sh). Runs vet and the tests, then the remote
# build.sh, so the binaries land in /root/gonje/bin/ and the remote
# start.sh picks up the newest one.
#
# Usage: ./moshix.sh [-t]
#   -t  skip vet and tests, just build
#
# Override the target with DEPLOY_HOST / DEPLOY_DIR, as for deploy.sh.
set -eu

host=${DEPLOY_HOST:-root@moshix.tech}
dir=${DEPLOY_DIR:-/root/gonje}
tests=yes

while getopts t opt; do
    case $opt in
        t) tests=no ;;
        *) echo "usage: $0 [-t]" >&2; exit 2 ;;
    esac
done

echo "building gonje on $host:$dir"

# A login shell (-l) is used so Go's PATH from the profile is picked up.
ssh "$host" "sh -lc '
    set -e
    cd \"$dir\" 2>/dev/null || { echo \"$dir not found; run ./deploy.sh first\" >&2; exit 1; }
    command -v go >/dev/null || { echo \"go is not installed on the remote host\" >&2; exit 1; }
    go version
    if [ \"$tests\" = yes ]; then
        go vet ./...
        go test ./...
    fi
    ./build.sh
    [ -f gonje.cnf ] || echo \"note: no gonje.cnf in $dir yet; copy gonje.cnf.example and edit it\"
'"
