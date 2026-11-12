package jenkins

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

// ---------------------------------------------------------------------------
// Controller
// ---------------------------------------------------------------------------

// Info describes the controller identity and the bridge's privilege level.
type Info struct {
	URL          string `json:"url"`
	Version      string `json:"version,omitempty"`
	Auth         string `json:"auth"`
	Mode         string `json:"mode,omitempty"`
	NodeName     string `json:"node_name,omitempty"`
	NumExecutors int    `json:"num_executors"`
	UseCrumbs    bool   `json:"use_crumbs"`
	QuietingDown bool   `json:"quieting_down"`
}

// GetInfo reads the controller root and the version header. The version is not
// in the JSON body, so it is taken from X-Jenkins on the same response.
func (c *Client) GetInfo(ctx context.Context) (*Info, error) {
	var raw struct {
		Mode         string `json:"mode"`
		NodeName     string `json:"nodeName"`
		NumExecutors int    `json:"numExecutors"`
		UseCrumbs    bool   `json:"useCrumbs"`
		QuietingDown bool   `json:"quietingDown"`
		URL          string `json:"url"`
	}
	req, err := c.newRequest(ctx, http.MethodGet, "/api/json?tree=mode,nodeName,numExecutors,useCrumbs,quietingDown,url", nil)
	if err != nil {
		return nil, NewError(CodeInternal, "could not build request", err.Error())
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, classifyTransport(err)
	}
	defer resp.Body.Close()
	body, err := readAll(resp)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, classifyHTTP(resp.StatusCode, body)
	}
	if err := jsonUnmarshal([]byte(body), &raw); err != nil {
		return nil, NewError(CodeBadGateway, "controller returned unreadable JSON", err.Error())
	}
	return &Info{
		URL:          c.BaseURL(),
		Version:      resp.Header.Get("X-Jenkins"),
		Auth:         c.AuthKind(),
		Mode:         raw.Mode,
		NodeName:     raw.NodeName,
		NumExecutors: raw.NumExecutors,
		UseCrumbs:    raw.UseCrumbs,
		QuietingDown: raw.QuietingDown,
	}, nil
}

// ---------------------------------------------------------------------------
// Jobs
// ---------------------------------------------------------------------------

// Job is a summary of a job, as it appears in a listing.
type Job struct {
	Name        string `json:"name"`
	FullName    string `json:"full_name"`
	URL         string `json:"url"`
	Color       string `json:"color,omitempty"`
	Class       string `json:"class,omitempty"`
	IsFolder    bool   `json:"is_folder"`
	Description string `json:"description,omitempty"`
}

type jobSummaryJSON struct {
	Class string           `json:"_class"`
	Name  string           `json:"name"`
	URL   string           `json:"url"`
	Color string           `json:"color"`
	Jobs  []jobSummaryJSON `json:"jobs"`
}

// ListJobs lists jobs. folder is a Full Name prefix; empty means the root.
//
// Folders are reported as jobs (is_folder=true) rather than silently flattened,
// because a caller that cannot see a folder cannot know to look inside it.
func (c *Client) ListJobs(ctx context.Context, folder string) ([]Job, error) {
	base := ""
	if strings.TrimSpace(folder) != "" {
		jp, err := JobPath(folder)
		if err != nil {
			return nil, err
		}
		base = jp
	}
	var raw struct {
		Jobs []jobSummaryJSON `json:"jobs"`
	}
	path := base + "/api/json?tree=jobs%5B_class,name,url,color,jobs%5B_class,name,url,color%5D%5D"
	if err := c.getJSON(ctx, path, &raw); err != nil {
		return nil, err
	}
	prefix := strings.TrimSpace(folder)
	out := make([]Job, 0, len(raw.Jobs))
	for _, j := range raw.Jobs {
		full := j.Name
		if prefix != "" {
			full = prefix + "/" + j.Name
		}
		out = append(out, Job{
			Name:     j.Name,
			FullName: full,
			URL:      j.URL,
			Color:    j.Color,
			Class:    j.Class,
			IsFolder: strings.Contains(j.Class, "Folder"),
		})
	}
	return out, nil
}

// ParameterDefinition is one declared input of a job.
type ParameterDefinition struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Description string   `json:"description,omitempty"`
	Default     any      `json:"default,omitempty"`
	Choices     []string `json:"choices,omitempty"`
	// ChoicesAvailable is false for plugin-defined parameter types whose option
	// list is not exposed through api/json (ExtendedChoice, ListSubversionTags).
	ChoicesAvailable bool `json:"choices_available"`
}

type paramDefJSON struct {
	Class   string   `json:"_class"`
	Type    string   `json:"type"`
	Name    string   `json:"name"`
	Desc    string   `json:"description"`
	Choices []string `json:"choices"`
	Default *struct {
		Value any `json:"value"`
	} `json:"defaultParameterValue"`
}

// JobDetail is the expanded view of a job: identity plus its parameter contract.
type JobDetail struct {
	Job
	Description     string                `json:"description,omitempty"`
	Buildable       bool                  `json:"buildable"`
	InQueue         bool                  `json:"in_queue"`
	NextBuildNumber int64                 `json:"next_build_number,omitempty"`
	Parameters      []ParameterDefinition `json:"parameters"`
	LastBuild       *BuildSummary         `json:"last_build,omitempty"`
	LastSuccess     *BuildSummary         `json:"last_successful_build,omitempty"`
	LastFailed      *BuildSummary         `json:"last_failed_build,omitempty"`
}

type buildSummaryJSON struct {
	Number int64  `json:"number"`
	URL    string `json:"url"`
	Result string `json:"result"`
}

// GetJob reads a job and its parameter definitions.
func (c *Client) GetJob(ctx context.Context, fullName string) (*JobDetail, error) {
	jp, err := JobPath(fullName)
	if err != nil {
		return nil, err
	}
	tree := "name,url,color,_class,description,buildable,inQueue,nextBuildNumber," +
		"property%5BparameterDefinitions%5B_class,type,name,description,choices,defaultParameterValue%5Bvalue%5D%5D%5D," +
		"lastBuild%5Bnumber,url,result%5D,lastSuccessfulBuild%5Bnumber,url,result%5D,lastFailedBuild%5Bnumber,url,result%5D"
	var raw struct {
		Class       string `json:"_class"`
		Name        string `json:"name"`
		URL         string `json:"url"`
		Color       string `json:"color"`
		Description string `json:"description"`
		Buildable   bool   `json:"buildable"`
		InQueue     bool   `json:"inQueue"`
		NextBuild   int64  `json:"nextBuildNumber"`
		Property    []struct {
			ParamDefs []paramDefJSON `json:"parameterDefinitions"`
		} `json:"property"`
		LastBuild   *buildSummaryJSON `json:"lastBuild"`
		LastSuccess *buildSummaryJSON `json:"lastSuccessfulBuild"`
		LastFailed  *buildSummaryJSON `json:"lastFailedBuild"`
	}
	if err := c.getJSON(ctx, jp+"/api/json?tree="+tree, &raw); err != nil {
		return nil, err
	}
	d := &JobDetail{
		Job: Job{
			Name:        raw.Name,
			FullName:    strings.Trim(fullName, "/"),
			URL:         raw.URL,
			Color:       raw.Color,
			Class:       raw.Class,
			IsFolder:    strings.Contains(raw.Class, "Folder"),
			Description: raw.Description,
		},
		Description:     raw.Description,
		Buildable:       raw.Buildable,
		InQueue:         raw.InQueue,
		NextBuildNumber: raw.NextBuild,
		Parameters:      []ParameterDefinition{},
	}
	for _, p := range raw.Property {
		for _, pd := range p.ParamDefs {
			// Separator definitions are decoration inside the parameter form, not
			// inputs. Passing them to the controller is noise at best.
			if isSeparatorType(pd.Class, pd.Type) {
				continue
			}
			def := ParameterDefinition{
				Name:             pd.Name,
				Type:             normalizeParamType(pd.Class, pd.Type),
				Description:      pd.Desc,
				Choices:          pd.Choices,
				ChoicesAvailable: len(pd.Choices) > 0,
			}
			if pd.Default != nil {
				def.Default = pd.Default.Value
			}
			d.Parameters = append(d.Parameters, def)
		}
	}
	if raw.LastBuild != nil {
		d.LastBuild = &BuildSummary{Number: raw.LastBuild.Number, URL: raw.LastBuild.URL, Result: raw.LastBuild.Result}
	}
	if raw.LastSuccess != nil {
		d.LastSuccess = &BuildSummary{Number: raw.LastSuccess.Number, URL: raw.LastSuccess.URL, Result: raw.LastSuccess.Result}
	}
	if raw.LastFailed != nil {
		d.LastFailed = &BuildSummary{Number: raw.LastFailed.Number, URL: raw.LastFailed.URL, Result: raw.LastFailed.Result}
	}
	return d, nil
}

// ---------------------------------------------------------------------------
// Builds
// ---------------------------------------------------------------------------

// BuildSummary is one row of a build history.
type BuildSummary struct {
	Number    int64  `json:"number"`
	URL       string `json:"url,omitempty"`
	Result    string `json:"result"`
	State     string `json:"state"`
	Building  bool   `json:"building"`
	Timestamp int64  `json:"timestamp,omitempty"`
	Duration  int64  `json:"duration,omitempty"`
}

// buildState renders (result, building) into one word, so that "still running"
// and "finished with null result" are never confused.
func buildState(result string, building bool) string {
	if building {
		return "running"
	}
	if result == "" {
		// Not building and no result means the build record is incomplete, not
		// that it succeeded.
		return "unknown"
	}
	return strings.ToLower(result)
}

type buildListJSON struct {
	Builds []struct {
		Number    int64  `json:"number"`
		URL       string `json:"url"`
		Result    string `json:"result"`
		Building  bool   `json:"building"`
		Timestamp int64  `json:"timestamp"`
		Duration  int64  `json:"duration"`
	} `json:"builds"`
}

// ListBuilds returns the most recent builds of a job, newest first.
func (c *Client) ListBuilds(ctx context.Context, fullName string, limit int) ([]BuildSummary, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 200 {
		limit = 200
	}
	jp, err := JobPath(fullName)
	if err != nil {
		return nil, err
	}
	var raw buildListJSON
	path := fmt.Sprintf("%s/api/json?tree=builds%%5Bnumber,url,result,building,timestamp,duration%%5D%%7B0,%d%%7D", jp, limit)
	if err := c.getJSON(ctx, path, &raw); err != nil {
		return nil, err
	}
	out := make([]BuildSummary, 0, len(raw.Builds))
	for _, b := range raw.Builds {
		out = append(out, BuildSummary{
			Number: b.Number, URL: b.URL, Result: b.Result,
			State: buildState(b.Result, b.Building), Building: b.Building,
			Timestamp: b.Timestamp, Duration: b.Duration,
		})
	}
	return out, nil
}

// Build is the expanded view of one build.
type Build struct {
	Number      int64          `json:"number"`
	Job         string         `json:"job"`
	URL         string         `json:"url"`
	Result      string         `json:"result"`
	State       string         `json:"state"`
	Building    bool           `json:"building"`
	DurationMs  int64          `json:"duration_ms,omitempty"`
	EstimatedMs int64          `json:"estimated_duration_ms,omitempty"`
	Timestamp   int64          `json:"timestamp,omitempty"`
	QueueID     int64          `json:"queue_id,omitempty"`
	DisplayName string         `json:"display_name,omitempty"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
	BuiltOn     string         `json:"built_on,omitempty"`
}

// GetBuild reads one build.
func (c *Client) GetBuild(ctx context.Context, fullName string, ref BuildRef) (*Build, error) {
	bp, err := buildPath(fullName, ref)
	if err != nil {
		return nil, err
	}
	tree := "number,url,result,building,duration,estimatedDuration,timestamp,queueId,displayName,description," +
		"actions%5Bparameters%5Bname,value%5D%5D,builtOn"
	var raw struct {
		Number      int64  `json:"number"`
		URL         string `json:"url"`
		Result      string `json:"result"`
		Building    bool   `json:"building"`
		Duration    int64  `json:"duration"`
		Estimated   int64  `json:"estimatedDuration"`
		Timestamp   int64  `json:"timestamp"`
		QueueID     int64  `json:"queueId"`
		DisplayName string `json:"displayName"`
		Description string `json:"description"`
		BuiltOn     string `json:"builtOn"`
		Actions     []struct {
			Parameters []struct {
				Name  string `json:"name"`
				Value any    `json:"value"`
			} `json:"parameters"`
		} `json:"actions"`
	}
	if err := c.getJSON(ctx, bp+"/api/json?tree="+tree, &raw); err != nil {
		return nil, err
	}
	b := &Build{
		Number: raw.Number, Job: strings.Trim(fullName, "/"), URL: raw.URL,
		Result: raw.Result, State: buildState(raw.Result, raw.Building), Building: raw.Building,
		DurationMs: raw.Duration, EstimatedMs: raw.Estimated, Timestamp: raw.Timestamp,
		QueueID: raw.QueueID, DisplayName: raw.DisplayName, Description: raw.Description,
		BuiltOn: raw.BuiltOn,
	}
	for _, a := range raw.Actions {
		if len(a.Parameters) == 0 {
			continue
		}
		if b.Parameters == nil {
			b.Parameters = make(map[string]any, len(a.Parameters))
		}
		for _, p := range a.Parameters {
			b.Parameters[p.Name] = p.Value
		}
	}
	return b, nil
}
