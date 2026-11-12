# Jenkins MCP Bridge

A Model Context Protocol server that exposes a Jenkins controller to an agent.
This glossary fixes the language used for the Jenkins objects the bridge talks
about, so that tool names, parameters and JSON fields all say the same thing.

## Language

### Jenkins objects

**Controller**:
The Jenkins server the bridge connects to; it owns jobs and schedules builds.
_Avoid_: master, server, instance

**Job**:
A named, addressable unit of work on the controller (here: a Pipeline job).
Identified by its **full name**, which includes any folder prefix.
_Avoid_: project, task, pipeline (a pipeline is the job's *definition*, not the job)

**Full Name**:
The canonical, slash-separated identifier of a job including its folders
(`folder/sub/name`). Distinct from the display name, which may collide.
_Avoid_: path, job path

**Build**:
One execution of a Job, identified by a build number unique within that job.
_Avoid_: run, execution, instance

**Build Reference**:
A way of naming a build without knowing its number: `lastBuild`,
`lastSuccessfulBuild`, `lastFailedBuild`, `lastCompletedBuild`, or an explicit
number.
_Avoid_: alias, shortcut

**Build State**:
The pair (result, in-progress) describing where a build stands: queued, running,
or finished with `SUCCESS` / `FAILURE` / `UNSTABLE` / `ABORTED`.
_Avoid_: status, phase

**Queue Item**:
The controller's placeholder for a build that has been requested but has not yet
been assigned an executor. It has its own id and can be cancelled before it starts.
_Avoid_: pending build, waiting build

**Executor**:
A slot on the controller or an agent that can actually run a build. A job with no
executor available leaves its builds in the queue.
_Avoid_: worker, runner

**Parameter Definition**:
A job's declaration of an input it accepts: name, type, default, and (for choice
types) allowed values. Parameters exist per job and are not known in advance.
_Avoid_: argument spec, input

**Parameter**:
A concrete value supplied for a Parameter Definition when requesting a build.
_Avoid_: argument, variable, env

**Console Log**:
The append-only text a build writes while running; it grows until the build ends.
_Avoid_: output, stdout, log file

**Byte Cursor**:
A position in the Console Log measured in bytes from the start, paired with the
controller's "more data" flag. Because logs may be UTF-8, a cursor can land in
the middle of a character.
_Avoid_: line number, offset, read position

**Crumb**:
The controller's CSRF token required on state-changing requests. It is bound to
the session cookie that issued it, so a crumb cannot be reused across sessions.
_Avoid_: token (that word is reserved for the API token), csrf key

### Bridge objects

**Profile**:
A named, self-contained set of connection settings (controller URL, credentials,
and capability switches) that the bridge connects with.
_Avoid_: config, environment, target

**API Token**:
The credential used to authenticate to the controller, presented with a username
via HTTP basic auth. Distinct from the Crumb.
_Avoid_: password, secret, key

**Read Tool**:
A tool that only observes the controller and never changes its state.
_Avoid_: safe tool, GET tool

**Write Tool**:
A tool that changes controller state (queueing, cancelling). Disabled unless the
profile explicitly enables writes.
_Avoid_: action tool, mutating tool

**Truncation Envelope**:
The wrapper a tool returns when a response body is too large to inline: the
returned slice, the total size, whether it was truncated, and where the full body
was written.
_Avoid_: paging, limit struct

**Write Gate**:
The registration-time switch that decides whether Write Tools are advertised at
all. Closed by default; opening it requires both credentials and an explicit
opt-in.
_Avoid_: permission check, auth flag

**Anonymous Mode**:
Running with no API Token, so every request carries the controller's anonymous
identity. Always reported, never implied.
_Avoid_: unauthenticated, public mode, guest

**Escape Hatch**:
The single GET-only tool that reaches arbitrary controller paths for plugin
surfaces no fixed tool anticipates.
_Avoid_: passthrough, proxy tool, raw request
