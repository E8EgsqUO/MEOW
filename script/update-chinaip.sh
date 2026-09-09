#!/bin/bash
# Regenerate chinaip_data.go from the current APNIC delegation statistics.
#
# The table is compiled into the binary, so this runs at development time, not
# on the user's machine. Nobody needs to subscribe to or refresh anything at
# runtime; building a fresh binary is what picks up fresh data.

set -euo pipefail

cd "$(dirname "$0")/.."

before=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")
echo "regenerating chinaip_data.go (working tree at $before)"

go run -tags generate chinaip_gen.go
gofmt -l chinaip_data.go >/dev/null

# A truncated or scrambled download would quietly send every site through the
# parent proxy, so refuse to keep a table that cannot pass the routing tests.
go test -run 'TestIPShouldDirect|TestCNIPPrefixBoundaries|TestIPsShouldDirect' .

if git diff --quiet -- chinaip_data.go 2>/dev/null; then
    echo "chinaip_data.go is already up to date"
else
    echo "chinaip_data.go updated:"
    git diff --stat -- chinaip_data.go 2>/dev/null || true
fi
