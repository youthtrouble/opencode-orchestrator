#!/bin/sh
# Cross-compiles orch for every supported target into dist/ (gitignored) and writes
# dist/checksums.txt. cgo is off, so linux binaries are fully static and darwin binaries
# link only libSystem.
set -eu

module_dir=$(cd "$(dirname "$0")/.." && pwd)
cd "$module_dir"

version=$(tr -d '[:space:]' < internal/version/VERSION)
rm -rf dist
mkdir dist

for target in darwin/arm64 darwin/amd64 linux/amd64 linux/arm64; do
  os=${target%/*}
  arch=${target#*/}
  out="dist/orch_${version}_${os}_${arch}"
  CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -ldflags="-s -w" -o "$out" ./cmd/orch
  echo "built $out"
done

cd dist
if command -v sha256sum >/dev/null 2>&1; then
  sha256sum orch_* > checksums.txt
else
  shasum -a 256 orch_* > checksums.txt
fi
echo "wrote dist/checksums.txt"
