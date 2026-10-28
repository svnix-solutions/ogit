# How oGit is built

oGit is a Go server (module `github.com/labbs/ogit`) with a React web UI embedded in the binary. This page is a map of the code; [Development](./development) covers building, testing and sending changes.

## Layers

```txt
cmd/cmd.go            the CLI: server, migration, admin, storage, runner
interfaces/           HTTP (Fiber), SSH, CLI: parse requests, call the application, map errors
application/          the rules: who may do what, and what happens
domain/               entities and the persistence interfaces
infrastructure/       config, database (GORM), storage (local, S3), git, mail, webhooks, the runner…
web/                  the React app (Vite, TypeScript, Tailwind), embedded from web/dist
docs/                 this site (VitePress)
```

Requests come in through `interfaces/`, which only translates: HTTP handlers and DTOs in `interfaces/http/v1/<area>`, git Smart HTTP in `interfaces/http/git`, SSH in `interfaces/ssh`. The decisions live in `application/`: each area's main file starts with a comment listing its rules (for example `application/pull_request_app.go`, `wiki_app.go`, `branch_protection.go`). Read that comment first when you change an area.

Application errors map to HTTP statuses in one place, `interfaces/http/v1/apierror`, so handlers stay small.

`interfaces/cli/server/server.go` wires everything together (`setupDeps`), and the end-to-end tests build the same app.

## Git without a git binary

oGit never shells out to `git`. It uses [go-git](https://github.com/go-git/go-git) on a `storer.Storer`, which is either a bare repository on disk or oGit's own S3 storer:

- **Fetch negotiation and pushes** are oGit's own on top of go-git (`infrastructure/helpers/git`), because go-git's server doesn't negotiate and can't resolve thin packs.
- **Merges** (`infrastructure/gitmerge`) work file by file in memory, and are written as one pack only when they succeed.
- **Browsing** (`infrastructure/gitbrowse`) reads trees, files, history and diffs directly from objects, with limits on everything.
- **Refs move by compare-and-swap**, so concurrent pushes and merges never overwrite each other, on S3 too (conditional writes).

## Storage

Repositories are addressed by path (`<namespace>/<slug>`), but stored by a **storage key** that never changes (the repository's id), so renames and moves touch no data. `infrastructure/storage` defines the interface; `local/` and `s3/` implement it. On S3, each push is a pack read with ranged requests through a cache, and `ogit storage gc` merges packs.

## Database

GORM over SQLite or PostgreSQL. The schema is goose migrations written in Go (`infrastructure/migration/files`), one per change. SQLite doesn't enforce foreign keys here, so deletions remove dependent rows explicitly.

## The web UI

`web/` is a React 19 single-page app. Pages are lazy-loaded routes (`web/src/app.tsx`), they read data with TanStack Query through `web/src/lib/`, and sign in with an HttpOnly session cookie plus a CSRF header. The server serves `web/dist` and answers unknown paths with `index.html`, under a strict content security policy: no inline scripts, no third-party origins.

## Security rules that span the code

- **Never disclose private repositories:** a caller who can't read one gets the same 404 as for a missing one, everywhere.
- **Sign-in never reveals whether an account exists:** unknown emails and wrong passwords get the same answer, in the same time.
- **Only hashes of tokens are stored,** and tokens, passwords and session tokens are never logged.
- **Raw files never render as HTML** on the site's origin.
- **Outgoing connections** (webhooks, mirrors) are checked against private addresses on the address actually dialed.
- **Sensitive actions need a recent confirmation** (sudo).
