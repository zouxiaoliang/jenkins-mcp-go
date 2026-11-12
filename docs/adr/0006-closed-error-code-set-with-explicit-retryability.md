# Every failure is one of a closed set of codes plus an explicit `retryable`

Tool failures are returned as `{"error":{"code","message","retryable","detail"}}`
where `code` comes from a fixed enumeration: `not_found`, `forbidden`,
`auth_failed`, `csrf_failed`, `write_disabled`, `invalid_parameter`,
`job_has_no_parameters`, `queue_timeout`, `controller_unreachable`, `bad_gateway`,
`bad_request`, `internal`.

The alternative — surfacing HTTP status text — was rejected because an agent
cannot reliably derive intent from it. The distinction that actually matters is
whether retrying helps: `forbidden` never does, `controller_unreachable` usually
does. Leaving that to prose means an agent either gives up too early or hammers a
permission wall.

`retryable` is derived from `code` in one place rather than set by each call site,
so the two can never disagree.

Consequences:

- `404` and `403` must stay distinct end to end. "The job does not exist" and
  "you may not see this job" are different facts, and collapsing them turns a
  permission bug into an apparently empty controller.
- MCP carries errors as text content with `IsError=true`, so the text is JSON
  rather than a sentence. That is a deliberate trade: machine-readable beats
  pretty in a tool result.
- Adding a code is cheap; reusing one for a new meaning is not, because agents
  will branch on it.
