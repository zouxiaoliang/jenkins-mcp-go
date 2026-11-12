package mcpserver

import (
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/zouxiaoliang/jenkins-mcp-go/internal/jenkins"
)

// Options configures the tool surface.
type Options struct {
	// AllowWrite is the Write Gate. When false, no write tool is registered at
	// all, so an agent can see from tools/list that it cannot change anything.
	AllowWrite bool
	// AllowedJobs restricts which jobs may be triggered. Empty means "any job",
	// which is only sound when writes are explicitly enabled by the operator.
	AllowedJobs []string
	// WaitForBuild bounds how long a trigger waits for the build to leave the
	// queue before reporting status "queued".
	WaitForBuild time.Duration
	// SpillDir receives full console logs when a response is truncated.
	SpillDir string
	// MaxConsoleLines caps how many lines one console read may return.
	MaxConsoleLines int
	// MaxPathBytes caps the Escape Hatch's response size.
	MaxPathBytes int64
	// Version is reported in the MCP implementation block.
	Version string
}

func (o *Options) withDefaults() {
	if o.WaitForBuild <= 0 {
		o.WaitForBuild = 15 * time.Second
	}
	if o.MaxConsoleLines <= 0 {
		o.MaxConsoleLines = 5000
	}
	if o.MaxPathBytes <= 0 {
		o.MaxPathBytes = 256 << 10 // 256 KiB
	}
	if o.SpillDir == "" {
		o.SpillDir = "/tmp/jenkins-mcp"
	}
	if o.Version == "" {
		o.Version = "dev"
	}
}

// Server bundles the client with its tool options so handlers can stay free of
// globals.
type Server struct {
	client *jenkins.Client
	opts   Options
}

// NewServer builds the MCP server and registers the tool surface.
func NewServer(client *jenkins.Client, opts Options) *mcp.Server {
	opts.withDefaults()
	s := &Server{client: client, opts: opts}

	srv := mcp.NewServer(&mcp.Implementation{
		Name:    "jenkins-mcp",
		Version: opts.Version,
	}, &mcp.ServerOptions{
		Instructions: instructions(opts),
	})

	s.registerReadTools(srv)
	if opts.AllowWrite {
		s.registerWriteTools(srv)
	}
	return srv
}

// instructions tells the agent how to behave against this controller before it
// picks a tool. Capability limits belong here, not in a failed call.
func instructions(opts Options) string {
	base := "Bridge to a Jenkins controller. " +
		"Address jobs by full name; folders use slashes (folder/sub/job). " +
		"Builds accept a number or lastBuild / lastSuccessfulBuild / lastFailedBuild / lastCompletedBuild. " +
		"Console logs are read incrementally: pass next_start from the previous result to continue. "
	if opts.AllowWrite {
		return base + "Builds can be triggered and stopped; writes are enabled for this session."
	}
	return base + "This session is read-only: no tool can change controller state."
}

func boolPtr(b bool) *bool { return &b }

// readOnly annotates a tool that never changes controller state.
func readOnly() *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{
		ReadOnlyHint:  true,
		OpenWorldHint: boolPtr(false),
	}
}

// destructive annotates a write tool. Every write tool here can discard work, so
// none of them is marked additive.
func destructive() *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{
		ReadOnlyHint:    false,
		DestructiveHint: boolPtr(true),
		OpenWorldHint:   boolPtr(false),
	}
}

// writeAllowed reports whether the Write Gate is open. It is checked at call time
// as well as at registration time: registration is a fast path, the check is the
// guarantee.
func (s *Server) writeAllowed() error {
	if !s.opts.AllowWrite {
		return jenkins.NewError(jenkins.CodeWriteDisabled,
			"writes are disabled for this session", "start jenkins-mcp with allowWrite enabled")
	}
	return nil
}

// jobAllowed enforces the trigger whitelist.
func (s *Server) jobAllowed(job string) error {
	if len(s.opts.AllowedJobs) == 0 {
		return nil
	}
	for _, j := range s.opts.AllowedJobs {
		if j == job {
			return nil
		}
	}
	return jenkins.NewError(jenkins.CodeForbidden,
		"job is not in the allowed list for this session",
		"allowed jobs: "+joinComma(s.opts.AllowedJobs))
}

// maxWait bounds a trigger's queue wait so a tool call cannot hang a client.
const maxWait = 120 * time.Second

func timeSeconds(n int) time.Duration { return time.Duration(n) * time.Second }

func joinComma(in []string) string {
	out := ""
	for i, s := range in {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}
