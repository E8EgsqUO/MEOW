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

# platform:output-suffix
targets=(
    "linux/amd64:linux-amd64"
    "linux/arm64:linux-arm64"
    "linux/arm:linux-armv7"
    "darwin/arm64:darwin-arm64"
    "darwin/amd64:darwin-amd64"
    "windows/amd64:windows-amd64.exe"
    "windows/arm64:windows-arm64.exe"
)

for target in "${targets[@]}"; do
    platform="${target%%:*}"
    suffix="${target##*:}"
    goos="${platform%%/*}"
    goarch="${platform##*/}"

    out="$outdir/MEOW-$suffix"
    env CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" ${GOARM:+GOARM=$GOARM} \
        go build -trimpath -buildvcs=false -ldflags="-s -w" -o "$out" .
    echo "  $(printf '%-22s' "$goos/$goarch") $out"
done

echo
ls -lh "$outdir"/MEOW-* | awk '{print "  " $5 "\t" $9}'
