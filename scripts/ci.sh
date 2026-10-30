#!/usr/bin/env bash
# Run the same checks locally and in Actions; never treat missing native tests as success.
set -euo pipefail
project_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$project_dir"
if [ "$#" -ne 1 ]; then
  echo 'Usage: bash scripts/ci.sh portable|native' >&2
  exit 2
fi
mode=$1
test_args=(-mod=readonly -count=1 -timeout=300s -json ./...)
case "$mode" in
  portable) export CGO_ENABLED=0 ;;
  native)
    export CGO_ENABLED=1
    test_args=(-race "${test_args[@]}")
    ;;
  *) echo "Unknown CI mode: $mode" >&2; exit 2 ;;
esac
# External flags must not silently select fewer packages/tests or cross-compile.
unset GOFLAGS GOOS GOARCH
mkdir -p var/ci
report=$(mktemp -d "var/ci/$mode.XXXXXX")
mkdir "$report/tmp"
export TMPDIR="$project_dir/$report/tmp"
finish() {
  status=$?
  printf 'mode=%s exit=%s\nPower-loss validation: deferred, not tested.\n' "$mode" "$status" >> "$report/summary.txt"
  if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
    cat "$report/summary.txt" >> "$GITHUB_STEP_SUMMARY"
  fi
}
trap finish EXIT
echo "CI mode: $mode; evidence: $report"
scripts/go env -json GOVERSION GOOS GOARCH CGO_ENABLED CC > "$report/environment.json"
uname -a > "$report/platform.txt"
if [ "$mode" = native ]; then
  [ "$(uname -s):$(uname -m)" = Darwin:arm64 ] || {
    echo 'Native verification requires macOS arm64.' >&2; exit 1;
  }
  # The probe checks APFS through the same adapter used by the WAL. Fail before
  # testing if the filesystem, strict file/directory sync or locking is unsupported.
  scripts/go run -mod=readonly ./tools/storageprobe -dir "$TMPDIR" > "$report/storage-probe.json"
fi
echo 'Checking formatting, vet and build...'
goroot=$(scripts/go env GOROOT)
unformatted=$(git ls-files --cached --others --exclude-standard -z -- '*.go' | xargs -0 "$goroot/bin/gofmt" -l)
if [ -n "$unformatted" ]; then
  printf 'Run gofmt on:\n%s\n' "$unformatted" >&2
  exit 1
fi
git diff --check
git diff --cached --check
scripts/go vet -mod=readonly ./...
scripts/go build -mod=readonly ./...
# The manifest records the files selected by this job's platform/build tags.
scripts/go list -mod=readonly -json ./... > "$report/packages.json"
echo 'Running all tests (uncached); raw events are retained in tests.jsonl...'
test_status=0
scripts/go test "${test_args[@]}" > "$report/tests.jsonl" 2> "$report/tests.stderr" || test_status=$?
verify_status=0
scripts/go run -mod=readonly ./tools/civerify -mode "$mode" -packages "$report/packages.json" -events "$report/tests.jsonl" 2>&1 | tee "$report/summary.txt" || verify_status=$?
if [ "$test_status" -ne 0 ] || [ "$verify_status" -ne 0 ]; then
  cat "$report/tests.stderr" >&2
  tail -n 50 "$report/tests.jsonl" >&2
  exit 1
fi
