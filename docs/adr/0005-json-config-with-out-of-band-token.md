# Configuration is JSON, and the token never lives in it

The bridge's configuration is a single JSON file parsed with `encoding/json`, and
the API token lives in a separate 0600 file (or `JENKINS_TOKEN`), never inside the
configuration.

Chosen over YAML/TOML because the file has fewer than ten fields, is written once,
and YAML would add a second non-stdlib dependency right after ADR-0001 argued for
exactly one. Hand-written comments were the only thing lost, and they were not
worth a dependency.

Chosen over a command-line `--token` flag because argv is world-readable through
`ps` and is recorded in shell history. A token that can trigger release builds does
not belong in either.

Consequences:

- Decoding uses `DisallowUnknownFields`, so a typo'd key fails at startup instead
  of being silently ignored — the file decides what an agent may do to a build
  server.
- A token file with any group/other permission bits is refused rather than
  warned about; the error tells the operator the exact `chmod` to run.
- Environment variables override the active profile, which is what makes
  container and CI use possible without rewriting the file.
