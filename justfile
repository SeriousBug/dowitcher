run:
    #!/usr/bin/env bash
    set -uo pipefail
    # go run spawns the compiled binary as a child, so kill the whole process
    # group rather than the tracked PIDs.
    trap 'trap - INT TERM EXIT; kill 0' INT TERM EXIT
    prefix() { while IFS= read -r line; do printf '[%s] %s\n' "$1" "$line"; done; }
    mkdir -p ./library-dev
    LONGBOX_DB=./longbox.db LONGBOX_LIBRARY=./library-dev LONGBOX_DATA=./data-dev \
        LONGBOX_DEV_AUTH=dev go run ./cmd/longbox 2>&1 | prefix api &
    (cd web && pnpm dev 2>&1 | prefix web) &
    wait

test:
    go test ./...

check:
    go vet ./...
    go test ./...
