#!/bin/sh
# Deploy the gonje source to moshix.tech:/root/gonje and build it there
# (the build step is ./moshix.sh),
# for testing against the Sine Nomine NJE node on that host.
#
# Usage: ./deploy.sh [-n] [-b]
#   -n  dry run: show what would be copied, change nothing
#   -b  copy only, don't build on the remote host
#
# Override the target with DEPLOY_HOST / DEPLOY_DIR, e.g.
#   DEPLOY_HOST=root@otherhost ./deploy.sh
#
# The remote gonje.cnf, logs, inbox/outbox and binaries are never
# overwritten or deleted: they are excluded from the copy.
set -eu

host=${DEPLOY_HOST:-root@moshix.tech}
dir=${DEPLOY_DIR:-/root/gonje}
dry=""
build=yes

while getopts nb opt; do
    case $opt in
        n) dry="--dry-run" ;;
        b) build=no ;;
        *) echo "usage: $0 [-n] [-b]" >&2; exit 2 ;;
    esac
done

cd "$(dirname "$0")"

version=$(sed -n 's/^var version = "\(.*\)"$/\1/p' cmd/gonje/main.go)
echo "deploying gonje $version to $host:$dir"

# rsync only sends what changed and compresses it (-z), which helps on a
# slow link. --delete removes remote source files we deleted locally, but
# the excluded paths below are protected from deletion too.
ssh "$host" "mkdir -p '$dir'"
rsync -az --delete $dry \
    --exclude '.git/' \
    --exclude '.claude/' \
    --exclude 'bin/' \
    --exclude '/gonje' \
    --exclude 'gonje.cnf' \
    --exclude '*.log' \
    --exclude 'inbox/' \
    --exclude 'outbox/' \
    --exclude 'test/nodeA/' \
    --exclude 'test/nodeB/' \
    ./ "$host:$dir/"

if [ -n "$dry" ] || [ "$build" = no ]; then
    echo "done (no remote build)"
    exit 0
fi

# Build on the remote host (vet, tests, build.sh -> bin/).
exec ./moshix.sh
