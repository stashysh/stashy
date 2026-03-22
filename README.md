# Stashy

Object storage gateway exposing a gRPC/Connect API with REST transcoding.

## Quick start

```bash
go run ./cmd/stashy
```

## Environment variables

| Variable | Description | Default |
|---|---|---|
| `PORT` | Server listen port | `8080` |
| `STORAGE_BACKEND` | Storage backend: `memory`, `local`, or `gcs` | `memory` |
| `LOCAL_STORAGE_DIR` | Directory for local file storage | `./storage` |
| `GCS_BUCKET` | GCS bucket name (required when `STORAGE_BACKEND=gcs`) | — |

GCS authentication uses [Application Default Credentials](https://cloud.google.com/docs/authentication/application-default-credentials).

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
curl -X POST http://localhost:8080/v1/files \
  -H "Content-Type: image/png" \
  --data-binary @photo.png
```

### Download a file

```bash
curl http://localhost:8080/v1/files/{id}
```

### Direct file access

```bash
curl http://localhost:8080/{id}
```

Ideal for CDN or subdomain mapping (e.g., `cdn.example.com/{id}`).

## Storage backends

- **memory** — in-memory, ephemeral; good for development
- **local** — files on disk at `LOCAL_STORAGE_DIR`
- **gcs** — Google Cloud Storage
