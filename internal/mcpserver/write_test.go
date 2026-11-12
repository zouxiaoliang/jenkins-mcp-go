package mcpserver

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// jobWithParams serves a job whose parameter contract is a single String
// parameter, and counts POSTs so a test can prove nothing was triggered.
func jobWithParams(posts *atomic.Int32) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts.Add(1)
			w.Header().Set("Location", "/queue/item/1/")
			w.WriteHeader(http.StatusCreated)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"_class":"org.jenkinsci.plugins.workflow.job.WorkflowJob",
			"name":"Demo","url":"http://x/job/Demo/","buildable":true,
			"property":[{"_class":"hudson.model.ParametersDefinitionProperty","parameterDefinitions":[
				{"_class":"hudson.model.StringParameterDefinition","name":"target_version",
				 "type":"StringParameterDefinition","defaultParameterValue":{"value":"1.0"}},
				{"_class":"jenkins.plugins.parameter_separator.ParameterSeparatorDefinition",
				 "name":"sep-1","type":"ParameterSeparatorDefinition"}
			]}]
		}`))
	})
}

// TestTriggerRejectsUnknownParameterBeforeSending is the guarantee behind
// "parameters are validated against the job's own definitions": a typo must fail
// locally, not reach the controller and silently become the default value.
func TestTriggerRejectsUnknownParameterBeforeSending(t *testing.T) {
	var posts atomic.Int32
	cs := connect(t, Options{AllowWrite: true}, jobWithParams(&posts))

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "trigger_build",
		Arguments: map[string]any{"job": "Demo", "params": map[string]any{"target_verison": "9.9"}},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatal("a misspelled parameter must be an error")
	}
	if posts.Load() != 0 {
		t.Fatalf("controller received %d POSTs; validation must happen before the request", posts.Load())
	}
	text := textOf(res)
	if !strings.Contains(text, "invalid_parameter") || !strings.Contains(text, "target_version") {
		t.Fatalf("error should name the code and the available parameter: %s", text)
	}
}

// TestTriggerEnforcesJobWhitelist proves the session-scoped allowlist is checked
// at call time, not only when tools are registered.
func TestTriggerEnforcesJobWhitelist(t *testing.T) {
	var posts atomic.Int32
	cs := connect(t, Options{AllowWrite: true, AllowedJobs: []string{"Allowed"}}, jobWithParams(&posts))

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "trigger_build",
		Arguments: map[string]any{"job": "NotAllowed"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatal("a job outside the allowlist must be refused")
	}
	if posts.Load() != 0 {
		t.Fatalf("controller received %d POSTs for a non-allowlisted job", posts.Load())
	}
	if !strings.Contains(textOf(res), "forbidden") {
		t.Fatalf("expected forbidden, got %s", textOf(res))
	}
}

// TestStopBuildRequiresOneTarget documents the ambiguity guard: passing both a
// build number and a queue id has no correct interpretation.
func TestStopBuildRejectsBothTargets(t *testing.T) {
	var posts atomic.Int32
	cs := connect(t, Options{AllowWrite: true}, jobWithParams(&posts))

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "stop_build",
		Arguments: map[string]any{"job": "Demo", "build": "3", "queue_id": 7},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatal("build and queue_id together must be refused")
	}
	if posts.Load() != 0 {
		t.Fatalf("controller received %d POSTs", posts.Load())
	}
}
