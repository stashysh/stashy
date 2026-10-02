# Stashy

File storage service with multi-protocol API (gRPC, gRPC-Web, Connect, REST).

## Service

`stashy.v1.FileService` — list, get, upload, download, replace, update, delete, and publish files.

| RPC | Method | Path |
|---|---|---|
| `ListFiles` | `GET` | `/v1/files` |
| `CreateFile` | `POST` | `/v1/files` |
| `GetFile` | `GET` | `/v1/files/{id}` |
| `GetFileContent` | `GET` | `/v1/files/{id}/content` |
| `ReplaceFile` | `PUT` | `/v1/files/{id}` |
| `UpdateFile` | `PATCH` | `/v1/files/{id}` |
| `DeleteFile` | `DELETE` | `/v1/files/{id}` |
| `PublishFile` | `POST` | `/v1/files/{id}/publish` |
| `UnpublishFile` | `POST` | `/v1/files/{id}/unpublish` |

## Links

- [GitHub](https://github.com/stashysh/stashy)
