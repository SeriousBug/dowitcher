#!/usr/bin/env bash
# Boots a throwaway Dowitcher for the E2E suite.
#
# This serves the built Go binary with the SPA embedded, not vite. The service
# worker is the thing under test and main.tsx never registers it in DEV, so a
# vite dev server would exercise none of the offline path — the suite would
# pass against a build that has no offline support at all.
#
# Authentication is bypassed with DOWITCHER_DEV_AUTH, which needs -tags dev to
# exist in the binary at all. The alternative is driving WebAuthn through a CDP
# virtual authenticator, which would test the login flow this suite is not
# about.
set -euo pipefail

port="${DOWITCHER_E2E_PORT:-8099}"
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo="$(cd "$here/../.." && pwd)"

# A fixed path, wiped per run, rather than mktemp: this script ends in exec, so
# an EXIT trap would never fire and every run would leak a temp directory.
work="$here/.tmp"
rm -rf "$work"
mkdir -p "$work/library" "$work/data"

cd "$repo"

# Written before the server starts, because main.go scans the library root
# before it opens the listener — so this is indexed by the time the first
# request can land. Dropping it in afterwards would mean waiting out the
# watcher's quiet+settle delay (~3s) on every run.
go run "$here/make-fixture.go" "$work/library/Test Comic.cbz" 3

# Built rather than `go run`: go run spawns the real binary as a child, so
# Playwright's kill would land on the wrapper and leave the server holding the
# port.
go build -tags dev -o "$work/dowitcher" ./cmd/dowitcher

# ORIGIN must name the browser's exact origin — it is the WebSocket allowlist,
# and a mismatch 403s every socket. ADDR must be loopback or dev-auth refuses
# to boot, which is why this is 127.0.0.1 and not the default ":8080".
exec env \
	DOWITCHER_DB="$work/dowitcher.db" \
	DOWITCHER_LIBRARY="$work/library" \
	DOWITCHER_DATA="$work/data" \
	DOWITCHER_ADDR="127.0.0.1:$port" \
	DOWITCHER_ORIGIN="http://localhost:$port" \
	DOWITCHER_DEV_AUTH=e2e \
	"$work/dowitcher"
