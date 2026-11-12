# Split read and write tools, and only register write tools when enabled

The bridge exposes one tool per operation rather than a few tools with an
`action` enum. MCP clients pick tools by name and description, so an enum
degenerates into the agent guessing parameter shapes and reading error messages.
More importantly, a per-operation split lets the read/write boundary be enforced
at *registration* time: with writes disabled, `trigger_build`, `stop_build` and
`cancel_queue_item` are never advertised at all, instead of being advertised and
then refused.

Read tools: `jenkins_info`, `list_jobs`, `get_job`, `list_builds`, `get_build`,
`get_console`, `list_queue`, `list_nodes`, `jenkins_get_path`.

Write tools: `trigger_build`, `stop_build`, `cancel_queue_item`.

The same reasoning produces one deliberate exception to "no open-ended tools":
`jenkins_get_path` accepts an arbitrary GET path. Real controllers have plugin
surfaces no fixed tool can anticipate, and a missing field would otherwise cost a
code change and a re-release. Its restrictions are non-negotiable: GET only,
path must start with `/`, no `../` segments, no scheme or authority, and the
result goes through the same Truncation Envelope as every other tool.

Consequences:

- Capability limits are visible in `tools/list`, not discovered through 403s.
- `jenkins_get_path` is the only tool whose risk is not bounded by its name, so
  it is the one place that needs path validation tests.
