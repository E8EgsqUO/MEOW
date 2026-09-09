#!/bin/bash
# Cross compile release binaries into the repository root.
#
# Usage: ./script/build-release.sh [output-dir]

set -euo pipefail

cd "$(dirname "$0")/.."
outdir="${1:-.}"
mkdir -p "$outdir"

version=$(sed -n 's/^\tversion  *= "\(.*\)"$/\1/p' config.go)
if [ -z "$version" ]; then
    echo "could not read version from config.go" >&2
    exit 1
fi
echo "building MEOW $version"

build_target() {
    local goos="$1"
    local goarch="$2"
    local suffix="$3"
    local ldflags="${4:--s -w}"
    local out="$outdir/MEOW-$suffix"

    if [[ "$goarch" == "arm" ]]; then
        env CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" GOARM=7 \
            go build -trimpath -buildvcs=false -ldflags="$ldflags" -o "$out" .
    else
        env CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
            go build -trimpath -buildvcs=false -ldflags="$ldflags" -o "$out" .
    fi
    echo "  $(printf '%-22s' "$goos/$goarch") $out"
}

build_target linux   amd64 linux-amd64
build_target linux   arm64 linux-arm64
build_target linux   arm   linux-armv7
build_target darwin  arm64 darwin-arm64
build_target windows amd64 windows-amd64.exe
# Same program, built as a GUI-subsystem executable so Windows does not create
# a console window. Runtime state remains available at http://127.0.0.1:4411/status.
build_target windows amd64 windows-amd64-gui.exe "-s -w -H=windowsgui -X main.windowsGUI=true"

echo
ls -lh "$outdir"/MEOW-* | awk '{print "  " $5 "\t" $9}'
