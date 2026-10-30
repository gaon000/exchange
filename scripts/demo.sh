#!/bin/sh
# One-command, isolated trading and normal-restart demonstration.
set -eu
project_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$project_dir"
sh scripts/setup-go.sh
mkdir -p bin
scripts/go build -mod=readonly -o bin/demo-exchange ./cmd/exchange
scripts/go build -mod=readonly -o bin/demo ./cmd/demo
exec bin/demo --exchange "$project_dir/bin/demo-exchange" "$@"
