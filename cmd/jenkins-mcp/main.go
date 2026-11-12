// Command jenkins-mcp exposes a Jenkins controller over the Model Context
// Protocol, over stdio or Streamable HTTP.
package main

import (
	"context"
	"crypto/subtle"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/zouxiaoliang/jenkins-mcp-go/internal/config"
	"github.com/zouxiaoliang/jenkins-mcp-go/internal/jenkins"
	"github.com/zouxiaoliang/jenkins-mcp-go/internal/mcpserver"
)

// version is overridden at build time with -ldflags.
var version = "dev"

func main() {
	if err := run(); err != nil {
		// stdout may be the MCP channel; diagnostics always go to stderr.
		fmt.Fprintln(os.Stderr, "jenkins-mcp:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		transport  = flag.String("transport", "stdio", "transport to serve: stdio or http")
		configPath = flag.String("config", config.DefaultPath(), "path to the JSON config file")
		listen     = flag.String("listen", "", "override the Streamable HTTP listen address (http transport only)")
		profile    = flag.String("profile", "", "profile name to use instead of activeProfile")
		showVer    = flag.Bool("version", false, "print the version and exit")
	)
	flag.Parse()

	if *showVer {
		fmt.Println(version)
		return nil
	}
	if *transport != "stdio" && *transport != "http" {
		return fmt.Errorf("-transport must be stdio or http, got %q", *transport)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if *profile != "" {
		cfg.ActiveProfile = *profile
	}
	p, err := cfg.Active()
	if err != nil {
		return err
	}

	token := cfg.EnvToken()
	if token == "" {
		token, err = config.ReadToken(p.TokenFile)
		if err != nil {
			return err
		}
	}
	if token != "" && p.User == "" {
		return errors.New("a token was supplied without a user; Jenkins basic auth needs both")
	}

	timeout := time.Duration(p.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	client, err := jenkins.NewClient(jenkins.Options{
		BaseURL:            p.URL,
		User:               p.User,
		Token:              token,
		Timeout:            timeout,
		InsecureSkipVerify: p.InsecureSkipVerify,
	})
	if err != nil {
		return err
	}

	srv := mcpserver.NewServer(client, mcpserver.Options{
		AllowWrite:  p.AllowWrite,
		AllowedJobs: p.AllowedJobs,
		Version:     version,
	})

	mode := "read-only"
	if p.AllowWrite {
		mode = "read-write"
	}
	logger.Info("starting",
		"controller", client.BaseURL(),
		"auth", client.AuthKind(),
		"mode", mode,
		"transport", *transport,
		"version", version)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch *transport {
	case "stdio":
		// Nothing may be written to stdout except the protocol itself.
		return srv.Run(ctx, &mcp.StdioTransport{})
	case "http":
		return serveHTTP(ctx, srv, cfg, p, *listen, logger)
	}
	return nil
}

// serveHTTP runs the Streamable HTTP transport with the listener's own
// authentication policy enforced.
func serveHTTP(ctx context.Context, srv *mcp.Server, cfg *config.Config, p config.Profile, listen string, logger *slog.Logger) error {
	addr := listen
	if addr == "" {
		addr = cfg.HTTP.Listen
	}
	if addr == "" {
		addr = "127.0.0.1:8765"
	}

	// A non-loopback listener without a shared secret would hand every client on
	// the network this bridge's Jenkins authority. Refuse to start instead.
	var bearer string
	if cfg.HTTP.ServerAuthTokenFile != "" {
		tok, err := config.ReadToken(cfg.HTTP.ServerAuthTokenFile)
		if err != nil {
			return err
		}
		bearer = tok
	}
	if !config.Loopback(addr) && bearer == "" {
		return fmt.Errorf("refusing to listen on %s without http.serverAuthTokenFile: "+
			"a non-loopback listener would let anyone on the network act as this bridge", addr)
	}

	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{
			// Stateful sessions so a client can resume, but idle ones are reaped.
			SessionTimeout: 30 * time.Minute,
			Logger:         logger,
		})

	mux := http.NewServeMux()
	mux.Handle("/mcp", handler)
	mux.Handle("/mcp/", handler)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"ok","version":%q}`, version)
	})

	var root http.Handler = mux
	if bearer != "" {
		root = requireBearer(mux, bearer)
	}

	hs := &http.Server{
		Addr:              addr,
		Handler:           root,
		ReadHeaderTimeout: 10 * time.Second,
		// Log streams and long tool calls need generous per-request timeouts;
		// WriteTimeout is deliberately unset so SSE streams are not cut.
		IdleTimeout: 120 * time.Second,
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", addr, err)
	}
	logger.Info("listening", "addr", ln.Addr().String(), "endpoint", "/mcp",
		"auth", ternary(bearer != "", "bearer", "none"))

	errCh := make(chan error, 1)
	go func() { errCh <- hs.Serve(ln) }()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		logger.Info("shutting down")
		return hs.Shutdown(shutdownCtx)
	}
}

// requireBearer rejects requests without the configured shared secret.
func requireBearer(next http.Handler, token string) http.Handler {
	want := []byte(token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("Authorization")
		const prefix = "Bearer "
		got := ""
		if strings.HasPrefix(h, prefix) {
			got = h[len(prefix):]
		}
		if subtle.ConstantTimeCompare([]byte(got), want) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="jenkins-mcp"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func ternary(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}
