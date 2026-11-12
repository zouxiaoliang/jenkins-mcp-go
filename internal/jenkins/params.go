package jenkins

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// paramCacheTTL bounds how stale a job's parameter contract may be. Job
// definitions change rarely, but an agent that triggers twice within a session
// should not pay two round trips.
const paramCacheTTL = 5 * time.Minute

type paramCacheEntry struct {
	params  []ParameterDefinition
	fetched time.Time
}

// paramCache is per-Client so that two profiles never share a job contract.
type paramCache struct {
	mu      sync.Mutex
	entries map[string]paramCacheEntry
}

func newParamCache() *paramCache {
	return &paramCache{entries: make(map[string]paramCacheEntry)}
}

func (p *paramCache) get(name string) ([]ParameterDefinition, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.entries[name]
	if !ok || time.Since(e.fetched) > paramCacheTTL {
		return nil, false
	}
	return e.params, true
}

func (p *paramCache) put(name string, params []ParameterDefinition) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.entries[name] = paramCacheEntry{params: params, fetched: time.Now()}
}

// invalidate drops a job's cached contract, used after a failed trigger.
func (p *paramCache) invalidate(name string) {
	p.mu.Lock()
	delete(p.entries, name)
	p.mu.Unlock()
}

// ParameterDefinitions returns a job's inputs, cached for paramCacheTTL.
func (c *Client) ParameterDefinitions(ctx context.Context, fullName string) ([]ParameterDefinition, error) {
	if c.params == nil {
		c.params = newParamCache()
	}
	if p, ok := c.params.get(fullName); ok {
		return p, nil
	}
	d, err := c.GetJob(ctx, fullName)
	if err != nil {
		return nil, err
	}
	c.params.put(fullName, d.Parameters)
	return d.Parameters, nil
}

// BuildRequest is a validated request to start a build.
type BuildRequest struct {
	Job    string
	Params map[string]any
}

// NormalizeParams validates caller-supplied parameters against the job's
// Parameter Definitions and renders them into the single string-per-parameter
// form the controller actually accepts.
//
// Two jobs for this function: catch a typo before it silently becomes "the
// default value was used", and absorb the controller's odd corners (multi-select
// is a comma-separated string, separators are not parameters) so an agent does
// not have to know them.
func NormalizeParams(defs []ParameterDefinition, in map[string]any) (url.Values, error) {
	byName := make(map[string]ParameterDefinition, len(defs))
	names := make([]string, 0, len(defs))
	for _, d := range defs {
		byName[d.Name] = d
		names = append(names, d.Name)
	}
	sort.Strings(names)

	out := url.Values{}
	for k, v := range in {
		def, ok := byName[k]
		if !ok {
			return nil, NewError(CodeInvalidParameter,
				fmt.Sprintf("job has no parameter named %q", k),
				"available parameters: "+strings.Join(names, ", "))
		}
		s, err := renderParam(def, v)
		if err != nil {
			return nil, err
		}
		out.Set(k, s)
	}
	return out, nil
}

// renderParam converts one caller value into the controller's string form.
func renderParam(def ParameterDefinition, v any) (string, error) {
	if v == nil {
		return "", NewError(CodeInvalidParameter,
			fmt.Sprintf("parameter %q is null", def.Name),
			"omit the parameter to use its default")
	}
	switch def.Type {
	case "Boolean":
		switch t := v.(type) {
		case bool:
			return strconv.FormatBool(t), nil
		case string:
			if b, err := strconv.ParseBool(strings.TrimSpace(t)); err == nil {
				return strconv.FormatBool(b), nil
			}
			return "", NewError(CodeInvalidParameter,
				fmt.Sprintf("parameter %q is boolean but got %q", def.Name, t),
				"send true or false")
		}
		return "", typeErr(def, v)

	case "ExtendedChoice":
		// Multi-select is transmitted as one comma-separated value; accept an
		// array so the caller can express the selection honestly.
		switch t := v.(type) {
		case []any:
			parts := make([]string, 0, len(t))
			for _, e := range t {
				parts = append(parts, fmt.Sprint(e))
			}
			return strings.Join(parts, ","), nil
		case []string:
			return strings.Join(t, ","), nil
		case string:
			return t, nil
		}
		return "", typeErr(def, v)

	case "Choice":
		s := fmt.Sprint(v)
		if len(def.Choices) > 0 && !contains(def.Choices, s) {
			return "", NewError(CodeInvalidParameter,
				fmt.Sprintf("parameter %q does not accept %q", def.Name, s),
				"allowed values: "+strings.Join(def.Choices, ", "))
		}
		return s, nil

	case "File":
		return "", NewError(CodeInvalidParameter,
			fmt.Sprintf("parameter %q is a file parameter", def.Name),
			"file parameters require a multipart upload and are not supported")
	}

	switch t := v.(type) {
	case string:
		return t, nil
	case bool, float64, int, int64:
		return fmt.Sprint(t), nil
	case json.Number:
		return t.String(), nil
	case []any:
		parts := make([]string, 0, len(t))
		for _, e := range t {
			parts = append(parts, fmt.Sprint(e))
		}
		return strings.Join(parts, ","), nil
	}
	return "", typeErr(def, v)
}

func typeErr(def ParameterDefinition, v any) error {
	return NewError(CodeInvalidParameter,
		fmt.Sprintf("parameter %q expects a %s value", def.Name, def.Type),
		fmt.Sprintf("got %T", v))
}

func contains(list []string, s string) bool {
	for _, e := range list {
		if e == s {
			return true
		}
	}
	return false
}

// ErrJobHasNoParams is returned when a caller supplies parameters for a job that
// declares none; the controller would ignore them silently.
var ErrJobHasNoParams = NewError(CodeJobHasNoParameters,
	"job declares no parameters", "trigger it without parameters")

// Trigger starts a build, waiting up to wait for it to leave the queue.
func (c *Client) Trigger(ctx context.Context, req BuildRequest, wait time.Duration) (*TriggerResult, error) {
	if c.params == nil {
		c.params = newParamCache()
	}
	defs, err := c.ParameterDefinitions(ctx, req.Job)
	if err != nil {
		return nil, err
	}
	jp, err := JobPath(req.Job)
	if err != nil {
		return nil, err
	}
	if len(defs) == 0 {
		if len(req.Params) > 0 {
			return nil, ErrJobHasNoParams
		}
		resp, err := c.postForm(ctx, jp+"/build", url.Values{})
		if err != nil {
			c.params.invalidate(req.Job)
			return nil, err
		}
		return c.afterTrigger(ctx, resp, wait)
	}
	form, err := NormalizeParams(defs, req.Params)
	if err != nil {
		return nil, err
	}
	resp, err := c.postForm(ctx, jp+"/buildWithParameters", form)
	if err != nil {
		c.params.invalidate(req.Job)
		return nil, err
	}
	return c.afterTrigger(ctx, resp, wait)
}

func (c *Client) afterTrigger(ctx context.Context, resp *Response, wait time.Duration) (*TriggerResult, error) {
	id, ok := queueIDFromLocation(resp.Header.Get("Location"))
	if !ok {
		// A controller can accept the request without telling us where it went
		// (queue disabled, folded into an existing build). Say so rather than
		// inventing an id.
		return &TriggerResult{Status: "queued"}, nil
	}
	return c.WaitForBuild(ctx, id, wait)
}

// StopBuild aborts a build that has already started.
func (c *Client) StopBuild(ctx context.Context, fullName string, ref BuildRef) error {
	bp, err := buildPath(fullName, ref)
	if err != nil {
		return err
	}
	_, err = c.postForm(ctx, bp+"/stop", url.Values{})
	return err
}
