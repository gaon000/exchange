#!/bin/sh
# Test-only hooks stay outside normal binaries. Kill only the owned child.
set -eu
project_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$project_dir"
sh scripts/setup-go.sh
mkdir -p bin var/crash-scenarios
scripts/go test -mod=readonly -c -o bin/concurrent-crash.test ./internal/runtime
EXCHANGE_CONCURRENT_CRASH_MODE=command exec bin/concurrent-crash.test -test.run='^TestConcurrentCrashCommand$' -test.timeout=150s -- "$@"
