# Stashy

File storage service with multi-protocol API (gRPC, gRPC-Web, Connect, REST).

## Service

`stashy.v1alpha1.StorageService` — upload, download, replace, delete, and publish files.

| RPC | Method | Path |
|---|---|---|
| `CreateFile` | `POST` | `/v1/files` |
| `GetFile` | `GET` | `/v1/files/{id}` |
| `UpdateFile` | `PUT` | `/v1/files/{id}` |
| `DeleteFile` | `DELETE` | `/v1/files/{id}` |
| `PublishFile` | `POST` | `/v1/files/{id}/publish` |
| `UnpublishFile` | `POST` | `/v1/files/{id}/unpublish` |

## Links

- [GitHub](https://github.com/stashysh/stashy)
