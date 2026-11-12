package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/zouxiaoliang/jenkins-mcp-go/internal/jenkins"
)

// msToRFC3339 renders a Jenkins millisecond timestamp. Agents reason in dates,
// not in epoch milliseconds.
func msToRFC3339(ms int64) string {
	if ms <= 0 {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format(time.RFC3339)
}

// ---------------------------------------------------------------------------
// jenkins_info
// ---------------------------------------------------------------------------

type infoIn struct{}

type infoOut struct {
	jenkins.Info
	WriteEnabled bool     `json:"write_enabled"`
	AllowedJobs  []string `json:"allowed_jobs,omitempty"`
}

func (s *Server) registerInfo(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "jenkins_info",
		Title:       "Controller info",
		Description: "Report the controller URL, version, authentication mode, executor count and whether this session may change state. Call this first when you need to know what you are allowed to do.",
		Annotations: readOnly(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ infoIn) (*mcp.CallToolResult, infoOut, error) {
		info, err := s.client.GetInfo(ctx)
		if err != nil {
			return nil, infoOut{}, fail(err)
		}
		return nil, infoOut{
			Info:         *info,
			WriteEnabled: s.opts.AllowWrite,
			AllowedJobs:  s.opts.AllowedJobs,
		}, nil
	})
}

// ---------------------------------------------------------------------------
// list_jobs / get_job
// ---------------------------------------------------------------------------

type listJobsIn struct {
	Folder string `json:"folder,omitempty" jsonschema:"full name of a folder to list inside; omit for the controller root"`
}

type listJobsOut struct {
	Folder string        `json:"folder,omitempty"`
	Count  int           `json:"count"`
	Jobs   []jenkins.Job `json:"jobs"`
}

func (s *Server) registerListJobs(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_jobs",
		Title:       "List jobs",
		Description: "List the jobs directly inside the controller root or inside one folder. Folders are returned as entries with is_folder=true; pass their full_name as folder to look inside.",
		Annotations: readOnly(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listJobsIn) (*mcp.CallToolResult, listJobsOut, error) {
		jobs, err := s.client.ListJobs(ctx, in.Folder)
		if err != nil {
			return nil, listJobsOut{}, fail(err)
		}
		return nil, listJobsOut{Folder: in.Folder, Count: len(jobs), Jobs: jobs}, nil
	})
}

type getJobIn struct {
	Job string `json:"job" jsonschema:"full job name, e.g. ModuleSubmitBuild or folder/sub/job"`
}

func (s *Server) registerGetJob(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get_job",
		Title:       "Get job",
		Description: "Read one job: state, last builds, and its parameter definitions (names, types, defaults, and choices where the controller exposes them). Read this before triggering a build so you use the job's real parameter names.",
		Annotations: readOnly(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in getJobIn) (*mcp.CallToolResult, jenkins.JobDetail, error) {
		d, err := s.client.GetJob(ctx, in.Job)
		if err != nil {
			return nil, jenkins.JobDetail{}, fail(err)
		}
		return nil, *d, nil
	})
}

// ---------------------------------------------------------------------------
// list_builds / get_build
// ---------------------------------------------------------------------------

type buildRow struct {
	jenkins.BuildSummary
	StartedAt string `json:"started_at,omitempty"`
}

type listBuildsIn struct {
	Job   string `json:"job" jsonschema:"full job name"`
	Limit int    `json:"limit,omitempty" jsonschema:"how many recent builds to return, 1-200 (default 20)"`
}

type listBuildsOut struct {
	Job    string     `json:"job"`
	Count  int        `json:"count"`
	Builds []buildRow `json:"builds"`
}

func (s *Server) registerListBuilds(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_builds",
		Title:       "List builds",
		Description: "List a job's most recent builds, newest first, with result and state. State is 'running' while a build is in progress, so a null result is never mistaken for success.",
		Annotations: readOnly(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listBuildsIn) (*mcp.CallToolResult, listBuildsOut, error) {
		builds, err := s.client.ListBuilds(ctx, in.Job, in.Limit)
		if err != nil {
			return nil, listBuildsOut{}, fail(err)
		}
		rows := make([]buildRow, 0, len(builds))
		for _, b := range builds {
			rows = append(rows, buildRow{BuildSummary: b, StartedAt: msToRFC3339(b.Timestamp)})
		}
		return nil, listBuildsOut{Job: in.Job, Count: len(rows), Builds: rows}, nil
	})
}

type getBuildIn struct {
	Job   string `json:"job" jsonschema:"full job name"`
	Build string `json:"build,omitempty" jsonschema:"build number, or lastBuild / lastSuccessfulBuild / lastFailedBuild / lastCompletedBuild (default lastBuild)"`
}

type getBuildOut struct {
	Number      int64          `json:"number"`
	Job         string         `json:"job"`
	URL         string         `json:"url,omitempty"`
	Result      string         `json:"result"`
	State       string         `json:"state"`
	Building    bool           `json:"building"`
	StartedAt   string         `json:"started_at,omitempty"`
	DurationMs  int64          `json:"duration_ms,omitempty"`
	EstimatedMs int64          `json:"estimated_duration_ms,omitempty"`
	QueueID     int64          `json:"queue_id,omitempty"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

func (s *Server) registerGetBuild(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get_build",
		Title:       "Get build",
		Description: "Read one build: result, whether it is still running, timing, and the parameters it was started with.",
		Annotations: readOnly(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in getBuildIn) (*mcp.CallToolResult, getBuildOut, error) {
		ref, err := buildRefOrDefault(in.Build)
		if err != nil {
			return nil, getBuildOut{}, fail(err)
		}
		b, err := s.client.GetBuild(ctx, in.Job, ref)
		if err != nil {
			return nil, getBuildOut{}, fail(err)
		}
		return nil, getBuildOut{
			Number: b.Number, Job: b.Job, URL: b.URL, Result: b.Result, State: b.State,
			Building: b.Building, StartedAt: msToRFC3339(b.Timestamp),
			DurationMs: b.DurationMs, EstimatedMs: b.EstimatedMs, QueueID: b.QueueID,
			Description: b.Description, Parameters: b.Parameters,
		}, nil
	})
}

// buildRefOrDefault applies the documented default of lastBuild.
func buildRefOrDefault(s string) (jenkins.BuildRef, error) {
	if strings.TrimSpace(s) == "" {
		return jenkins.RefLastBuild, nil
	}
	return jenkins.ParseBuildRef(s)
}

// ---------------------------------------------------------------------------
// get_console
// ---------------------------------------------------------------------------

type consoleIn struct {
	Job   string `json:"job" jsonschema:"full job name"`
	Build string `json:"build,omitempty" jsonschema:"build number or lastBuild / lastSuccessfulBuild / lastFailedBuild / lastCompletedBuild (default lastBuild)"`
	Lines int    `json:"lines,omitempty" jsonschema:"when starting fresh, how many trailing lines to return (default 200, max 5000)"`
	Start int64  `json:"start,omitempty" jsonschema:"byte cursor to resume from; pass next_start from a previous call to read only what is new"`
}

func (s *Server) registerConsole(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:  "get_console",
		Title: "Read console log",
		Description: "Read a build's console log. Without start it returns the last N lines; with start it returns everything after that byte cursor. " +
			"The result carries next_start and more_data: while more_data is true the build is still writing, so poll with next_start to follow the log. " +
			"Reads always begin at a line boundary, and a truncated read spills the full log to a file whose path is returned.",
		Annotations: readOnly(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in consoleIn) (*mcp.CallToolResult, jenkins.ConsoleResult, error) {
		ref, err := buildRefOrDefault(in.Build)
		if err != nil {
			return nil, jenkins.ConsoleResult{}, fail(err)
		}
		lines := in.Lines
		if lines > s.opts.MaxConsoleLines {
			lines = s.opts.MaxConsoleLines
		}
		res, err := s.client.ConsoleRead(ctx, in.Job, ref, jenkins.ConsoleOptions{
			TailLines: lines,
			Cursor:    in.Start,
			SpillDir:  s.opts.SpillDir,
		})
		if err != nil {
			return nil, jenkins.ConsoleResult{}, fail(err)
		}
		return nil, *res, nil
	})
}

// ---------------------------------------------------------------------------
// list_queue / list_nodes
// ---------------------------------------------------------------------------

type emptyIn struct{}

type listQueueOut struct {
	Count int                 `json:"count"`
	Items []jenkins.QueueItem `json:"items"`
}

func (s *Server) registerListQueue(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_queue",
		Title:       "List build queue",
		Description: "List builds waiting for an executor. Each item has its own id and a 'why' string explaining what it is waiting for; an item that has started moves to executable with a build number.",
		Annotations: readOnly(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyIn) (*mcp.CallToolResult, listQueueOut, error) {
		items, err := s.client.ListQueue(ctx)
		if err != nil {
			return nil, listQueueOut{}, fail(err)
		}
		return nil, listQueueOut{Count: len(items), Items: items}, nil
	})
}

func (s *Server) registerListNodes(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_nodes",
		Title:       "List executors",
		Description: "List the controller and its agents with executor counts and busy state. Use this to explain why a build is still queued.",
		Annotations: readOnly(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyIn) (*mcp.CallToolResult, jenkins.Cluster, error) {
		c, err := s.client.ListNodes(ctx)
		if err != nil {
			return nil, jenkins.Cluster{}, fail(err)
		}
		return nil, *c, nil
	})
}

// ---------------------------------------------------------------------------
// jenkins_get_path (Escape Hatch)
// ---------------------------------------------------------------------------

type getPathIn struct {
	Path     string `json:"path" jsonschema:"absolute controller path beginning with /, e.g. /job/foo/api/json?tree=name"`
	MaxBytes int64  `json:"max_bytes,omitempty" jsonschema:"response size budget in bytes (default 262144)"`
}

type getPathOut struct {
	Path      string `json:"path"`
	Body      string `json:"body"`
	Truncated bool   `json:"truncated"`
	TotalSize int64  `json:"total_bytes"`
	Note      string `json:"note,omitempty"`
}

func (s *Server) registerGetPath(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:  "jenkins_get_path",
		Title: "Raw GET (escape hatch)",
		Description: "Perform a GET against any controller path and return the raw body. GET only: it cannot change state. " +
			"Use it for plugin endpoints the other tools do not cover, such as /pluginManager/api/json or a specific job's api/json with a custom tree. " +
			"Large bodies are truncated to max_bytes.",
		Annotations: readOnly(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in getPathIn) (*mcp.CallToolResult, getPathOut, error) {
		path, err := jenkins.ValidateGetPath(in.Path)
		if err != nil {
			return nil, getPathOut{}, fail(err)
		}
		limit := in.MaxBytes
		if limit <= 0 {
			limit = s.opts.MaxPathBytes
		}
		resp, err := s.client.GetPath(ctx, path)
		if err != nil {
			return nil, getPathOut{}, fail(err)
		}
		env := jenkins.Truncate(resp.Body, limit)
		out := getPathOut{
			Path: path, Body: string(env.Body), Truncated: env.Truncated,
			TotalSize: env.TotalSize, Note: env.Note,
		}
		// Hint at JSON when the controller sent JSON, so the agent does not have
		// to re-encode an escaped string in its head.
		if ct := resp.Header.Get("Content-Type"); strings.Contains(ct, "json") && json.Valid(resp.Body) && !env.Truncated {
			out.Body = string(resp.Body)
		}
		return nil, out, nil
	})
}

// registerReadTools installs every Read Tool.
func (s *Server) registerReadTools(srv *mcp.Server) {
	s.registerInfo(srv)
	s.registerListJobs(srv)
	s.registerGetJob(srv)
	s.registerListBuilds(srv)
	s.registerGetBuild(srv)
	s.registerConsole(srv)
	s.registerListQueue(srv)
	s.registerListNodes(srv)
	s.registerGetPath(srv)
}
