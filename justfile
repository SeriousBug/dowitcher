run:
    #!/usr/bin/env bash
    set -uo pipefail
    # go run spawns the compiled binary as a child, so kill the whole process
    # group rather than the tracked PIDs.
    trap 'trap - INT TERM EXIT; kill 0' INT TERM EXIT
    prefix() { while IFS= read -r line; do printf '[%s] %s\n' "$1" "$line"; done; }
    mkdir -p ./library-dev
    # The browser talks to vite, not to the Go server, so the WS Origin the
    # handshake carries is vite's. Leaving the default :8080 origin here makes
    # handleWS reject every dev socket with 403.
    #
    # -tags dev is what compiles DOWITCHER_DEV_AUTH in at all; without it the
    # variable below is inert and the dev server asks for a passkey. The bypass
    # is behind a build tag so no release binary can carry it. DOWITCHER_ADDR
    # binds loopback explicitly because dev-auth refuses to boot otherwise.
    DOWITCHER_DB=./dowitcher.db DOWITCHER_LIBRARY=./library-dev DOWITCHER_DATA=./data-dev \
        DOWITCHER_ADDR=127.0.0.1:8080 \
        DOWITCHER_ORIGIN=http://localhost:5173 \
        DOWITCHER_DEV_AUTH=dev go run -tags dev ./cmd/dowitcher 2>&1 | prefix api &
    (cd web && pnpm dev 2>&1 | prefix web) &
    wait

test:
    go test ./...

# test-dev covers what only exists under -tags dev: the auth bypass itself. The
# default suite cannot reach that code, so both runs are needed to cover it all.
test-dev:
    go test -tags dev ./...

check:
    go vet ./...
    go vet -tags dev ./...
    go test ./...
    go test -tags dev ./...
