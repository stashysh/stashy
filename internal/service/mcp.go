package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"buf.build/go/protovalidate"
	"connectrpc.com/connect"
	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/protobuf/proto"

	stashyv1 "github.com/stashysh/stashy/gen/stashy/v1"
	"github.com/stashysh/stashy/internal/auth"
	"github.com/stashysh/stashy/internal/db"
)

// MCPHandler serves the Model Context Protocol at /mcp. It authenticates with
// the same Bearer API keys as /v1/*, and its tools call the FileService
// methods so ownership checks and validation stay in one place.
func (s *FileService) MCPHandler(database *db.DB, version string) http.Handler {
	server := mcp.NewServer(&mcp.Implementation{Name: "stashy", Version: version}, nil)
	s.addMCPTools(server)

	handler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		// No tool calls back to the client, so there is no session state to keep.
		&mcp.StreamableHTTPOptions{Stateless: true},
	)

	verify := func(ctx context.Context, token string, _ *http.Request) (*mcpauth.TokenInfo, error) {
		key, err := database.LookupAPIKey(ctx, token)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid api key", mcpauth.ErrInvalidToken)
		}
		return &mcpauth.TokenInfo{UserID: key.UserID}, nil
	}
	// API keys don't expire.
	return mcpauth.RequireBearerToken(verify, &mcpauth.RequireBearerTokenOptions{AllowMissingExpiration: true})(handler)
}

// mcpFile is the tool-facing view of a file. Unlike the protobuf JSON
// mapping, size is a number.
type mcpFile struct {
	ID          string    `json:"id"`
	Slug        string    `json:"slug" jsonschema:"human-readable end of the URL; empty when not set"`
	URL         string    `json:"url" jsonschema:"canonical URL of the file"`
	Name        string    `json:"name" jsonschema:"original filename; empty when not set"`
	ContentType string    `json:"content_type"`
	Size        int64     `json:"size" jsonschema:"size in bytes"`
	Public      bool      `json:"public" jsonschema:"whether the URL works without signing in"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func toMCPFile(f *stashyv1.File) mcpFile {
	return mcpFile{
		ID:          f.Id,
		Slug:        f.Slug,
		URL:         f.Url,
		Name:        f.Name,
		ContentType: f.ContentType,
		Size:        f.Size,
		Public:      f.Public,
		CreatedAt:   f.CreatedAt.AsTime(),
		UpdatedAt:   f.UpdatedAt.AsTime(),
	}
}

type mcpFileID struct {
	ID string `json:"id" jsonschema:"file id"`
}

type mcpListFilesInput struct {
	Limit *int32  `json:"limit,omitempty" jsonschema:"maximum number of files to return, 1-100 (default 50)"`
	After *string `json:"after,omitempty" jsonschema:"id of the last file from the previous page"`
}

type mcpListFilesOutput struct {
	Files []mcpFile `json:"files" jsonschema:"newest first; fewer than limit means this is the last page"`
}

type mcpUpdateFileInput struct {
	ID   string  `json:"id" jsonschema:"file id"`
	Slug *string `json:"slug,omitempty" jsonschema:"human-readable name used in the file URL; empty string clears it"`
	Name *string `json:"name,omitempty" jsonschema:"original filename, e.g. Quarterly report.pdf; empty string clears it"`
}

type mcpEmpty struct{}

func (s *FileService) addMCPTools(server *mcp.Server) {
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: new(false)}
	idempotent := &mcp.ToolAnnotations{DestructiveHint: new(false), IdempotentHint: true, OpenWorldHint: new(false)}
	destructive := &mcp.ToolAnnotations{DestructiveHint: new(true), IdempotentHint: true, OpenWorldHint: new(false)}

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_files",
		Description: "List your files, newest first. Pass the last file's id as after to get the next page.",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, in mcpListFilesInput) (*mcp.CallToolResult, mcpListFilesOutput, error) {
		resp, err := callUnary(ctx, req, s.ListFiles, &stashyv1.ListFilesRequest{Limit: in.Limit, After: in.After})
		if err != nil {
			return nil, mcpListFilesOutput{}, err
		}
		out := mcpListFilesOutput{Files: make([]mcpFile, 0, len(resp.Files))}
		for _, f := range resp.Files {
			out.Files = append(out.Files, toMCPFile(f))
		}
		return nil, out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_file",
		Description: "Get a file's metadata: URL, content type, size, and whether it is public.",
		Annotations: readOnly,
	}, func(ctx context.Context, req *mcp.CallToolRequest, in mcpFileID) (*mcp.CallToolResult, mcpFile, error) {
		resp, err := callUnary(ctx, req, s.GetFile, &stashyv1.GetFileRequest{Id: in.ID})
		if err != nil {
			return nil, mcpFile{}, err
		}
		return nil, toMCPFile(resp.File), nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "update_file",
		Description: "Update a file's name (its original filename) or slug (the human-readable end of its URL).",
		Annotations: idempotent,
	}, func(ctx context.Context, req *mcp.CallToolRequest, in mcpUpdateFileInput) (*mcp.CallToolResult, mcpFile, error) {
		resp, err := callUnary(ctx, req, s.UpdateFile, &stashyv1.UpdateFileRequest{Id: in.ID, Slug: in.Slug, Name: in.Name})
		if err != nil {
			return nil, mcpFile{}, err
		}
		return nil, toMCPFile(resp.File), nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "publish_file",
		Description: "Make a file public, so its URL works for anyone without signing in.",
		Annotations: idempotent,
	}, func(ctx context.Context, req *mcp.CallToolRequest, in mcpFileID) (*mcp.CallToolResult, mcpEmpty, error) {
		_, err := callUnary(ctx, req, s.PublishFile, &stashyv1.PublishFileRequest{Id: in.ID})
		return nil, mcpEmpty{}, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "unpublish_file",
		Description: "Make a public file private again.",
		Annotations: idempotent,
	}, func(ctx context.Context, req *mcp.CallToolRequest, in mcpFileID) (*mcp.CallToolResult, mcpEmpty, error) {
		_, err := callUnary(ctx, req, s.UnpublishFile, &stashyv1.UnpublishFileRequest{Id: in.ID})
		return nil, mcpEmpty{}, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "delete_file",
		Description: "Permanently delete a file. Its URL stops working.",
		Annotations: destructive,
	}, func(ctx context.Context, req *mcp.CallToolRequest, in mcpFileID) (*mcp.CallToolResult, mcpEmpty, error) {
		_, err := callUnary(ctx, req, s.DeleteFile, &stashyv1.DeleteFileRequest{Id: in.ID})
		return nil, mcpEmpty{}, err
	})
}

// callUnary validates msg the way the Connect interceptor does, then calls a
// unary FileService method as the tool caller.
func callUnary[Req, Res any](
	ctx context.Context,
	req *mcp.CallToolRequest,
	method func(context.Context, *connect.Request[Req]) (*connect.Response[Res], error),
	msg *Req,
) (*Res, error) {
	if err := protovalidate.Validate(any(msg).(proto.Message)); err != nil {
		return nil, err
	}
	owner, err := mcpOwner(req)
	if err != nil {
		return nil, err
	}
	resp, err := method(auth.ContextWithUserID(ctx, owner), connect.NewRequest(msg))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

// mcpOwner returns the user id that the bearer-token middleware attached.
func mcpOwner(req *mcp.CallToolRequest) (string, error) {
	if req.Extra == nil || req.Extra.TokenInfo == nil || req.Extra.TokenInfo.UserID == "" {
		return "", errors.New("authentication required")
	}
	return req.Extra.TokenInfo.UserID, nil
}
