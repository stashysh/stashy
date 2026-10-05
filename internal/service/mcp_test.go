package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/stashysh/stashy/internal/db"
	"github.com/stashysh/stashy/internal/storage/memory"
)

type bearerTransport struct{ token string }

func (t bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+t.token)
	return http.DefaultTransport.RoundTrip(req)
}

// newMCPTest serves /mcp over HTTP and returns the service and a connected
// client session for each of two users.
func newMCPTest(t *testing.T) (*FileService, *mcp.ClientSession, *mcp.ClientSession) {
	t.Helper()

	database := newTestDB(t)
	svc := New(memory.New(), database, "http://example.test")
	srv := httptest.NewServer(svc.MCPHandler(database, "test"))
	t.Cleanup(srv.Close)

	connect := func(googleID string) *mcp.ClientSession {
		user, err := database.UpsertUser(t.Context(), googleID, googleID+"@example.com", googleID)
		if err != nil {
			t.Fatalf("UpsertUser: %v", err)
		}
		key, _, err := database.CreateAPIKey(t.Context(), user.ID, "test")
		if err != nil {
			t.Fatalf("CreateAPIKey: %v", err)
		}
		client := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil)
		session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
			Endpoint:   srv.URL,
			HTTPClient: &http.Client{Transport: bearerTransport{key}},
		}, nil)
		if err != nil {
			t.Fatalf("Connect: %v", err)
		}
		t.Cleanup(func() { session.Close() })
		return session
	}
	return svc, connect("alice"), connect("bob")
}

func callTool(t *testing.T, session *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()

	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return res
}

// toolFile calls a tool that returns a file and decodes its structured output.
func toolFile(t *testing.T, session *mcp.ClientSession, name string, args map[string]any) mcpFile {
	t.Helper()

	res := callTool(t, session, name, args)
	if res.IsError {
		t.Fatalf("%s: tool error: %s", name, toolText(res))
	}
	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var f mcpFile
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatalf("%s: decoding %s: %v", name, b, err)
	}
	return f
}

func toolText(res *mcp.CallToolResult) string {
	var parts []string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			parts = append(parts, tc.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func TestMCPRequiresAPIKey(t *testing.T) {
	database := newTestDB(t)
	svc := New(memory.New(), database, "http://example.test")
	h := svc.MCPHandler(database, "test")

	for _, header := range []string{"", "Bearer not-a-key"} {
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{}`))
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("Authorization %q: status = %d, want 401", header, rec.Code)
		}
	}
}

func TestMCPTools(t *testing.T) {
	svc, alice, bob := newMCPTest(t)

	tools, err := alice.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	annotations := map[string]*mcp.ToolAnnotations{}
	schemas := map[string]string{}
	for _, tool := range tools.Tools {
		annotations[tool.Name] = tool.Annotations
		b, _ := json.Marshal(tool.InputSchema)
		schemas[tool.Name] = string(b)
	}
	if len(annotations) != 4 {
		t.Errorf("got %d tools, want 4", len(annotations))
	}
	if !strings.Contains(schemas["update_file"], `"enum":["private","internal","public"],"type":"string"`) {
		t.Errorf("update_file schema should list the visibility values: %s", schemas["update_file"])
	}
	if !strings.Contains(schemas["list_files"], `"maximum":100,"minimum":1`) {
		t.Errorf("list_files schema should bound limit to 1-100: %s", schemas["list_files"])
	}
	for _, name := range []string{"list_files", "get_file", "update_file", "delete_file"} {
		if annotations[name] == nil {
			t.Errorf("tool %s missing or has no annotations", name)
		}
	}
	if a := annotations["delete_file"]; a == nil || a.DestructiveHint == nil || !*a.DestructiveHint {
		t.Errorf("delete_file should be marked destructive")
	}
	if a := annotations["update_file"]; a == nil || a.DestructiveHint == nil || *a.DestructiveHint {
		t.Errorf("update_file should be marked non-destructive")
	}

	created, err := svc.putFile(t.Context(), db.File{Owner: firstUserID(t, svc), ContentType: "text/markdown"}, strings.NewReader("# Notes"))
	if err != nil {
		t.Fatalf("putFile: %v", err)
	}

	got := toolFile(t, alice, "get_file", map[string]any{"id": created.ID})
	if got.Size != 7 || got.ContentType != "text/markdown" || got.Visibility != "internal" {
		t.Fatalf("get_file = %+v", got)
	}

	updated := toolFile(t, alice, "update_file", map[string]any{"id": created.ID, "slug": "notes.md", "name": "Notes.md"})
	if updated.URL != "http://example.test/"+created.ID+"/notes.md" || updated.Name != "Notes.md" {
		t.Fatalf("updated = %+v", updated)
	}

	if f := toolFile(t, alice, "update_file", map[string]any{"id": created.ID, "visibility": "public"}); f.Visibility != "public" {
		t.Fatalf("visibility = %q after update_file, want public", f.Visibility)
	}
	for _, visibility := range []any{"everyone", nil} {
		if res := callTool(t, alice, "update_file", map[string]any{"id": created.ID, "visibility": visibility}); !res.IsError {
			t.Fatalf("update_file with visibility %v: want a tool error", visibility)
		}
	}

	res := callTool(t, alice, "list_files", map[string]any{"limit": 10})
	if res.IsError || !strings.Contains(toolText(res), created.ID) {
		t.Fatalf("list_files missing the file: %s", toolText(res))
	}

	// Another user can't see or change the file.
	for _, name := range []string{"get_file", "update_file", "delete_file"} {
		if res := callTool(t, bob, name, map[string]any{"id": created.ID}); !res.IsError {
			t.Errorf("bob %s: want a tool error", name)
		}
	}

	if res := callTool(t, alice, "delete_file", map[string]any{"id": created.ID}); res.IsError {
		t.Fatalf("delete_file: %s", toolText(res))
	}
	if res := callTool(t, alice, "get_file", map[string]any{"id": created.ID}); !res.IsError {
		t.Fatalf("get_file after delete: want a tool error")
	}
}

func TestMCPToolsValidateInput(t *testing.T) {
	_, alice, _ := newMCPTest(t)

	for name, args := range map[string]map[string]any{
		"get_file":    {"id": "short"},
		"list_files":  {"limit": 101},
		"update_file": {"id": "AAAAAAAAAAAAAAAAAAAAA", "slug": "has spaces"},
	} {
		if res := callTool(t, alice, name, args); !res.IsError {
			t.Errorf("%s %v: want a tool error", name, args)
		}
	}
}

// firstUserID returns the id of the first user newMCPTest created (alice).
func firstUserID(t *testing.T, svc *FileService) string {
	t.Helper()

	user, err := svc.db.UpsertUser(t.Context(), "alice", "alice@example.com", "alice")
	if err != nil {
		t.Fatalf("UpsertUser: %v", err)
	}
	return user.ID
}
