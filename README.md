# Dowitcher

A self-hosted comic reader. Point it at a folder of CBZ files and it watches them, reads them in
the browser, and syncs your reading position across every device you open it on. It can also take a
folder of images, dedupe it, and package it into a CBZ.

Login is passkeys only. There are no passwords to phish, reuse, or leak. The first run prints a
one-time admin enrollment link to the logs; admins mint single-use invite links from there.

Dowitcher ships as a single static Go binary with the web UI embedded, on a `distroless/static` base.

## What it does

- **Reads your library.** Watches a folder of CBZ files, reads `ComicInfo.xml` for series, number,
  volume and summary, and picks up adds, edits, renames and deletions as they happen. A renamed file
  keeps its tags and reading progress: the scanner matches on content hash when the path misses.
- **Syncs reading position.** Progress is per user, per comic, so you can start on a laptop and
  finish on a phone.
- **Imports folders of images.** Hashes, groups near-duplicate pages, optionally re-encodes to AVIF,
  WebP or JPEG, and packages the result into a deduped CBZ. Progress streams over a WebSocket.
- **Tags and collections.** Tags are server-global. Collections are ordered, owned, and private by
  default.
- **Uploads stay private.** A comic you upload is yours alone until you put it in a collection and
  share that collection. Comics found under the watched library root are server-wide by definition.

## Sharing model

A comic is visible to a user if:

- it came from the watched library root, or
- they uploaded it, or
- it sits in a collection whose owner turned sharing on.

Sharing is opt-in per collection, never in bulk, and sharing grants read access only — the owner
stays the only one who can rename, reorder, or delete. The rule lives in SQL in one place
(`store.visibleComics`) and every comic read path goes through it, so a handler cannot forget it.

## Running it

```sh
docker run -d --name dowitcher \
  -p 8080:8080 \
  -v dowitcher-data:/data \
  -v /path/to/your/comics:/library \
  -e DOWITCHER_ORIGIN=https://dowitcher.example.com \
  -e DOWITCHER_RP_ID=dowitcher.example.com \
  ghcr.io/seriousbug/dowitcher:latest
```

Then read the logs for the first-run enrollment link:

```sh
docker logs dowitcher
```

### Configuration

| Variable | Default | Meaning |
| --- | --- | --- |
| `DOWITCHER_DB` | `/data/dowitcher.db` | SQLite database path |
| `DOWITCHER_ADDR` | `:8080` | Listen address |
| `DOWITCHER_ORIGIN` | `http://localhost:8080` | Public URL. Must match what the browser sees, or WebAuthn rejects every ceremony |
| `DOWITCHER_RP_ID` | `localhost` | WebAuthn relying party ID: the origin's hostname, no scheme, no port |
| `DOWITCHER_LIBRARY` | `/library` | Watched library root |
| `DOWITCHER_DATA` | `/data` | Working directory for uploads and imports |
| `DOWITCHER_DEV_AUTH` | unset | **Development only.** See below |

`DOWITCHER_ORIGIN` and `DOWITCHER_RP_ID` are the two that matter. Passkeys are bound to an origin, so if
either is wrong, enrollment and login fail with errors that look like browser bugs.

### Recovery

If every passkey for the instance is lost, mint a fresh admin link from the host:

```sh
docker exec dowitcher /dowitcher invite          # admin link
docker exec dowitcher /dowitcher invite --normal # ordinary user link
```

Admins can also mint a recovery link for one user from the UI, which enrolls a new passkey onto that
account without changing who they are.

## Development

```sh
just run                  # both servers, [api]/[web] prefixed
go test ./...
cd web && pnpm typecheck
cd web && pnpm build      # must run before `go build`; the binary embeds web/dist
```

`just run` sets `DOWITCHER_DEV_AUTH=dev`, which **disables authentication entirely** and treats every
request as an admin named `dev`. It exists so the UI can be worked on without a passkey ceremony in
the loop. It prints a banner on every start, and Dowitcher refuses to boot if it is set while
`DOWITCHER_ORIGIN` is `https://` — that combination can only mean it escaped into a real deployment.
Never set it anywhere but a local machine.

## License

See `LICENSE`.
