# Degrade to anonymous read-only mode when no credentials are configured

Credentials are optional. With an API token the bridge authenticates with HTTP
basic auth and may enable writes; without one it runs anonymously, keeps the read
tools, and reports `auth: anonymous` from `jenkins_info`.

The alternative — refuse to start without a token — was rejected because
anonymous read access is common and useful on real controllers (the reference
controller allows `api/json`, `queue`, `computer` and build logs anonymously but
returns 403 for `/build` and `config.xml`). Failing to start would make the bridge
useless exactly where it is cheapest to use.

Consequences:

- The bridge must state its privilege level instead of letting callers discover it
  through 403s. `jenkins_info` carries `auth`, and read-tool errors distinguish
  "not found" from "not permitted" so an agent never reads a permission failure
  as an absence of data.
- Write tool registration depends on credentials *and* the explicit `allowWrite`
  switch; having a token alone never enables writes.
