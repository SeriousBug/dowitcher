# Dowitcher

A self-hosted comic reader. Read all your comics in 
the browser, sync your reading position across devices, and
share them with others.
Ships as a single static binary in a tiny Docker container.

![A shared collection of graphic novels in Dowitcher](docs/images/collection.avif)

## Features

- **Auto-import library** Watches a folder to import all comics dropped into it.
- **Reading position sync** Across all devices. Start reading on your laptop, finish on your phone.
- **Broad support** Including cbz/cbr/cb7, PDFs, and just a folder full of images.
- **Organization** Tags, collections, and reading lists help organize your library. You can share your reading lists with others too.
- **Offline mode** Save Dowitcher on your home page as a PWA, and download comics for offline viewing.
- **MCP** Optionally, give an AI agent access to your library to help you keep it organized.

![A shared reading list with its comics in reading order](docs/images/reading-list.avif)

![Comics downloaded for offline reading](docs/images/downloads.avif)

![The import page, with its drop zone and duplicate detection options](docs/images/import.avif)

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
| `DOWITCHER_MCP` | unset | Set to `1` to expose the MCP server at `/mcp`. See below |
| `DOWITCHER_DEV_AUTH` | unset | **Development only.** See below |

`DOWITCHER_ORIGIN` and `DOWITCHER_RP_ID` are the two that matter. Passkeys are bound to an origin, so if
either is wrong, enrollment and login fail with errors that look like browser bugs.

### MCP server

Dowitcher can expose your library as an [MCP](https://modelcontextprotocol.io) server so you can point
an AI agent (Claude, etc.) at your instance and manage comics conversationally like "tag all Batman comics with their release years."
This is **off by default**, set
`DOWITCHER_MCP=1` to turn it on and then check your settings page for the setup link.

### Recovery

If every passkey for the instance is lost, mint a fresh admin link from the host:

```sh
docker exec dowitcher /dowitcher invite          # admin link
docker exec dowitcher /dowitcher invite --normal # ordinary user link
```

Admins can also mint a recovery link for one user from the settings page. 

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

