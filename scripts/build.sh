#!/usr/bin/env bash
# Build WaGramDeskLite: compile Windows resources, build the executable into dist/.

set -euo pipefail
cd "$(dirname "$0")/.."

# github.com/tc-hib/go-winres (pure Go resource compiler, no windres needed)
if command -v go-winres >/dev/null 2>&1; then
    RESTOOL="go-winres"
elif [ -x "$(go env GOPATH)/bin/go-winres.exe" ]; then
    RESTOOL="$(go env GOPATH)/bin/go-winres.exe"
else
    echo "go-winres not found. Install it with: go install github.com/tc-hib/go-winres@latest" >&2
    exit 1
fi
OUT="dist/WaGramDeskLite.exe"

echo "[1/3] Compiling Windows resources (icon, manifest, VERSIONINFO)..."
cd build
"$RESTOOL" make -arch amd64 --in winres.json
cp rsrc_windows_amd64.syso ../cmd/wagramdesklite/rsrc.syso
cd ..

echo "[2/3] Building $OUT..."
mkdir -p dist
go build -ldflags="-H windowsgui -s -w" -o "$OUT" ./cmd/wagramdesklite
# The tray icon is loaded at runtime from icon.ico next to the executable.
cp assets/icon.ico dist/icon.ico

echo "[3/3] Done: $OUT"