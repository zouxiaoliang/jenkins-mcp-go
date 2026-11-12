package jenkins

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Client talks to one Jenkins controller. It owns the HTTP connection, the
// authentication, and the Crumb lifecycle; it knows nothing about MCP.
type Client struct {
	base  *url.URL
	user  string
	token string
	http  *http.Client

	// authKind is reported back to callers so that Anonymous Mode is never
	// implied: an agent must be able to see that it is unprivileged.
	authKind string

	crumbMu   sync.Mutex
	crumb     string
	crumbHead string

	// params caches job parameter contracts; guarded internally.
	params *paramCache
}

// Options configures a Client.
type Options struct {
	BaseURL string
	// User and Token are both empty for Anonymous Mode.
	User  string
	Token string
	// Timeout bounds any single request, including reading a response body.
	Timeout time.Duration
	// InsecureSkipVerify is for controllers with self-signed certificates.
	InsecureSkipVerify bool
}

// CrumbHeaderField is the header Jenkins expects the Crumb in.
const CrumbHeaderField = "Jenkins-Crumb"

// NewClient builds a Client. The returned Client always has a cookie jar: the
// Crumb is bound to the JSESSIONID that issued it, so a crumb fetched without
// the matching session cookie is rejected by the controller.
func NewClient(opts Options) (*Client, error) {
	base, err := url.Parse(strings.TrimRight(opts.BaseURL, "/"))
	if err != nil {
		return nil, NewError(CodeInvalidParameter, "invalid controller URL", err.Error())
	}
	if base.Scheme != "http" && base.Scheme != "https" {
		return nil, NewError(CodeInvalidParameter, "controller URL must be http or https", opts.BaseURL)
	}
	if base.Host == "" {
		return nil, NewError(CodeInvalidParameter, "controller URL must include a host", opts.BaseURL)
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if opts.InsecureSkipVerify {
		// Deliberately opt-in only; see ADR-0004 on capability disclosure.
		tr.TLSClientConfig.InsecureSkipVerify = true
	}
	authKind := "anonymous"
	if opts.User != "" && opts.Token != "" {
		authKind = "token"
	}
	return &Client{
		base:     base,
		user:     opts.User,
		token:    opts.Token,
		authKind: authKind,
		http:     &http.Client{Transport: tr, Jar: jar, Timeout: timeout},
		params:   newParamCache(),
	}, nil
}

// AuthKind reports how the client authenticates: "token" or "anonymous".
func (c *Client) AuthKind() string { return c.authKind }

// BaseURL is the controller root URL, without a trailing slash.
func (c *Client) BaseURL() string { return c.base.String() }

// newRequest builds a request rooted at the controller. path must be an absolute
// path beginning with "/".
func (c *Client) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	if !strings.HasPrefix(path, "/") {
		return nil, fmt.Errorf("internal: path must start with /: %q", path)
	}
	u := *c.base
	// Preserve any path prefix the controller itself is mounted under.
	u.Path = strings.TrimRight(c.base.Path, "/") + path
	u.RawQuery = ""
	if i := strings.Index(path, "?"); i >= 0 {
		u.Path = strings.TrimRight(c.base.Path, "/") + path[:i]
		u.RawQuery = path[i+1:]
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, err
	}
	if c.authKind == "token" {
		req.SetBasicAuth(c.user, c.token)
	}
	req.Header.Set("Accept", "application/json")
	return req, nil
}

// fetchCrumb retrieves a Crumb for the current session.
func (c *Client) fetchCrumb(ctx context.Context) error {
	req, err := c.newRequest(ctx, http.MethodGet, "/crumbIssuer/api/json", nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return classifyTransport(err)
	}
	defer resp.Body.Close()
	// A controller without the CSRF issuer configured answers 404: no crumb is
	// needed, and that is not an error.
	if resp.StatusCode == http.StatusNotFound {
		c.crumb, c.crumbHead = "", ""
		return nil
	}
	if resp.StatusCode != http.StatusOK {
		return classifyHTTP(resp.StatusCode, readString(resp.Body))
	}
	var payload struct {
		Crumb             string `json:"crumb"`
		CrumbRequestField string `json:"crumbRequestField"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return NewError(CodeBadGateway, "crumb issuer returned unreadable JSON", err.Error())
	}
	c.crumb = payload.Crumb
	c.crumbHead = payload.CrumbRequestField
	if c.crumbHead == "" {
		c.crumbHead = CrumbHeaderField
	}
	return nil
}

// Crumb returns a Crumb, fetching one if none is cached.
func (c *Client) Crumb(ctx context.Context) (field, value string, err error) {
	c.crumbMu.Lock()
	defer c.crumbMu.Unlock()
	if c.crumb == "" {
		if err := c.fetchCrumb(ctx); err != nil {
			return "", "", err
		}
	}
	return c.crumbHead, c.crumb, nil
}

// invalidateCrumb drops the cached Crumb so the next state-changing request
// re-fetches it. The controller expires crumbs when the session is recycled.
func (c *Client) invalidateCrumb() {
	c.crumbMu.Lock()
	c.crumb, c.crumbHead = "", ""
	c.crumbMu.Unlock()
}

// Response is a completed controller response that the caller still owns.
type Response struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

// do performs a request and returns the body, mapping failures to the contract.
func (c *Client) do(ctx context.Context, method, path string, body io.Reader, contentType string) (*Response, error) {
	req, err := c.newRequest(ctx, method, path, body)
	if err != nil {
		return nil, NewError(CodeInternal, "could not build request", err.Error())
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	// State-changing requests need the Crumb. Attaching it unconditionally on
	// POST is what makes a write work on a crumbs-enabled controller.
	if method != http.MethodGet && method != http.MethodHead {
		field, value, err := c.Crumb(ctx)
		if err != nil {
			return nil, err
		}
		if value != "" {
			req.Header.Set(field, value)
		}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, classifyTransport(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, classifyTransport(err)
	}
	out := &Response{StatusCode: resp.StatusCode, Header: resp.Header, Body: data}
	if resp.StatusCode >= 400 {
		// A stale Crumb surfaces as 403. Clearing it lets the caller's retry (or
		// the Write Tool's own single retry) succeed rather than fail forever.
		if resp.StatusCode == http.StatusForbidden {
			c.invalidateCrumb()
		}
		return out, classifyHTTP(resp.StatusCode, string(data))
	}
	return out, nil
}

// getJSON GETs path and decodes it into out.
func (c *Client) getJSON(ctx context.Context, path string, out any) error {
	resp, err := c.do(ctx, http.MethodGet, path, nil, "")
	if err != nil {
		return err
	}
	if err := json.Unmarshal(resp.Body, out); err != nil {
		return NewError(CodeBadGateway, "controller returned unreadable JSON", err.Error())
	}
	return nil
}

// postForm POSTs url-encoded form data. It retries exactly once on a stale
// Crumb, which is the only failure a retry can legitimately fix.
func (c *Client) postForm(ctx context.Context, path string, form url.Values) (*Response, error) {
	body := form.Encode()
	resp, err := c.do(ctx, http.MethodPost, path, strings.NewReader(body), "application/x-www-form-urlencoded")
	if err == nil {
		return resp, nil
	}
	if je := AsError(err); je != nil && je.Code == CodeForbidden {
		if resp2, err2 := c.do(ctx, http.MethodPost, path, strings.NewReader(body), "application/x-www-form-urlencoded"); err2 == nil {
			return resp2, nil
		}
	}
	return nil, err
}

func readString(r io.Reader) string {
	b, _ := io.ReadAll(io.LimitReader(r, 4096))
	return string(b)
}

// queueIDFromLocation extracts the numeric queue item id from the Location header
// that a build trigger returns, e.g. ".../queue/item/78695/".
func queueIDFromLocation(loc string) (int64, bool) {
	if loc == "" {
		return 0, false
	}
	u, err := url.Parse(loc)
	if err != nil {
		return 0, false
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i := 0; i < len(segs)-1; i++ {
		if segs[i] == "item" {
			if id, err := strconv.ParseInt(segs[i+1], 10, 64); err == nil {
				return id, true
			}
		}
	}
	return 0, false
}
