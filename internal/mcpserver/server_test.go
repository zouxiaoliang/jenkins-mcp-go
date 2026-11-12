package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/zouxiaoliang/jenkins-mcp-go/internal/jenkins"
)

// connect builds a server against a fake controller and returns a live client
// session, so that tests exercise the real MCP path rather than the handlers.
func connect(t *testing.T, opts Options, handler http.Handler) *mcp.ClientSession {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	jc, err := jenkins.NewClient(jenkins.Options{BaseURL: srv.URL, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("jenkins.NewClient: %v", err)
	}
	mcpSrv := NewServer(jc, opts)

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	t1, t2 := mcp.NewInMemoryTransports()
	ctx := context.Background()
	serverSession, err := mcpSrv.Connect(ctx, t1, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { serverSession.Close() })

	cs, err := client.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func toolNames(t *testing.T, cs *mcp.ClientSession) map[string]bool {
	t.Helper()
	res, err := cs.ListTools(context.Background(), &mcp.ListToolsParams{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	out := make(map[string]bool, len(res.Tools))
	for _, tl := range res.Tools {
		out[tl.Name] = true
	}
	return out
}

var readToolNames = []string{
	"jenkins_info", "list_jobs", "get_job", "list_builds", "get_build",
	"get_console", "list_queue", "list_nodes", "jenkins_get_path",
}

var writeToolNames = []string{"trigger_build", "stop_build", "cancel_queue_item"}

// TestWriteGateHidesWriteTools is the core of ADR-0003: a read-only session must
// not advertise write tools at all, so an agent cannot even attempt them.
func TestWriteGateHidesWriteTools(t *testing.T) {
	cs := connect(t, Options{AllowWrite: false}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{}`))
	}))
	names := toolNames(t, cs)
	for _, n := range readToolNames {
		if !names[n] {
			t.Errorf("read tool %q is missing", n)
		}
	}
	for _, n := range writeToolNames {
		if names[n] {
			t.Errorf("write tool %q was advertised while the write gate is closed", n)
		}
	}
	if len(names) != len(readToolNames) {
		t.Errorf("advertised %d tools, want %d: %v", len(names), len(readToolNames), names)
	}
}

func TestWriteGateOpenAdvertisesWriteTools(t *testing.T) {
	cs := connect(t, Options{AllowWrite: true, AllowedJobs: []string{"ok"}},
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{}`))
		}))
	names := toolNames(t, cs)
	for _, n := range writeToolNames {
		if !names[n] {
			t.Errorf("write tool %q is missing with the gate open", n)
		}
	}
}

// TestErrorContractIsStructured proves a permission failure reaches the agent as
// a retryable=false forbidden code, not as an ambiguous message.
func TestErrorContractIsStructured(t *testing.T) {
	cs := connect(t, Options{}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`<html>Access Denied</html>`))
	}))
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "get_job",
		Arguments: map[string]any{"job": "Secret"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError for a forbidden response")
	}
	text := textOf(res)
	var payload struct {
		Error struct {
			Code      string `json:"code"`
			Retryable bool   `json:"retryable"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		t.Fatalf("error text is not JSON (%v): %s", err, text)
	}
	if payload.Error.Code != "forbidden" {
		t.Errorf("code = %q, want forbidden", payload.Error.Code)
	}
	if payload.Error.Retryable {
		t.Error("forbidden must not be reported as retryable")
	}
}

// TestNotFoundAndForbiddenStayDistinct is the "never turn unknown into zero"
// rule applied to HTTP: a 404 says the job does not exist, a 403 says it does.
func TestNotFoundAndForbiddenStayDistinct(t *testing.T) {
	statuses := map[int]string{404: "not_found", 403: "forbidden"}
	for status, wantCode := range statuses {
		cs := connect(t, Options{}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
		}))
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
			Name:      "get_job",
			Arguments: map[string]any{"job": "x"},
		})
		if err != nil {
			t.Fatalf("CallTool: %v", err)
		}
		text := textOf(res)
		if !strings.Contains(text, `"code":"`+wantCode+`"`) {
			t.Errorf("HTTP %d produced %s, want code %s", status, text, wantCode)
		}
	}
}

// TestSuccessReturnsStructuredContent checks the other direction: a good call
// must land in StructuredContent so clients do not have to parse text.
func TestSuccessReturnsStructuredContent(t *testing.T) {
	cs := connect(t, Options{}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Jenkins", "2.492.3")
		w.Write([]byte(`{"mode":"NORMAL","numExecutors":0,"useCrumbs":true,"url":"http://x/"}`))
	}))
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "jenkins_info"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error: %s", textOf(res))
	}
	if res.StructuredContent == nil {
		t.Fatal("no structured content")
	}
	var info struct {
		Version      string `json:"version"`
		Auth         string `json:"auth"`
		WriteEnabled bool   `json:"write_enabled"`
	}
	if err := json.Unmarshal(mustJSON(t, res.StructuredContent), &info); err != nil {
		t.Fatalf("structured content is not the expected shape: %v", err)
	}
	if info.Version != "2.492.3" {
		t.Errorf("version = %q", info.Version)
	}
	if info.Auth != "anonymous" {
		t.Errorf("auth = %q, want anonymous to be stated explicitly", info.Auth)
	}
	if info.WriteEnabled {
		t.Error("write_enabled must be false when the gate is closed")
	}
}

func textOf(res *mcp.CallToolResult) string {
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			return tc.Text
		}
	}
	return ""
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}
