# Stashy

File storage service with multi-protocol API (gRPC, gRPC-Web, Connect, REST).

## Service

`stashy.v1alpha1.StorageService` — upload and download files via streaming RPCs.

| RPC | Method | Path |
|---|---|---|
| `CreateFile` | `POST` | `/api/v1/files` |
| `GetFile` | `GET` | `/api/v1/files/{id}` |

## Links

- [GitHub](https://github.com/stashysh/stashy)
