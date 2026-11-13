#!/bin/sh
# Reproduce a bounded scenario in a new owned directory, never the original run.
set -eu
project_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$project_dir"
sh scripts/setup-go.sh
mkdir -p bin var/scenarios
scripts/go build -mod=readonly -o bin/recoverycheck ./tools/recoverycheck
exec bin/recoverycheck "$@"
