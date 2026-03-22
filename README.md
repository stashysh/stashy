# Stashy

Self-hosted file storage service with multi-protocol API.

## Quick start

```bash
export SESSION_SECRET="your-secret-here"
export GOOGLE_CLIENT_ID="your-client-id"
export GOOGLE_CLIENT_SECRET="your-client-secret"

just run
# or: go run ./cmd/stashy serve
```

Visit `http://localhost:8080` to sign in and generate API keys.
Uses SQLite by default — no external dependencies needed.

## Build

```bash
just build          # build binary
just generate       # regenerate proto code
just tidy           # go mod tidy
just clean          # remove build artifacts
```

## Docker

```bash
cp .env.example .env  # fill in your secrets
docker compose up
```

## Environment variables

| Variable | Description | Default |
|---|---|---|
| `PORT` | Server listen port | `8080` |
| `HOSTNAME` | Public base URL | `http://localhost:$PORT` |
| `DB_DSN` | Database connection string (see below) | `file:stashy.db` |
| `STORAGE_BACKEND` | Storage backend: `memory`, `local`, or `gcs` | `memory` |
| `LOCAL_STORAGE_DIR` | Directory for local file storage | `./storage` |
| `GCS_BUCKET` | GCS bucket name (required when `STORAGE_BACKEND=gcs`) | — |
| `SESSION_SECRET` | HMAC key for signing session cookies | required |
| `GOOGLE_CLIENT_ID` | Google OAuth 2.0 client ID | required |
| `GOOGLE_CLIENT_SECRET` | Google OAuth 2.0 client secret | required |

## Database

Driver is auto-detected from the DSN:

| DSN | Database |
|---|---|
| `file:stashy.db` | SQLite (default) |
| `file:stashy.db?authToken=T&syncUrl=libsql://db.turso.io` | Turso |
| `postgres://user:pass@host/db` | PostgreSQL |
| `mysql://user:pass@tcp(host)/db` | MySQL |

Migrations run automatically on startup via [goose](https://github.com/pressly/goose).

To run migrations independently (e.g. as a Cloud Run job):

```bash
stashy migrate
```

## CLI

```
stashy serve      # start the server (default)
stashy migrate    # run database migrations and exit
```

## Authentication

### Web UI (Google OAuth)

1. Visit `/` — sign in with Google
2. After login, the dashboard lets you generate and manage API keys

### API (Bearer token)

All `/v1/*` API endpoints require a Bearer token:

```bash
curl -H "Authorization: Bearer <api-key>" \
  -X POST http://localhost:8080/v1/files \
  -H "Content-Type: image/png" \
  --data-binary @photo.png
```

Direct file access at `/{uuid}` is public (no auth required).

## Protocols

A single endpoint serves all protocols via [vanguard-go](https://github.com/connectrpc/vanguard-go) transcoding:

| Protocol | Transport |
|---|---|
| gRPC | HTTP/2 (h2c) |
| gRPC-Web | HTTP/1.1 or HTTP/2 |
| Connect | HTTP/1.1 or HTTP/2 |
| REST | HTTP/1.1 or HTTP/2 |

## API

### Upload a file

```bash
curl -H "Authorization: Bearer <api-key>" \
  -X POST http://localhost:8080/v1/files \
  -H "Content-Type: image/png" \
  --data-binary @photo.png
```

### Download a file (via API)

```bash
curl -H "Authorization: Bearer <api-key>" \
  http://localhost:8080/v1/files/{id}
```

### Direct file access (public)

```bash
curl http://localhost:8080/{id}
```

Ideal for CDN or subdomain mapping (e.g., `cdn.example.com/{id}`).

## Storage backends

- **memory** — in-memory, ephemeral; good for development
- **local** — files on disk at `LOCAL_STORAGE_DIR`
- **gcs** — Google Cloud Storage
