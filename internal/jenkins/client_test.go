package jenkins

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"
)

// TestCrumbIsFetchedAndSent proves the two halves of the CSRF handshake: a crumb
// is requested, and a POST carries it back in the header the issuer named.
func TestCrumbIsFetchedAndSent(t *testing.T) {
	var crumbFetches atomic.Int32
	var sawCrumb atomic.Bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/crumbIssuer"):
			crumbFetches.Add(1)
			json.NewEncoder(w).Encode(map[string]string{
				"crumb":             "abc123",
				"crumbRequestField": "Jenkins-Crumb",
			})
		case r.Method == http.MethodPost:
			if r.Header.Get("Jenkins-Crumb") == "abc123" {
				sawCrumb.Store(true)
			}
			w.Header().Set("Location", "/queue/item/42/")
			w.WriteHeader(http.StatusCreated)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	c, err := NewClient(Options{BaseURL: srv.URL, Timeout: defaultTestTimeout})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := c.CancelQueueItem(t.Context(), 42); err != nil {
		t.Fatalf("CancelQueueItem: %v", err)
	}
	if !sawCrumb.Load() {
		t.Fatal("POST did not carry the crumb; a crumbs-enabled controller would answer 403")
	}
	if crumbFetches.Load() != 1 {
		t.Fatalf("crumb fetched %d times, want 1 (it must be cached)", crumbFetches.Load())
	}
}

// TestStaleCrumbIsRetriedOnce covers session recycling: the cached crumb goes
// stale, the controller answers 403, and the request succeeds after a refresh.
func TestStaleCrumbIsRetriedOnce(t *testing.T) {
	var crumbFetches atomic.Int32
	var posts atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/crumbIssuer") {
			n := crumbFetches.Add(1)
			json.NewEncoder(w).Encode(map[string]string{
				"crumb":             "crumb" + string(rune('0'+n)),
				"crumbRequestField": "Jenkins-Crumb",
			})
			return
		}
		if r.Method == http.MethodPost {
			posts.Add(1)
			if r.Header.Get("Jenkins-Crumb") != "crumb2" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, err := NewClient(Options{BaseURL: srv.URL, Timeout: defaultTestTimeout})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := c.CancelQueueItem(t.Context(), 1); err != nil {
		t.Fatalf("CancelQueueItem: %v", err)
	}
	if posts.Load() != 2 {
		t.Fatalf("POST attempted %d times, want 2 (one retry after the stale crumb)", posts.Load())
	}
	if crumbFetches.Load() < 2 {
		t.Fatalf("crumb refetched %d times, want at least 2", crumbFetches.Load())
	}
}

// TestBasicAuthSentOnlyWithToken guards the Anonymous Mode path: no token must
// mean no Authorization header, not an empty one.
func TestBasicAuthSentOnlyWithToken(t *testing.T) {
	var sawAuth atomic.Value

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth.Store(r.Header.Get("Authorization"))
		json.NewEncoder(w).Encode(map[string]any{"mode": "NORMAL"})
	}))
	defer srv.Close()

	anon, _ := NewClient(Options{BaseURL: srv.URL, Timeout: defaultTestTimeout})
	if anon.AuthKind() != "anonymous" {
		t.Fatalf("auth kind = %q", anon.AuthKind())
	}
	if _, err := anon.GetInfo(t.Context()); err != nil {
		t.Fatalf("GetInfo: %v", err)
	}
	if v, _ := sawAuth.Load().(string); v != "" {
		t.Fatalf("anonymous request sent Authorization: %q", v)
	}

	tok, _ := NewClient(Options{BaseURL: srv.URL, User: "u", Token: "t", Timeout: defaultTestTimeout})
	if tok.AuthKind() != "token" {
		t.Fatalf("auth kind = %q", tok.AuthKind())
	}
	if _, err := tok.GetInfo(t.Context()); err != nil {
		t.Fatalf("GetInfo: %v", err)
	}
	if v, _ := sawAuth.Load().(string); !strings.HasPrefix(v, "Basic ") {
		t.Fatalf("token request did not use basic auth: %q", v)
	}
}

func TestQueueIDFromLocation(t *testing.T) {
	cases := map[string]int64{
		"http://h/queue/item/78695/": 78695,
		"http://h/queue/item/42":     42,
		"/queue/item/7/":             7,
		"http://h/job/x/":            0,
		"":                           0,
	}
	for in, want := range cases {
		got, ok := queueIDFromLocation(in)
		if want == 0 {
			if ok {
				t.Errorf("queueIDFromLocation(%q) = %d, want no match", in, got)
			}
			continue
		}
		if !ok || got != want {
			t.Errorf("queueIDFromLocation(%q) = %d,%v want %d", in, got, ok, want)
		}
	}
}

func TestClassifyTransportMarksUnreachableRetryable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // nothing is listening any more

	c, err := NewClient(Options{BaseURL: url, Timeout: defaultTestTimeout})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_, err = c.GetInfo(t.Context())
	if err == nil {
		t.Fatal("expected a failure against a closed server")
	}
	je := AsError(err)
	if je.Code != CodeControllerUnreachable {
		t.Fatalf("code = %s, want controller_unreachable", je.Code)
	}
	if !je.Retryable {
		t.Fatal("controller_unreachable must be retryable")
	}
}

func TestValidateGetPathBlocksSSRF(t *testing.T) {
	bad := []string{
		"",
		"relative/path",
		"//evil.example.com/steal",
		"http://evil.example.com/",
		"https://evil.example.com/",
		"/../../../etc/passwd",
		"/a/../../b",
		"/ok\r\nX-Injected: 1",
	}
	for _, p := range bad {
		if _, err := ValidateGetPath(p); err == nil {
			t.Errorf("ValidateGetPath(%q) should have been rejected", p)
		}
	}
	good := []string{
		"/api/json",
		"/job/foo/api/json?tree=name",
		"/pluginManager/api/json?depth=1",
	}
	for _, p := range good {
		if got, err := ValidateGetPath(p); err != nil || got != p {
			t.Errorf("ValidateGetPath(%q) = %q, %v", p, got, err)
		}
	}
}

func TestTruncateNeverSplitsACharacter(t *testing.T) {
	body := []byte("第一行第二行第三行") // 3 bytes per rune
	for limit := int64(1); limit < int64(len(body)); limit++ {
		env := Truncate(body, limit)
		if !utf8.Valid(env.Body) {
			t.Fatalf("limit %d produced invalid UTF-8: %q", limit, env.Body)
		}
	}
	env := Truncate(body, int64(len(body)))
	if env.Truncated {
		t.Fatal("a body exactly at the limit must not be reported as truncated")
	}
	env = Truncate(body, 4)
	if !env.Truncated {
		t.Fatal("a body over the limit must be reported as truncated")
	}
	if env.TotalSize != int64(len(body)) {
		t.Fatalf("total = %d, want %d", env.TotalSize, len(body))
	}
}
