package service

import (
	"fmt"
	"net/http"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"connectrpc.com/connect/v2/connectproto"
	"connectrpc.com/validate"
	"connectrpc.com/vanguard"

	"github.com/stashysh/stashy/gen/stashy/v1/stashyv1connect"
	"github.com/stashysh/stashy/internal/auth"
)

// Mount registers the API that agents use on mux: Connect, gRPC, and gRPC-Web
// under /stashy.v1.FileService/, REST under /v1/, and MCP at /mcp. Every
// route authenticates with API keys. version is reported to MCP clients.
func (s *FileService) Mount(mux *http.ServeMux, version string) error {
	return s.mount(mux, auth.RequireAPIKey(s.db), version)
}

// mount is Mount with the RPC and REST authentication supplied, so tests can
// set the user directly. MCP always checks API keys itself, via the SDK's
// bearer-token middleware, which answers in the form MCP clients expect.
func (s *FileService) mount(mux *http.ServeMux, authenticate func(http.Handler) http.Handler, version string) error {
	mux.Handle("/mcp", s.mcpHandler(version))

	server := connect.NewServer(validate.NewServerInterceptor())
	stashyv1connect.RegisterFileServiceHandler(server, s)

	// Uploads can arrive as one large message, so keep v1's unbounded
	// per-message limit.
	connecthttp.Mount(wrappingMux{mux, authenticate}, server, connecthttp.WithReadMaxBytes(0))

	// Vanguard installs a catch-all "/", so it gets its own mux under /v1/.
	rest := http.NewServeMux()
	err := vanguard.Mount(rest, server,
		// The limit covers a whole streamed request body, so the 4 MiB
		// default would cap uploads.
		vanguard.WithMaxReadBytes(0),
		vanguard.WithDiscardUnknownQueryParams(true),
		vanguard.WithCodec(func(res connectproto.TypeResolver) vanguard.RESTCodec {
			codec := vanguard.NewJSONCodec(res)
			codec.MarshalOptions.UseProtoNames = true
			codec.MarshalOptions.EmitUnpopulated = true
			codec.UnmarshalOptions.DiscardUnknown = true
			return codec
		}),
	)
	if err != nil {
		return fmt.Errorf("mounting REST routes: %w", err)
	}
	mux.Handle("/v1/", authenticate(rest))

	// Serve content reads and replacements directly rather than through
	// Vanguard: downloads need Range, HEAD, and conditional GET, and
	// replacements need If-Match with a 412 response.
	mux.Handle("GET /v1/files/{id}/content", authenticate(http.HandlerFunc(s.httpGetFileContent)))
	mux.Handle("PUT /v1/files/{id}/content", authenticate(http.HandlerFunc(s.httpUpdateFileContent)))
	return nil
}

// wrappingMux registers each handler on mux wrapped in wrap, so routes that
// connecthttp.Mount picks get the same middleware as ours.
type wrappingMux struct {
	mux  *http.ServeMux
	wrap func(http.Handler) http.Handler
}

func (m wrappingMux) Handle(pattern string, h http.Handler) {
	m.mux.Handle(pattern, m.wrap(h))
}
