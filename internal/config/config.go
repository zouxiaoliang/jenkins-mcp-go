package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Profile is one self-contained set of connection settings.
type Profile struct {
	URL                string   `json:"url"`
	User               string   `json:"user"`
	TokenFile          string   `json:"tokenFile"`
	AllowWrite         bool     `json:"allowWrite"`
	AllowedJobs        []string `json:"allowedJobs"`
	TimeoutSeconds     int      `json:"timeoutSeconds"`
	InsecureSkipVerify bool     `json:"insecureSkipVerify"`
}

// HTTP is the Streamable HTTP listener configuration.
type HTTP struct {
	Listen              string `json:"listen"`
	ServerAuthTokenFile string `json:"serverAuthTokenFile"`
}

// Config is the whole file.
type Config struct {
	ActiveProfile string             `json:"activeProfile"`
	Profiles      map[string]Profile `json:"profiles"`
	HTTP          HTTP               `json:"http"`

	// envToken is injected from JENKINS_TOKEN. It is unexported so encoding/json
	// never reads or writes it, and it can never end up in the config file.
	envToken string
}

// EnvToken reports the token supplied through the environment, if any.
func (c *Config) EnvToken() string { return c.envToken }

// DefaultPath is where the bridge looks for its configuration.
func DefaultPath() string {
	if p := os.Getenv("JENKINS_MCP_CONFIG"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "config.json"
	}
	return filepath.Join(home, ".config", "jenkins-mcp", "config.json")
}

// Load reads and validates the configuration file, then applies environment
// overrides.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config %s: %w", path, err)
	}
	var cfg Config
	dec := json.NewDecoder(bytes.NewReader(raw))
	// A typo in a config key should be loud: this file decides what an agent may
	// do to a build server.
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parsing config %s: %w", path, err)
	}
	if cfg.Profiles == nil {
		cfg.Profiles = map[string]Profile{}
	}
	if cfg.ActiveProfile == "" {
		if _, ok := cfg.Profiles["default"]; ok {
			cfg.ActiveProfile = "default"
		} else if len(cfg.Profiles) == 1 {
			for name := range cfg.Profiles {
				cfg.ActiveProfile = name
			}
		}
	}
	if err := cfg.applyEnv(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// applyEnv lets an operator override the connection without editing the file.
// The token is accepted here and only here: never on the command line, where it
// would be visible in ps and in shell history.
func (c *Config) applyEnv() error {
	if c.ActiveProfile == "" {
		if v := os.Getenv("JENKINS_PROFILE"); v != "" {
			c.ActiveProfile = v
		}
	}
	if c.ActiveProfile == "" && os.Getenv("JENKINS_URL") == "" {
		return nil
	}
	p := c.Profiles[c.ActiveProfile]

	if v := os.Getenv("JENKINS_URL"); v != "" {
		p.URL = v
	}
	if v := os.Getenv("JENKINS_USER"); v != "" {
		p.User = v
	}
	if v := os.Getenv("JENKINS_ALLOW_WRITE"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("JENKINS_ALLOW_WRITE=%q is not a boolean", v)
		}
		p.AllowWrite = b
	}
	if v := os.Getenv("JENKINS_ALLOWED_JOBS"); v != "" {
		p.AllowedJobs = splitList(v)
	}
	if v := os.Getenv("JENKINS_TOKEN"); v != "" {
		c.envToken = v
	}
	c.Profiles[c.ActiveProfile] = p
	return nil
}

func splitList(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ReadToken reads a token file and refuses one that other users can read.
//
// This is not ceremony: the token grants whatever the Jenkins user can do, which
// on the reference controller includes triggering release builds.
func ReadToken(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("reading token file %s: %w", path, err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("token file %s is readable by other users (mode %04o); run: chmod 600 %s",
			path, info.Mode().Perm(), path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading token file %s: %w", path, err)
	}
	return strings.TrimSpace(string(b)), nil
}

// Active returns the active profile, failing loudly if it is missing.
func (c *Config) Active() (Profile, error) {
	if c.ActiveProfile == "" {
		return Profile{}, fmt.Errorf("no active profile; set activeProfile in the config or JENKINS_URL")
	}
	p, ok := c.Profiles[c.ActiveProfile]
	if !ok {
		return Profile{}, fmt.Errorf("active profile %q is not defined in the config file", c.ActiveProfile)
	}
	if p.URL == "" {
		return Profile{}, fmt.Errorf("profile %q has no url", c.ActiveProfile)
	}
	return p, nil
}

// Loopback reports whether addr binds only to this machine.
func Loopback(addr string) bool {
	host := addr
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		host = addr[:i]
	}
	host = strings.Trim(host, "[]")
	if host == "localhost" || host == "::1" {
		return true
	}
	return strings.HasPrefix(host, "127.")
}
