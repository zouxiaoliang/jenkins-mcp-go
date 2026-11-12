# jenkins-mcp development tasks.
# Run `just` to see the list.

version := `git describe --tags --always --dirty 2>/dev/null || echo dev`
dist := "dist"

# List recipes.
default:
    @just --list

# Build a static binary for the host.
build:
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version={{version}}" -o {{dist}}/jenkins-mcp ./cmd/jenkins-mcp

# Install the static binary into ~/.local/bin.
install: build
    install -Dm755 {{dist}}/jenkins-mcp "$HOME/.local/bin/jenkins-mcp"
    @echo "installed $HOME/.local/bin/jenkins-mcp"

# Build static binaries for the release targets.
release: (build-cross "linux" "amd64") (build-cross "linux" "arm64")

build-cross os arch:
    CGO_ENABLED=0 GOOS={{os}} GOARCH={{arch}} go build -trimpath -ldflags "-s -w -X main.version={{version}}" -o {{dist}}/jenkins-mcp-{{os}}-{{arch}} ./cmd/jenkins-mcp

# Format and vet.
fmt:
    gofmt -w .

# Everything that must pass before a commit.
check:
    #!/usr/bin/env bash
    set -euo pipefail
    unformatted=$(gofmt -l .)
    if [ -n "$unformatted" ]; then
        echo "unformatted files:" >&2
        echo "$unformatted" >&2
        exit 1
    fi
    go vet ./...
    go build ./...
    echo "check: ok"

# Unit tests. Offline: no network, no controller.
test:
    go test ./...

# Race detector, because the client caches the crumb and job parameters.
test-race:
    go test -race ./...

# Read-only end-to-end against a live controller.
# Set JENKINS_MCP_E2E_CONFIG to a config file pointing at it.
e2e:
    go build -o {{dist}}/jenkins-mcp ./cmd/jenkins-mcp
    ./scripts/e2e.sh

# Remove build output.
clean:
    rm -rf {{dist}}
