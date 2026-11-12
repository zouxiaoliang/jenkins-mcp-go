# Use the official MCP Go SDK instead of hand-rolling JSON-RPC

The bridge must speak MCP over stdio and over HTTP. The project's default rule is
stdlib-only, but MCP is a moving spec whose framing (stdio newline-delimited
JSON-RPC), session lifecycle, and Streamable HTTP transport are all
error-prone to reimplement. We use `github.com/modelcontextprotocol/go-sdk`
(pinned v1.8.0) and accept it as the project's one non-stdlib dependency.

Considered options:

- **Hand-rolled JSON-RPC.** ~600+ lines of transport and session code to own
  forever, with a spec that changes; rejected.
- **`mark3labs/mcp-go`.** Community SDK, viable, but trails spec changes and was
  not in the local module cache; the official SDK was, so it also builds offline.

Consequences:

- The dependency requires `go 1.25.0`, which becomes the project's build floor.
- We get typed tool handlers for free: input schemas, argument validation and
  structured output are generated from Go types, so tool contracts cannot drift
  from their documentation.
- SDK upgrades are the main maintenance risk; the MCP surface we use is
  deliberately narrow (tools only, no prompts/resources/sampling) to keep that
  risk small.
