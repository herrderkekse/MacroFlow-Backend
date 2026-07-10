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
- **Auth:** HTTP Basic, users provisioned out-of-band (no signup)
- **Deploy:** one static binary in a distroless container

## Endpoints

All under `/api/v1/sync`, all requiring HTTP Basic auth:

| Method & path | Purpose |
|---|---|
| `GET /ping` | Auth check ("Test Connection" in the app). `200` / `401`. |
| `GET /changes?after=<cursor>&limit=<n>` | Pull changes with `seq > after`, ascending. |
| `POST /changes` | Append a batch of changes (`{deviceId, changes[]}`). |

Plus an unauthenticated `GET /healthz` for orchestrator probes.

## Configuration

All via environment variables (see [.env.example](.env.example)):

| Variable | Default | Notes |
|---|---|---|
| `USERS` | — | **Required.** `user:pass` pairs, comma-separated. |
| `USERS_FILE` | — | Optional file of `user:pass` lines (Docker secrets). Merged with `USERS`. |
| `PORT` | `8080` | Listen port. |
| `DB_PATH` | `./data/macroflow.db` (`/data/macroflow.db` in Docker) | SQLite file. |
| `MAX_BODY_BYTES` | `33554432` (32 MiB) | Push body cap; keep above 20 MB for base64 photos. |
| `MAX_LIMIT` | `1000` | Server cap on a pull's `limit`. |

The server refuses to start with no users configured.

> **Security:** passwords are compared in constant time, but stored as given
> (plaintext in env/file). Terminate TLS in front of this service (reverse
> proxy) — Basic auth over plain HTTP exposes credentials.

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
(latest-per-row incl. delete tombstones), user isolation, pagination, and
all-or-nothing batch validation.

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
internal/api            HTTP routing, Basic-auth middleware, handlers
Dockerfile              static build → distroless/static (nonroot)
docker-compose.yml      one-command deploy with a persistent volume
```
