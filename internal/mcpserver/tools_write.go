package mcpserver

import (
	"context"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/zouxiaoliang/jenkins-mcp-go/internal/jenkins"
)

// ---------------------------------------------------------------------------
// trigger_build
// ---------------------------------------------------------------------------

type triggerIn struct {
	Job         string         `json:"job" jsonschema:"full job name to build"`
	Params      map[string]any `json:"params,omitempty" jsonschema:"parameter values keyed by the job's parameter definitions; omit to accept every default. Multi-select parameters accept an array of strings."`
	WaitSeconds int            `json:"wait_seconds,omitempty" jsonschema:"how long to wait for the build to leave the queue (default 15, max 120). A queued result is not an error."`
}

type triggerOut struct {
	Action      string `json:"action_taken"`
	Job         string `json:"job"`
	QueueID     int64  `json:"queue_id,omitempty"`
	Status      string `json:"status"`
	Why         string `json:"why,omitempty"`
	BuildNumber int64  `json:"build_number,omitempty"`
	BuildURL    string `json:"build_url,omitempty"`
	Note        string `json:"note,omitempty"`
}

func (s *Server) registerTrigger(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:  "trigger_build",
		Title: "Trigger build",
		Description: "Request a build. Parameters are validated against the job's own parameter definitions first, so a mistyped name fails instead of silently becoming the default. " +
			"If the build does not start within wait_seconds the result is status=queued with the reason, which is a normal outcome on a busy controller.",
		Annotations: destructive(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in triggerIn) (*mcp.CallToolResult, triggerOut, error) {
		if err := s.writeAllowed(); err != nil {
			return nil, triggerOut{}, fail(err)
		}
		if strings.TrimSpace(in.Job) == "" {
			return nil, triggerOut{}, fail(jenkins.NewError(jenkins.CodeInvalidParameter, "job is required", ""))
		}
		if err := s.jobAllowed(in.Job); err != nil {
			return nil, triggerOut{}, fail(err)
		}
		wait := s.opts.WaitForBuild
		if in.WaitSeconds > 0 {
			wait = timeSeconds(in.WaitSeconds)
		}
		if wait > maxWait {
			wait = maxWait
		}
		res, err := s.client.Trigger(ctx, jenkins.BuildRequest{Job: in.Job, Params: in.Params}, wait)
		if err != nil {
			return nil, triggerOut{}, fail(err)
		}
		out := triggerOut{
			Action: "triggered", Job: in.Job, QueueID: res.QueueID, Status: res.Status,
			Why: res.Why, BuildNumber: res.BuildNumber, BuildURL: res.BuildURL,
		}
		if res.Status == "queued" {
			out.Note = "the build is still waiting for an executor; use list_queue or get_build to follow it"
		}
		return nil, out, nil
	})
}

// ---------------------------------------------------------------------------
// stop_build
// ---------------------------------------------------------------------------

type stopIn struct {
	Job     string `json:"job" jsonschema:"full job name"`
	Build   string `json:"build,omitempty" jsonschema:"build number or lastBuild (default when queue_id is omitted)"`
	QueueID int64  `json:"queue_id,omitempty" jsonschema:"queue item id to cancel instead, for a build that has not started yet"`
}

type stopOut struct {
	Action  string `json:"action_taken"`
	Job     string `json:"job"`
	Build   string `json:"build,omitempty"`
	QueueID int64  `json:"queue_id,omitempty"`
	Note    string `json:"note,omitempty"`
}

func (s *Server) registerStop(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:  "stop_build",
		Title: "Stop or cancel a build",
		Description: "Stop a running build, or cancel a queued one. Pass queue_id to cancel something that has not started; otherwise build selects the build to abort. " +
			"action_taken reports which actually happened, because the two are different controller operations.",
		Annotations: destructive(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in stopIn) (*mcp.CallToolResult, stopOut, error) {
		if err := s.writeAllowed(); err != nil {
			return nil, stopOut{}, fail(err)
		}
		if strings.TrimSpace(in.Job) == "" {
			return nil, stopOut{}, fail(jenkins.NewError(jenkins.CodeInvalidParameter, "job is required", ""))
		}
		if in.QueueID > 0 {
			if strings.TrimSpace(in.Build) != "" {
				return nil, stopOut{}, fail(jenkins.NewError(jenkins.CodeInvalidParameter,
					"build and queue_id are mutually exclusive",
					"a queued build has no number; pass only queue_id for it"))
			}
			if err := s.client.CancelQueueItem(ctx, in.QueueID); err != nil {
				return nil, stopOut{}, fail(err)
			}
			return nil, stopOut{
				Action: "cancelled_queue_item", Job: in.Job, QueueID: in.QueueID,
				Note: "the build never started, so nothing was aborted",
			}, nil
		}
		ref, err := buildRefOrDefault(in.Build)
		if err != nil {
			return nil, stopOut{}, fail(err)
		}
		if err := s.client.StopBuild(ctx, in.Job, ref); err != nil {
			return nil, stopOut{}, fail(err)
		}
		return nil, stopOut{
			Action: "stopped", Job: in.Job, Build: string(ref),
			Note: "the controller aborts the build asynchronously; state becomes ABORTED shortly after",
		}, nil
	})
}

// ---------------------------------------------------------------------------
// cancel_queue_item
// ---------------------------------------------------------------------------

type cancelQueueIn struct {
	QueueID int64 `json:"queue_id" jsonschema:"id of the queue item, as reported by list_queue or trigger_build"`
}

type cancelQueueOut struct {
	Action  string `json:"action_taken"`
	QueueID int64  `json:"queue_id"`
}

func (s *Server) registerCancelQueueItem(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "cancel_queue_item",
		Title:       "Cancel queue item",
		Description: "Remove a waiting build from the queue by its queue item id. Use this rather than stop_build when you already hold a queue id.",
		Annotations: destructive(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in cancelQueueIn) (*mcp.CallToolResult, cancelQueueOut, error) {
		if err := s.writeAllowed(); err != nil {
			return nil, cancelQueueOut{}, fail(err)
		}
		if err := s.client.CancelQueueItem(ctx, in.QueueID); err != nil {
			return nil, cancelQueueOut{}, fail(err)
		}
		return nil, cancelQueueOut{Action: "cancelled_queue_item", QueueID: in.QueueID}, nil
	})
}

// registerWriteTools installs every Write Tool. It is only called when the Write
// Gate is open, so a read-only session never advertises these names at all.
func (s *Server) registerWriteTools(srv *mcp.Server) {
	s.registerTrigger(srv)
	s.registerStop(srv)
	s.registerCancelQueueItem(srv)
}
