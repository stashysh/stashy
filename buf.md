# Stashy

File storage service with multi-protocol API (gRPC, gRPC-Web, Connect, REST).

## Service

`stashy.v1.FileService` — create, list, get, update, and delete files.

| RPC | Method | Path |
|---|---|---|
| `CreateFile` | `POST` | `/v1/files` |
| `ListFiles` | `GET` | `/v1/files` |
| `GetFile` | `GET` | `/v1/files/{id}` |
| `GetFileContent` | `GET` | `/v1/files/{id}/content` |
| `UpdateFile` | `PATCH` | `/v1/files/{id}` |
| `UpdateFileContent` | `PUT` | `/v1/files/{id}/content` |
| `DeleteFile` | `DELETE` | `/v1/files/{id}` |

## Links

- [GitHub](https://github.com/stashysh/stashy)
