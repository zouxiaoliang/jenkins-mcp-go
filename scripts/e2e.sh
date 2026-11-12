#!/usr/bin/env bash
# Read-only end-to-end check against a live controller.
#
# This script must never change controller state. It only calls Read Tools, and
# asserts that the write tools are absent from tools/list.
set -euo pipefail

cd "$(dirname "$0")/.."

BIN=dist/jenkins-mcp
CFG=${JENKINS_MCP_E2E_CONFIG:-/tmp/jenkins-mcp-e2e.json}

if [ ! -x "$BIN" ]; then
  echo "build first: just build" >&2
  exit 1
fi
if [ ! -f "$CFG" ]; then
  echo "no config at $CFG" >&2
  exit 1
fi

echo "== config =="
cat "$CFG"

# Drive the stdio transport with a raw JSON-RPC exchange.
echo "== stdio: initialize + tools/list + read-only calls =="
OUT=$(mktemp)
ERR=$(mktemp)
trap 'rm -f "$OUT" "$ERR"' EXIT

{
  printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"e2e","version":"0"}}}'
  printf '%s\n' '{"jsonrpc":"2.0","method":"notifications/initialized"}'
  printf '%s\n' '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}'
  printf '%s\n' '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"jenkins_info","arguments":{}}}'
  printf '%s\n' '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"list_jobs","arguments":{}}}'
  printf '%s\n' '{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"list_queue","arguments":{}}}'
  printf '%s\n' '{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"list_nodes","arguments":{}}}'
  printf '%s\n' '{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"list_builds","arguments":{"job":"ModuleSubmitBuild","limit":3}}}'
  printf '%s\n' '{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"get_build","arguments":{"job":"ModuleSubmitBuild","build":"lastSuccessfulBuild"}}}'
  printf '%s\n' '{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"get_console","arguments":{"job":"ModuleSubmitBuild","build":"lastSuccessfulBuild","lines":5}}}'
  printf '%s\n' '{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{"name":"get_job","arguments":{"job":"ModuleSubmitBuild"}}}'
  printf '%s\n' '{"jsonrpc":"2.0","id":11,"method":"tools/call","params":{"name":"jenkins_get_path","arguments":{"path":"/api/json?tree=mode"}}}'
  sleep 2
} | "$BIN" --transport stdio --config "$CFG" >"$OUT" 2>"$ERR" || true

echo "-- stderr --"
cat "$ERR"

fail=0
check() {
  local desc=$1 pattern=$2 file=$3
  if grep -qF -- "$pattern" "$file"; then
    echo "PASS  $desc"
  else
    echo "FAIL  $desc  (missing: $pattern)"
    fail=1
  fi
}
refute() {
  local desc=$1 pattern=$2 file=$3
  if grep -qF -- "$pattern" "$file"; then
    echo "FAIL  $desc  (unexpected: $pattern)"
    fail=1
  else
    echo "PASS  $desc"
  fi
}

check "initialize returns serverInfo" '"serverInfo"' "$OUT"
check "jenkins_info reports a version" '"version":' "$OUT"
check "jenkins_info states the auth mode" '"auth":"anonymous"' "$OUT"
check "write gate keeps trigger_build out of tools/list" '"name":"jenkins_info"' "$OUT"
refute "trigger_build is not advertised" '"name":"trigger_build"' "$OUT"
refute "stop_build is not advertised" '"name":"stop_build"' "$OUT"
refute "cancel_queue_item is not advertised" '"name":"cancel_queue_item"' "$OUT"
check "list_jobs returned jobs" '"jobs"' "$OUT"
check "list_builds returned a build row" '"state"' "$OUT"
check "get_console returned text" '"text"' "$OUT"
check "get_job exposed parameters" '"parameters"' "$OUT"
check "escape hatch returned a body" '"body"' "$OUT"

# The single strongest assertion: no read tool may fail on a healthy controller.
# Without it, a broken decoder still "passes" because the other greps match
# sibling results.
refute "no read tool reported an error" '"isError":true' "$OUT"

# A finished build must not claim it is still writing, or an agent polls forever.
check "finished build reports more_data=false" '"more_data":false' "$OUT"
check "list_nodes reports executors" '"total_executors"' "$OUT"
check "list_nodes reports nodes" '"nodes"' "$OUT"

echo
if [ "$fail" -ne 0 ]; then
  echo "E2E FAILED"
  exit 1
fi
echo "E2E PASSED (read-only; no build was triggered)"
