# Dowitcher

Self-hosted comic reader. Reads CBZ, watches a library folder, syncs reading position across
devices, and imports a folder of images into a deduped CBZ. Passkey-only login. Ships as a
single static Go binary with the web UI embedded (distroless/static base).

`dowitcher` is a placeholder name. Renaming means the module path in `go.mod`, the `DOWITCHER_*`
env prefix, `cmd/dowitcher/`, and the cookie names — nothing structural.

## Stack

- **Backend:** Go 1.26 (module `github.com/SeriousBug/dowitcher`, `internal/` packages). HTTP via
  the stdlib `net/http` mux with method+path patterns (e.g. `"DELETE /api/comics/{id}"`). No
  router library.
- **Frontend:** TypeScript + React 19 SPA in `web/`. TanStack Router (code-defined) + TanStack
  Query, Ark UI headless components, Panda CSS, `lucide-react` icons, Vite build. Package
  manager is `pnpm`.
- **DB:** SQLite via `modernc.org/sqlite` (pure Go — this is what keeps `CGO_ENABLED=0` and the
  distroless image possible; do not swap in a CGO driver).
- **Auth:** WebAuthn passkeys only, no passwords. Session cookie. First run prints a one-time
  admin enrollment link to the logs; admins mint single-use invite links from the UI.

## Layout

- `cmd/dowitcher/` — entrypoint, subcommands, background pollers.
- `internal/api/` — shared request/response types (`types.go`). **Source of truth for TS types.**
- `internal/server/` — HTTP handlers, routing (`server.go`), auth middleware (`middleware.go`,
  `requireAuth`/`requireAdmin`), `ws.go` hub. Handlers split per domain as `<domain>_handlers.go`.
- `internal/auth/` — WebAuthn, invites, sessions, users.
- `internal/store/` — SQLite persistence (`sqlite.go`, `migrations.go`, `accessors.go`).
- `internal/cbz/` — CBZ reading: page listing, page extraction, ComicInfo.xml, cover.
- `internal/library/` — filesystem scanner + fsnotify watcher over the library root.
- `internal/imports/` — the image-folder import pipeline (dedupe, sort, package).
- `web/src/routes/` — page components. `web/src/api/http.ts` — fetch wrapper (`http.get/post/put/del`).
  `web/src/auth/AuthProvider.tsx` — `useAuth()` gives the current `user`.
- `web/embed.go` — embeds `web/dist` into the Go binary via `//go:embed all:dist`.

## Type generation

TS API types in `web/src/api/generated.ts` are generated from Go structs in `internal/api` via
**tygo** (`tygo.yaml`), run from the repo root. `generated.ts` is **committed** — the Docker
build never runs tygo. After changing Go API types, regenerate rather than hand-editing:

```sh
go run github.com/gzuidhof/tygo@v0.2.17 generate
```

## Build / check

```sh
just run                  # both servers with [api]/[web] prefixes
go test ./...
cd web && pnpm typecheck  # panda codegen + tsc --noEmit
cd web && pnpm build      # panda codegen + vite build -> web/dist
go build ./...            # needs web/dist to exist for the embed
```

`pnpm build` before `go build`, always. Every pnpm script that touches types runs `panda codegen`
first, because `styled-system/` is gitignored and absent on a fresh clone.

## Conventions

- **Comments explain _why_, never _what_.** Every non-obvious decision carries its rationale.
  This is the strongest convention here; match it.
- Errors: `writeJSON(w, status, v)` / `writeErr(w, status, msg)` with `api.APIError`. Internal
  failures flatten to `"db error"` (details go to the log); user-actionable failures get
  specific text the frontend shows verbatim. Sentinel errors per package, matched with
  `errors.Is`. `{"ok": true}` is the success body for actions.
- Access control is a table: auth is applied per-route at the registration site in `server.go`
  via `s.requireAuth(...)` / `s.requireAdmin(...)`, so the route list reads as who-can-do-what.
- Store methods return `api.*` types and translate `sql.ErrNoRows` into `store.ErrNotFound` at
  the boundary. `database/sql` never leaks upward.
- Migrations are an append-only `[]string` in `migrations.go`; the slice index _is_ the schema
  version. **Never edit an existing entry.** Timestamps are `INTEGER` Unix seconds, booleans
  `INTEGER 0/1`, primary keys `TEXT` random tokens, ownership via `ON DELETE CASCADE`.
- Work that outlives its request uses `detached(r)` (`context.WithoutCancel`) and reports over
  the WebSocket, not the response.
- Reads are HTTP; live state is WebSocket. REST for resources, `POST` to a verb sub-path for
  actions (`POST /api/imports/{id}/cancel`).
- Frontend: named exports, `PascalCase.tsx` components, no barrels, props typed inline, query
  keys as inline literal arrays. Destructive actions go through `components/ConfirmDialog.tsx`
  (`tone="danger"`); outcomes via `lib/toaster.ts`. `aria-label` + `title` on icon-only buttons.
- Go tests are stdlib `testing` only, colocated, real SQLite in `t.TempDir()`, no mocks
  framework, no assertion library.

## Sharing model

A comic is visible to a user if they uploaded it, or if it sits in a collection whose `shared`
flag is on, or if it came from the watched library root (which is server-wide by definition).
Collections are private by default and the owner opts in per collection. Visibility is enforced
in SQL in the store layer, not in handlers — see `store.visibleComics`.

`comics.source` is what decides server-wide, not a NULL `owner_id`: an admin can **claim** a
library comic (`POST /api/comics/{id}/claim`), which sets `owner_id` and flips the source from
`library` to `claimed`. A claimed comic drops out of the `source='library'` arm and so is
visible only to its claimer, who can share it through a collection or hand it back with
`/unclaim`. The file never moves — a claimed path is still relative to the library root, and
the scanner still refreshes and diffs the row, it just never writes `owner_id` or `source`
(`UpsertComic`'s `ON CONFLICT` omits both).

Tags are **per-user**: `tags` is keyed `UNIQUE(user_id, name)` and a tag is only ever readable
by the user who wrote it. Seeing a comic is therefore the only requirement to tag it — a tag
cannot affect what anyone else reads. Every tag query filters on `tags.user_id` *and* the
visibility fragment, since a comic can stop being visible after it was tagged.
