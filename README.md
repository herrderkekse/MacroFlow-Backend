# MacroFlow Backend

A minimal, self-hosted **change-log relay** that synchronises
[MacroFlow](https://github.com/herrderkekse)'s local-first SQLite data across a
single user's devices.

The server never interprets your data. It stores an append-only, per-user,
strictly-increasing log of row changes and hands them back in order; the
clients do all conflict resolution (per-row last-write-wins). The full protocol
is specified in [SYNC.md](SYNC.md).

- **Language:** Go (standard library + pure-Go SQLite, no cgo)
- **Storage:** a single SQLite file
- **Auth:** HTTP Basic. Users are either provisioned out-of-band (`USERS`) or
  self-registered via the auth endpoints (stored bcrypt-hashed).
- **Deploy:** one static binary in a distroless container

## Endpoints

**Accounts** — unauthenticated (the caller has no account yet), JSON body
`{username, password}`:

| Method & path | Purpose |
|---|---|
| `POST /api/v1/auth/register` | Create an account. `201` / `409` (taken) / `400` (invalid) / `403` (signup disabled). |
| `POST /api/v1/auth/login` | Verify credentials ("Sign In" in the app). `200 {username}` / `401`. |

Usernames are case-insensitive (stored lower-cased), 3–64 chars of letters,
digits, `.`, `_`, `-`; passwords are 8–72 bytes.

**Sync** — all under `/api/v1/sync`, all requiring HTTP Basic auth (a
registered account or a `USERS` entry):

| Method & path | Purpose |
|---|---|
| `GET /ping` | Auth check ("Test Connection" in the app). `200` / `401`. |
| `GET /usage` | Storage used by the user: `{bytes, rows, quota}` (`quota` 0 = unlimited). |
| `GET /changes?after=<cursor>&limit=<n>` | Pull changes with `seq > after`, ascending. |
| `POST /changes` | Append a batch of changes (`{deviceId, changes[]}`). |

`POST /changes` returns `507 Insufficient Storage` (with `{error, bytes, quota}`)
when the batch would leave the user over `MAX_USER_BYTES`; the batch is rejected
atomically. Delete-only batches are always accepted so a user can free space.

**Contact** — unauthenticated, used by the website's contact form:

| Method & path | Purpose |
|---|---|
| `POST /api/v1/contact` | Store a contact-form submission (`{type, name, email, subject, message}`). `201` / `400` (invalid) / `429` (rate-limited). |

`type` identifies the submitting form so the admin can tell them apart; known
values are `contact` and `support` (extend `contactTypes` in
`internal/api/contact.go` when a new form is added). Submissions are
rate-limited per client IP (5 per 15 minutes) and validated (name ≤ 100 chars,
valid email ≤ 254 chars, subject ≤ 200 chars, message ≤ 5000 chars). The endpoint
sends CORS headers so the website can call it cross-origin; allowed origins are
configured via `CORS_ORIGINS`. Stored messages are read and deleted through the
admin API.

**Share** — hands a food/recipe/log payload from one account to anyone with
the resulting link, no account required to view/import it:

| Method & path | Purpose |
|---|---|
| `POST /api/v1/share` | Create a share (HTTP Basic auth required). Body `{kind: "food"\|"recipe"\|"log", version, payload}`. Returns `201 {token, url}` / `400` (invalid kind/version) / `413` (payload over `SHARE_MAX_BYTES`) / `429` (rate-limited). |
| `GET /api/v1/share/{token}` | Fetch a share's payload as JSON (unauthenticated). `200 {kind, version, payload, createdAt}` / `404` (missing or expired). |
| `GET /s/{token}` | The human-facing link (what's encoded in the QR). Unauthenticated `302` redirect to `macroflow://share/{token}`, or `404`. Deliberately not a rendered landing page — a plain redirect is all a browser needs to hand off to the app, and it's what keeps the QR scannable by stock camera apps, which only auto-recognize `http(s)://` URLs. |

Shares expire after `SHARE_TTL_DAYS` (default 30) and are garbage-collected
opportunistically on the next create. Creating a share is rate-limited per
account (20/hour); the two read endpoints are rate-limited per IP (60/5min).

Plus an unauthenticated `GET /healthz` for orchestrator probes.

## Configuration

All via environment variables (see [.env.example](.env.example)):

| Variable | Default | Notes |
|---|---|---|
| `USERS` | — | **Required.** `user:pass` pairs, comma-separated. |
| `USERS_FILE` | — | Optional file of `user:pass` lines (Docker secrets). Merged with `USERS`. |
| `ALLOW_SIGNUP` | `true` | When `false`, `POST /auth/register` returns `403`. Existing accounts and login are unaffected. |
| `PORT` | `8080` | Listen port. |
| `DB_PATH` | `./data/macroflow.db` (`/data/macroflow.db` in Docker) | SQLite file. |
| `MAX_BODY_BYTES` | `33554432` (32 MiB) | Push body cap; keep above 20 MB for base64 photos. |
| `MAX_LIMIT` | `1000` | Server cap on a pull's `limit`. |
| `MAX_USER_BYTES` | `0` | Per-user stored-size cap in bytes (`0` = unlimited). Over-cap pushes get `507`. |
| `ADMIN_ADDR` | — (disabled) | Admin dashboard/API listener, e.g. `127.0.0.1:8081` (`:8081` in Docker). **Keep it host-only** — see below. |
| `CORS_ORIGINS` | `*` | Origins allowed to call the contact endpoint from a browser, comma-separated. Set to the website origin in production, e.g. `https://macro-flow.org`. |
| `SHARE_MAX_BYTES` | `65536` (64 KiB) | Max share payload size in bytes. |
| `SHARE_TTL_DAYS` | `30` | Days a share stays retrievable before it's garbage-collected. |
| `PUBLIC_ORIGIN` | — (derived from the request) | The externally-reachable `scheme://host` used to build share URLs. Set this behind a TLS-terminating reverse proxy, where the request this server sees is plain HTTP. |

The server refuses to start with no users configured.

> **Security:** self-registered account passwords are stored bcrypt-hashed.
> `USERS`/`USERS_FILE` passwords are compared in constant time but stored as
> given (plaintext in env/file). Either way, Basic auth over plain HTTP exposes
> credentials on the wire — terminate TLS in front of this service (reverse
> proxy). Set `ALLOW_SIGNUP=false` on servers that should not accept new
> accounts.

## Admin dashboard

Setting `ADMIN_ADDR` starts a second listener serving a web dashboard (server
health, per-user storage/devices/quota, request counters) and a management API
(reset account passwords, wipe a user's data, delete accounts).

It is **unauthenticated by design**: access control is network reachability.
Bind it to loopback — never the public interface. The bundled
`docker-compose.yml` publishes it as `127.0.0.1:8081:8081`, so it is reachable
only from the server itself; from your machine, tunnel in and open
<http://localhost:8081>:

```bash
ssh -L 8081:localhost:8081 your-server
```

The management API is plain JSON if you prefer curl over the UI:

| Method & path | Purpose |
|---|---|
| `GET /api/admin/overview` | Uptime, storage totals, request counters, config. |
| `GET /api/admin/users` | All users (static/account/orphaned) with per-user stats. |
| `POST /api/admin/users/{name}/password` | Reset an account's password (`{"password": "..."}`). |
| `DELETE /api/admin/users/{name}/data` | Wipe a user's stored change log (account kept; devices re-upload). |
| `DELETE /api/admin/users/{name}` | Delete an account and all its data. |
| `GET /api/admin/contact` | List contact-form submissions, newest first. |
| `DELETE /api/admin/contact/{id}` | Delete one contact-form submission. |

Static `USERS` entries are configuration, not accounts: password reset and
delete return `409` for them (edit the environment instead); wiping their data
works.

The dashboard is a small Vite + React app in [admin-ui/](admin-ui/), compiled
into the binary via `go:embed`. The Docker build does this automatically; for
a local binary with the UI, run `npm ci && npm run build` in `admin-ui/`
first (without it, the JSON API still works and `/` explains itself). UI
development: `ADMIN_ADDR=127.0.0.1:8081 USERS=alice:secret go run .` in one
terminal, `npm run dev` in `admin-ui/` in another (the dev server proxies
`/api` to the Go listener).

## Run with Docker

```bash
# Build
docker build -t macroflow-sync .

# Run (persist the log in a named volume)
docker run -d --name macroflow-sync \
  -p 8080:8080 \
  -e USERS="alice:$(openssl rand -base64 18)" \
  -v macroflow-data:/data \
  macroflow-sync
```

Or with Compose:

```bash
export USERS="alice:change-me"
docker compose up -d
```

In the MacroFlow app's **Settings → Sync**, set the server URL to
`https://your-host` (the client appends the `/api/v1/sync/...` paths), then
enter the username and password.

## Run locally (development)

```bash
USERS="alice:secret" go run .
# → listening on :8080, db ./data/macroflow.db

# Smoke test
curl -u alice:secret localhost:8080/api/v1/sync/ping -i
```

## Test

```bash
go test ./...
```

The tests cover auth, push/pull round-trips, seq monotonicity, compaction
(latest-per-row incl. delete tombstones), user isolation, pagination,
all-or-nothing batch validation, and share create/fetch/redirect/expiry.

## How it works

- Each accepted change gets the next per-user `seq` (strictly increasing, never
  reused), stored with its `deviceId`.
- **Compaction:** only the latest change per `(user, table, row)` is kept — a
  unique index enforces one live row per key, and pushes upsert onto it. New or
  reset clients bootstrap from the compacted log.
- A push is one SQLite transaction: a malformed or failed batch stores nothing.
- Users' logs are fully isolated by `user_id` derived from auth.

## Project layout

```
main.go                 process wiring, graceful shutdown, -healthcheck
internal/config         env parsing, constant-time auth
internal/store          SQLite change log (pull, push, compaction)
internal/api            HTTP routing, Basic-auth middleware, handlers, admin API
admin-ui                admin dashboard (Vite + React), embedded via go:embed
Dockerfile              UI build → static Go build → distroless/static (nonroot)
docker-compose.yml      one-command deploy with a persistent volume
```
