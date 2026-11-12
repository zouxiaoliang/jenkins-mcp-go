# jenkins-mcp

An MCP server that exposes a Jenkins controller to an agent, over **stdio** or
**Streamable HTTP**.

Design decisions are recorded in [`docs/adr/`](./docs/adr); the vocabulary is in
[`GLOSSARY.md`](./GLOSSARY.md).

## What it gives an agent

| Tool | Kind | Purpose |
|---|---|---|
| `jenkins_info` | read | Controller version, auth mode, whether writes are enabled |
| `list_jobs` | read | Jobs in the root or one folder |
| `get_job` | read | One job: state, last builds, **parameter definitions** |
| `list_builds` | read | Recent builds with result and state |
| `get_build` | read | One build: result, running?, timing, its parameters |
| `get_console` | read | Console log, tail or resumed from a byte cursor |
| `list_queue` | read | Builds waiting for an executor, with the reason |
| `list_nodes` | read | Controller and agents, executor counts, busy state |
| `jenkins_get_path` | read | Escape hatch: GET any controller path |
| `trigger_build` | **write** | Request a build, parameters validated first |
| `stop_build` | **write** | Abort a running build or cancel a queued one |
| `cancel_queue_item` | **write** | Remove a waiting build by queue id |

Write tools are **not registered at all** unless writes are enabled, so
`tools/list` is an honest statement of what the session can do.

## Build

```sh
just build          # static binary for the host
just release        # static linux/amd64 and linux/arm64
just check          # gofmt + vet + build
just test           # offline unit tests
just test-race      # unit tests with the race detector
```

Go 1.25+ is required (the MCP SDK's floor). Default dependencies resolve from the
module cache; the only non-stdlib dependency is the official MCP Go SDK
(ADR-0001).

## Configure

```sh
mkdir -p ~/.config/jenkins-mcp
cp config.example.json ~/.config/jenkins-mcp/config.json
```

The token is never a command-line flag. Use one of:

```sh
# token file, must be 0600 or the bridge refuses to start
printf 'your-api-token' > ~/.config/jenkins-mcp/token
chmod 600 ~/.config/jenkins-mcp/token

# or the environment
export JENKINS_USER=zouxiaoliang JENKINS_TOKEN=...
```

Environment overrides: `JENKINS_URL`, `JENKINS_USER`, `JENKINS_TOKEN`,
`JENKINS_PROFILE`, `JENKINS_ALLOW_WRITE`, `JENKINS_ALLOWED_JOBS`,
`JENKINS_MCP_CONFIG`.

Without credentials the bridge runs in **Anonymous Mode**: read tools work where
the controller allows anonymous read, write tools stay unregistered, and
`jenkins_info` reports `auth: anonymous`.

## Run

```sh
# stdio — for a local MCP client
jenkins-mcp --transport stdio

# Streamable HTTP
jenkins-mcp --transport http            # 127.0.0.1:8765/mcp
jenkins-mcp --transport http --listen 127.0.0.1:9000
```

A non-loopback listen address requires `http.serverAuthTokenFile`; without it the
bridge refuses to start rather than expose the controller's authority to the
network.

### Client configuration

```json
{
  "mcpServers": {
    "jenkins": {
      "command": "/path/to/jenkins-mcp",
      "args": ["--transport", "stdio"]
    }
  }
}
```

## Enabling writes

```json
{
  "profiles": {
    "default": {
      "allowWrite": true,
      "allowedJobs": ["ModuleSubmitBuild"]
    }
  }
}
```

`allowedJobs` empty means every job may be triggered. That is only sound when the
operator has decided it is; prefer an explicit list.

## Behaviour worth knowing

- **Build references** accept a number or `lastBuild`, `lastSuccessfulBuild`,
  `lastFailedBuild`, `lastCompletedBuild`.
- **Job names** are full names; folders use slashes (`folder/sub/job`).
- **Console logs** are read with a byte cursor: `next_start` always lands on a
  line boundary, reads never split a UTF-8 character, and `more_data` is true only
  while the build is still writing.
- **Truncation** is reported, never silent: a tail read says how many lines of how
  large a log it returned, and the full log is spilled to a file whose path comes
  back with the result.
- **A queued result is not an error.** On a busy controller a triggered build
  legitimately stays in the queue; `status: "queued"` with a `why` says so.

## Verification status

Verified against a live Jenkins **2.492.3** (anonymous read):

- all nine read tools execute and return real data, over both stdio and
  Streamable HTTP
- a finished build reports `more_data: false` (Jenkins omits `X-More-Data` when
  the log is complete — assuming otherwise made finished builds look live)
- `list_nodes` decodes the controller's real executor payload (44 nodes,
  78 executors)
- write tools are absent from `tools/list` while the write gate is closed
- a non-loopback listener without a bearer token refuses to start
- Go's race detector is clean

**Not verified** (no usable credentials on the development machine):

- `trigger_build`, `stop_build`, `cancel_queue_item` against a real controller —
  every anonymous write returns 403. Their request shapes are unit-tested against
  a fake controller (including the crumb fetch and the stale-crumb retry), but no
  real build was ever triggered by this bridge.
- Bearer authentication on the HTTP listener (the middleware is unit-tested, but
  no client was run against it).
- Behaviour against a controller with folders, or a version other than 2.492.3.
