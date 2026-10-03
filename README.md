# Stashy

Self-hosted file storage service with multi-protocol API.

## Quick start

```bash
export SESSION_SECRET="your-secret-here"
export GOOGLE_CLIENT_ID="your-client-id"
export GOOGLE_CLIENT_SECRET="your-client-secret"

just run
# or: go run ./cmd/stashy serve --migrate
```

Visit `http://localhost:8080` to sign in and generate API keys.
Uses SQLite by default, with PostgreSQL available when you need a separate database.

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
| `DATABASE_URL` | Database connection string (see below) | `file:stashy.db` |
| `STORAGE_BACKEND` | Storage backend: `memory`, `local`, `gcs`, or `s3` | `memory` |
| `LOCAL_STORAGE_DIR` | Directory for local file storage | `./storage` |
| `GCS_BUCKET` | GCS bucket name (required when `STORAGE_BACKEND=gcs`) | — |
| `S3_BUCKET` | S3 bucket name (required when `STORAGE_BACKEND=s3`) | — |
| `SESSION_SECRET` | HMAC key for signing session cookies | required |
| `GOOGLE_CLIENT_ID` | Google OAuth 2.0 client ID | required |
| `GOOGLE_CLIENT_SECRET` | Google OAuth 2.0 client secret | required |
| `ALLOWED_DOMAINS` | Comma-separated list of allowed email domains | — (all allowed) |

## Database

Driver is auto-detected from the DSN:

| DSN | Database |
|---|---|
| `file:stashy.db` | SQLite (default) |
| `postgres://user:pass@host/db` | PostgreSQL |

Migrations are managed by [goose](https://github.com/pressly/goose). Run them explicitly:

```bash
stashy migrate
# or: stashy serve --migrate
```

## CLI

```
stashy serve [--migrate]   # start the server (default), optionally run migrations first
stashy migrate             # run database migrations and exit
stashy version             # print version
stashy help                # print usage
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

Files are private by default. Use `POST /v1/files/{id}/publish` to make a file publicly accessible at `/{id}`. Logged-in users can access any file via direct link.

Set `ALLOWED_DOMAINS` to restrict login to specific email domains.

## Protocols

A single endpoint serves all protocols via [vanguard-go](https://github.com/connectrpc/vanguard-go) transcoding:

| Protocol | Transport |
|---|---|
| gRPC | HTTP/2 (h2c) |
| gRPC-Web | HTTP/1.1 or HTTP/2 |
| Connect | HTTP/1.1 or HTTP/2 |
| REST | HTTP/1.1 or HTTP/2 |

## API

The API only works with your own files: every endpoint below acts on files uploaded with your API keys.

### Upload a file

```bash
curl -H "Authorization: Bearer <api-key>" \
  -X POST http://localhost:8080/v1/files \
  -H "Content-Type: image/png" \
  --data-binary @photo.png
```

Upload, replace, and update return the file's metadata:

```json
{
  "id": "V1StGXR8_Z5jdHi6B-myT",
  "url": "http://localhost:8080/V1StGXR8_Z5jdHi6B-myT",
  "content_type": "image/png",
  "size": "48213",
  "public": false,
  "slug": "",
  "created_at": "2026-10-02T12:00:00Z",
  "updated_at": "2026-10-02T12:00:00Z"
}
```

`size` is a string, as protobuf JSON encodes 64-bit integers.

### List files

```bash
curl -H "Authorization: Bearer <api-key>" \
  "http://localhost:8080/v1/files?limit=50"
```

Returns a JSON array of your files, newest first. To get the next page, pass
the last file's `id` as `after`; a page shorter than `limit` is the last.

### Get a file

```bash
curl -H "Authorization: Bearer <api-key>" \
  http://localhost:8080/v1/files/{id}
```

Returns the file's metadata.

### Download a file

```bash
curl -H "Authorization: Bearer <api-key>" \
  -o photo.png http://localhost:8080/v1/files/{id}/content
```

Supports `Range` requests.

### Replace a file

```bash
curl -H "Authorization: Bearer <api-key>" \
  -X PUT http://localhost:8080/v1/files/{id} \
  -H "Content-Type: image/png" \
  --data-binary @photo-v2.png
```

### Update a file

```bash
curl -H "Authorization: Bearer <api-key>" \
  -X PATCH http://localhost:8080/v1/files/{id} \
  -H "Content-Type: application/json" \
  -d '{"slug": "my-photo"}'
```

Updates a file's fields. Currently supports `slug`, a human-readable name;
send `{"slug": ""}` to clear it.

### Delete a file

```bash
curl -H "Authorization: Bearer <api-key>" \
  -X DELETE http://localhost:8080/v1/files/{id}
```

### Publish a file

```bash
curl -H "Authorization: Bearer <api-key>" \
  -X POST http://localhost:8080/v1/files/{id}/publish
```

### Unpublish a file

```bash
curl -H "Authorization: Bearer <api-key>" \
  -X POST http://localhost:8080/v1/files/{id}/unpublish
```

## MCP

Stashy serves a [Model Context Protocol](https://modelcontextprotocol.io) server at `/mcp`, so AI assistants can work with your files. It uses the same API keys as the REST API.

```bash
claude mcp add --transport http stashy http://localhost:8080/mcp \
  --header "Authorization: Bearer <api-key>"
```

Tools: `list_files`, `get_file`, `update_file`, `publish_file`, `unpublish_file`, `delete_file`. They work with file metadata; to upload or download content, use the REST API.

## File access

```bash
curl http://localhost:8080/{id}
```

Published files are accessible to anyone. Private files require a login session.
Ideal for CDN or subdomain mapping (e.g., `cdn.example.com/{id}`).

If a file has a slug, its canonical URL is `/{id}/{slug}` and `/{id}` redirects
there. A stale or wrong slug also redirects to the current canonical URL, so
links shared before a rename keep working.

## Storage backends

- **memory** — in-memory, ephemeral; good for development
- **local** — files on disk at `LOCAL_STORAGE_DIR`
- **gcs** — Google Cloud Storage
- **s3** — Amazon S3 or S3-compatible storage (MinIO, R2, etc.)
