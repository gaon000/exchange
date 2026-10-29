#!/bin/sh
# Install the verified development toolchain inside this checkout.
set -eu
project_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
version=$(cat "$project_dir/.go-version")
case "$version:$(uname -s):$(uname -m)" in
  1.27.3:Darwin:arm64)
    archive=go1.27.3.darwin-arm64.tar.gz
    checksum=dd3908cf20959d798c1e06ebe5b72213f0505ce01373b40ce3110975a6cd99c2
    ;;
  *) echo 'Only Go 1.27.3 on macOS arm64 is verified for P0.' >&2; exit 1 ;;
esac
destination="$project_dir/.tools/go$version"
if [ -e "$destination" ]; then
  [ "$(GOTOOLCHAIN=local "$destination/bin/go" version)" = "go version go$version darwin/arm64" ] || exit 1
  echo "Go $version is already installed."
  exit 0
fi
mkdir -p "$project_dir/.tools"
staging=$(mktemp -d "$project_dir/.tools/.go-install.XXXXXX")
trap 'rm -rf -- "$staging"' EXIT HUP INT TERM
curl --fail --location --proto '=https' --tlsv1.2 --retry 2 \
  "https://go.dev/dl/$archive" -o "$staging/$archive"
printf '%s  %s\n' "$checksum" "$staging/$archive" | shasum -a 256 -c -
tar -xzf "$staging/$archive" -C "$staging"
mv "$staging/go" "$destination"
GOTOOLCHAIN=local "$destination/bin/go" version
