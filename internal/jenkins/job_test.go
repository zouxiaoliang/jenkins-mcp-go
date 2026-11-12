package jenkins

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// newFakeClient wires a Client to a test server.
func newFakeClient(t *testing.T, h http.Handler) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := NewClient(Options{BaseURL: srv.URL, Timeout: defaultTestTimeout})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

func TestGetJobReadsRealFixture(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/fixtures/job_parameters.json")
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	c := newFakeClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/job/ModuleSubmitBuild/api/json") {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(raw)
	}))
	d, err := c.GetJob(t.Context(), "ModuleSubmitBuild")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if d.FullName != "ModuleSubmitBuild" {
		t.Errorf("full name = %q", d.FullName)
	}
	if len(d.Parameters) != 14 {
		t.Fatalf("got %d parameters, want 14 (separators must be excluded)", len(d.Parameters))
	}
	// The fixture declares these types; a separator leaking in would displace one.
	byName := map[string]ParameterDefinition{}
	for _, p := range d.Parameters {
		byName[p.Name] = p
	}
	if _, ok := byName["separator-47147a1a-c816-4dd6-9bc2-e965cade5775"]; ok {
		t.Error("separator definition was exposed as a parameter")
	}
	if p, ok := byName["update_module"]; !ok || p.Type != "Boolean" {
		t.Errorf("update_module: got %+v", p)
	}
	if p, ok := byName["target_version"]; !ok || p.Type != "String" {
		t.Errorf("target_version: got %+v", p)
	}
	if p := byName["feishu_notice_url"]; p.Default == nil {
		t.Error("feishu_notice_url lost its default value")
	}
}

func TestNormalizeParams(t *testing.T) {
	defs := []ParameterDefinition{
		{Name: "str", Type: "String"},
		{Name: "flag", Type: "Boolean"},
		{Name: "multi", Type: "ExtendedChoice"},
		{Name: "choice", Type: "Choice", Choices: []string{"a", "b"}},
	}

	t.Run("array becomes comma string", func(t *testing.T) {
		v, err := NormalizeParams(defs, map[string]any{"multi": []any{"x", "y"}})
		if err != nil {
			t.Fatalf("NormalizeParams: %v", err)
		}
		if got := v.Get("multi"); got != "x,y" {
			t.Fatalf("got %q, want %q", got, "x,y")
		}
	})

	t.Run("string bool is coerced", func(t *testing.T) {
		v, err := NormalizeParams(defs, map[string]any{"flag": "true"})
		if err != nil {
			t.Fatalf("NormalizeParams: %v", err)
		}
		if got := v.Get("flag"); got != "true" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("wrong bool word is rejected", func(t *testing.T) {
		if _, err := NormalizeParams(defs, map[string]any{"flag": "yes"}); err == nil {
			t.Fatal("expected an error for a non-boolean word")
		}
	})

	t.Run("unknown name lists available ones", func(t *testing.T) {
		_, err := NormalizeParams(defs, map[string]any{"nope": "x"})
		if err == nil {
			t.Fatal("expected an error")
		}
		je := AsError(err)
		if je.Code != CodeInvalidParameter {
			t.Fatalf("code = %s", je.Code)
		}
		for _, want := range []string{"flag", "multi", "str"} {
			if !strings.Contains(je.Detail, want) {
				t.Errorf("detail %q does not mention %q", je.Detail, want)
			}
		}
	})

	t.Run("choice rejects a value outside the list", func(t *testing.T) {
		if _, err := NormalizeParams(defs, map[string]any{"choice": "z"}); err == nil {
			t.Fatal("expected an error for a value outside the choices")
		}
	})

	t.Run("explicit null is rejected rather than defaulted silently", func(t *testing.T) {
		_, err := NormalizeParams(defs, map[string]any{"str": nil})
		if err == nil {
			t.Fatal("expected an error")
		}
	})
}

func TestBuildStateNeverReportsUnknownAsSuccess(t *testing.T) {
	cases := []struct {
		result   string
		building bool
		want     string
	}{
		{"SUCCESS", false, "success"},
		{"FAILURE", false, "failure"},
		{"", true, "running"},
		{"", false, "unknown"},
	}
	for _, c := range cases {
		if got := buildState(c.result, c.building); got != c.want {
			t.Errorf("buildState(%q,%v) = %q, want %q", c.result, c.building, got, c.want)
		}
	}
}

func TestClassifyHTTPKeepsForbiddenAndNotFoundApart(t *testing.T) {
	if got := classifyHTTP(403, "").Code; got != CodeForbidden {
		t.Errorf("403 -> %s, want forbidden", got)
	}
	if got := classifyHTTP(404, "").Code; got != CodeNotFound {
		t.Errorf("404 -> %s, want not_found", got)
	}
	if classifyHTTP(403, "").Retryable {
		t.Error("forbidden must not be retryable")
	}
	if !classifyHTTP(503, "").Retryable {
		t.Error("bad_gateway should be retryable")
	}
}

func TestQueueItemParsing(t *testing.T) {
	var raw queueItemJSON
	body := `{"id":78695,"why":"Waiting for next available executor","blocked":false,
	          "buildable":true,"stuck":false,"cancelled":false,"inQueueSince":1794385680000,
	          "task":{"name":"ModuleSubmitBuild","url":"http://x/job/ModuleSubmitBuild/"},
	          "executable":{"number":31249,"url":"http://x/job/ModuleSubmitBuild/31249/"}}`
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	q := toQueueItem(raw)
	if q.ID != 78695 || q.TaskName != "ModuleSubmitBuild" {
		t.Fatalf("got %+v", q)
	}
	if q.Executable == nil || q.Executable.Number != 31249 {
		t.Fatalf("executable not parsed: %+v", q.Executable)
	}
	if q.Why == "" {
		t.Error("why string lost, so a queued build would be unexplainable")
	}
}
