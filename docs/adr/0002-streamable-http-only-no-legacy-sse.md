# Serve MCP over Streamable HTTP only, not the legacy HTTP+SSE transport

"Support stdio and stream" is satisfied by stdio plus Streamable HTTP
(protocol 2025-03-26): one endpoint, POST for requests, optional GET for
server-initiated messages. The older HTTP+SSE transport (2024-11-05) needs two
endpoints and a per-client session store maintained by us, and is deprecated in
current clients. We do not implement it.

Considered options:

- **Both transports.** Doubles the HTTP surface and the session state to manage,
  for clients that are themselves on the way out.
- **Streamable HTTP, stateless mode.** Simplest to operate, but forbids
  server-initiated progress and resumability, which we would want before adding
  build-log streaming.

Consequences:

- HTTP mode runs stateful Streamable HTTP with a bounded session cache, so a
  client reconnect resumes rather than restarts.
- Build-log "streaming" is not done through the transport at all; callers poll a
  cursor-based tool instead (see the console-log tool), which keeps one code path
  for both stdio and HTTP clients.
