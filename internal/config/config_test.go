package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTemp(t *testing.T, name, content string, mode os.FileMode) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		t.Fatalf("writing %s: %v", p, err)
	}
	// WriteFile honours umask, so set the mode explicitly.
	if err := os.Chmod(p, mode); err != nil {
		t.Fatalf("chmod %s: %v", p, err)
	}
	return p
}

func TestReadTokenRejectsWorldReadableFile(t *testing.T) {
	p := writeTemp(t, "token", "s3cret", 0o644)
	if _, err := ReadToken(p); err == nil {
		t.Fatal("a 0644 token file must be refused: the token grants the Jenkins user's authority")
	}
}

func TestReadTokenAcceptsPrivateFile(t *testing.T) {
	p := writeTemp(t, "token", "s3cret\n", 0o600)
	tok, err := ReadToken(p)
	if err != nil {
		t.Fatalf("ReadToken: %v", err)
	}
	if tok != "s3cret" {
		t.Fatalf("token = %q", tok)
	}
}

func TestReadTokenEmptyPathIsNotAnError(t *testing.T) {
	tok, err := ReadToken("")
	if err != nil || tok != "" {
		t.Fatalf("got %q, %v; anonymous mode must be expressible", tok, err)
	}
}

func TestLoadRejectsUnknownKeys(t *testing.T) {
	p := writeTemp(t, "config.json", `{"activeProfile":"default","profiles":{"default":{"url":"http://x"}},"allowWrite":true}`, 0o600)
	if _, err := Load(p); err == nil {
		t.Fatal("a typo'd top-level key must fail loudly, not be ignored")
	}
}

func TestLoadAppliesEnvOverrides(t *testing.T) {
	p := writeTemp(t, "config.json", `{
		"activeProfile":"default",
		"profiles":{"default":{"url":"http://from-file:8080","user":"fileuser","allowWrite":false}}
	}`, 0o600)

	t.Setenv("JENKINS_URL", "http://from-env:8080")
	t.Setenv("JENKINS_USER", "envuser")
	t.Setenv("JENKINS_ALLOW_WRITE", "true")
	t.Setenv("JENKINS_TOKEN", "envtoken")

	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	prof, err := cfg.Active()
	if err != nil {
		t.Fatalf("Active: %v", err)
	}
	if prof.URL != "http://from-env:8080" {
		t.Errorf("url = %q, env did not win", prof.URL)
	}
	if prof.User != "envuser" {
		t.Errorf("user = %q", prof.User)
	}
	if !prof.AllowWrite {
		t.Error("allowWrite override was not applied")
	}
	if cfg.EnvToken() != "envtoken" {
		t.Errorf("token = %q", cfg.EnvToken())
	}
}

func TestLoadRejectsBadBoolEnv(t *testing.T) {
	p := writeTemp(t, "config.json", `{"activeProfile":"d","profiles":{"d":{"url":"http://x"}}}`, 0o600)
	t.Setenv("JENKINS_ALLOW_WRITE", "yes-please")
	if _, err := Load(p); err == nil {
		t.Fatal("a nonsense boolean must fail rather than silently mean false")
	}
}

func TestActiveProfileMissing(t *testing.T) {
	p := writeTemp(t, "config.json", `{"activeProfile":"nope","profiles":{"default":{"url":"http://x"}}}`, 0o600)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := cfg.Active(); err == nil {
		t.Fatal("an undefined active profile must be an error")
	}
}

func TestLoopback(t *testing.T) {
	loop := []string{"127.0.0.1:8765", "localhost:8765", "[::1]:8765", "127.1.2.3:1"}
	for _, a := range loop {
		if !Loopback(a) {
			t.Errorf("Loopback(%q) = false, want true", a)
		}
	}
	open := []string{"0.0.0.0:8765", ":8765", "192.168.1.10:8765", "[::]:8765"}
	for _, a := range open {
		if Loopback(a) {
			t.Errorf("Loopback(%q) = true, want false", a)
		}
	}
}
