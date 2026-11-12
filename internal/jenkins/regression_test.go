package jenkins

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// TestFinishedBuildReportsNoMoreData is a regression test for a real failure: on
// Jenkins 2.492.3 a completed build sends no X-More-Data header at all, and
// defaulting the missing header to "true" made finished builds look live. An
// agent following the log would poll forever.
func TestFinishedBuildReportsNoMoreData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain;charset=utf-8")
		w.Header().Set("X-Text-Size", "18")
		// Deliberately no X-More-Data: this is what a finished build looks like.
		w.Write([]byte("Finished: SUCCESS\n"))
	}))
	defer srv.Close()

	c, err := NewClient(Options{BaseURL: srv.URL, Timeout: defaultTestTimeout})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	chunk, err := c.ReadLog(t.Context(), "job", RefLastBuild, 0)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	if chunk.MoreData {
		t.Fatal("a finished build must not be reported as still writing")
	}
	if chunk.Text != "Finished: SUCCESS\n" {
		t.Fatalf("text = %q", chunk.Text)
	}
}

// TestRunningBuildStillReportsMoreData guards the other direction, so the fix
// above cannot silently disable log following.
func TestRunningBuildStillReportsMoreData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Text-Size", "7")
		w.Header().Set("X-More-Data", "true")
		w.Write([]byte("step 1\n"))
	}))
	defer srv.Close()

	c, err := NewClient(Options{BaseURL: srv.URL, Timeout: defaultTestTimeout})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	chunk, err := c.ReadLog(t.Context(), "job", RefLastBuild, 0)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	if !chunk.MoreData {
		t.Fatal("a running build must be reported as still writing")
	}
}

// TestListNodesParsesRealShape uses a captured response from 2.492.3. An earlier
// version decoded loadStatistics.busyExecutors as an int, but the controller
// serialises it as an empty object, so every list_nodes call failed.
func TestListNodesParsesRealShape(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/fixtures/computer.json")
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	c := newFakeClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(raw)
	}))
	cluster, err := c.ListNodes(t.Context())
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if cluster.TotalExecutors != 78 {
		t.Errorf("total_executors = %d, want 78", cluster.TotalExecutors)
	}
	if cluster.BusyExecutors != 2 {
		t.Errorf("busy_executors = %d, want 2", cluster.BusyExecutors)
	}
	if cluster.IdleExecutors != 76 {
		t.Errorf("idle_executors = %d, want 76", cluster.IdleExecutors)
	}
	if len(cluster.Nodes) != 44 {
		t.Fatalf("got %d nodes, want 44", len(cluster.Nodes))
	}
	// The controller's built-in node has zero executors; that must be preserved
	// rather than rounded into "one idle executor".
	if n := cluster.Nodes[0]; n.DisplayName != "Built-In Node" || n.NumExecutors != 0 {
		t.Errorf("first node = %+v", n)
	}
	// Labels must survive: they are how a queued build's "why" is interpreted.
	found := false
	for _, n := range cluster.Nodes {
		for _, l := range n.Labels {
			if l == "ARMPACKAGE" {
				found = true
			}
		}
	}
	if !found {
		t.Error("node labels were lost")
	}
}
