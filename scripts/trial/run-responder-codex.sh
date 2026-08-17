#!/usr/bin/env bash
# Usage: run-responder-codex.sh <exchange-dir> [poll-seconds]
#
# CHAOS-3884 arm-5 out-of-process responder: watches <exchange-dir>/requests
# for new file-exchange request files (file_exchange_runtime_test.go's own
# ENVELOPE CONTRACT -- session_nonce/system/prompt/output_schema/instructions
# in, {"session_nonce","output"} out) and answers each with a ONE-SHOT `codex
# exec` subprocess running on the operator's ChatGPT SUBSCRIPTION auth --
# NEVER a metered OPENAI_API_KEY (standing rule; this script never reads or
# exports that variable). Exits once <exchange-dir>/DONE exists and every
# published request has a matching response file.
#
# Hygiene (non-negotiable, per the epic's standing operational rules):
#   - ONE fresh, PRIVATE CODEX_HOME for this whole batch -- not the operator's
#     real ~/.codex (a large, shared, live-session directory: history,
#     attachments, hooks, other MCP servers). Only auth.json is copied in, so
#     this batch authenticates via the SAME subscription login without
#     touching any of that shared state. Wiped fully on exit via a trap, so a
#     Ctrl-C or an early failure still cleans it up -- never left behind.
#   - Every `codex exec` invocation's own stdout/stderr goes to a per-request
#     log file under <exchange-dir>/_responder_logs/, NEVER to this script's
#     own stdout. The exchange envelope's `system`/`prompt` fields carry real
#     corpus text -- echoing that here would leak it into whatever
#     terminal/agent transcript launched this script. Diagnosing a failure
#     must go through the trial harness's own closed-vocabulary failure-class
#     reporting (caseOutcome.ErrorClass/Stage), never by catting these logs.
#   - `codex exec` is single-shot per request -- no `--resume`, no persistent
#     app-server broker -- so there is no broker to kill beyond this script's
#     own process tree, which the trap also covers.
#   - Sequential, not concurrent: at most one `codex exec` runs at a time,
#     sharing one CODEX_HOME. The trial harness itself makes at most one
#     in-flight exchange call per case, so this never becomes a throughput
#     bottleneck, and it avoids two processes touching the same CODEX_HOME
#     session/state files at once.
set -euo pipefail

EXDIR="${1:?exchange dir required}"
POLL="${2:-2}"

REQ_DIR="$EXDIR/requests"
RESP_DIR="$EXDIR/responses"
LOG_DIR="$EXDIR/_responder_logs"
mkdir -p "$REQ_DIR" "$RESP_DIR" "$LOG_DIR"

real_codex_home="${CODEX_HOME:-$HOME/.codex}"
if [[ ! -f "$real_codex_home/auth.json" ]]; then
  echo "run-responder-codex.sh: $real_codex_home/auth.json not found -- log in with 'codex login' (ChatGPT subscription) first" >&2
  exit 1
fi

codex_home_batch="$(mktemp -d "${TMPDIR:-/tmp}/codex-responder-chaos3884.XXXXXX")"
cp "$real_codex_home/auth.json" "$codex_home_batch/auth.json"

cleanup() {
  rm -rf "$codex_home_batch"
}
trap cleanup EXIT INT TERM

echo "responder: watching $EXDIR (private CODEX_HOME=$codex_home_batch, poll=${POLL}s) -- subscription auth only, never a metered API key"

answer_one() {
  local req="$1" base resp prompt
  base="$(basename "$req")"
  resp="$RESP_DIR/$base"
  [[ -f "$resp" ]] && return 0
  prompt="Read the JSON file at $req. It has fields: operation, seq, session_nonce, system, prompt, output_schema, instructions. Follow the request's own \"instructions\" field exactly: treat \"system\" as the system role and \"prompt\" as the user payload, produce exactly one JSON object satisfying \"output_schema\" (every required field present, enum values exactly as listed, no extra top-level fields beyond what the schema allows). Base the answer ONLY on \"system\" and \"prompt\" -- never invent facts not present in them. Then write a JSON file to $resp whose top level is exactly {\"session_nonce\": <the request's session_nonce, copied verbatim>, \"output\": <your JSON object>}. Write the file yourself with your own file-write tool at that exact path. Do not print the JSON to the terminal. Do not modify any other file. This is a single self-contained task; stop once the response file is written."
  CODEX_HOME="$codex_home_batch" codex exec \
    --ephemeral \
    --skip-git-repo-check \
    -s workspace-write \
    -C "$EXDIR" \
    --add-dir "$EXDIR" \
    -c 'sandbox_workspace_write.network_access=false' \
    "$prompt" \
    </dev/null >>"$LOG_DIR/$base.log" 2>&1 || true
}

while true; do
  pending=0
  shopt -s nullglob
  for req in "$REQ_DIR"/*.json; do
    base="$(basename "$req")"
    [[ "$base" == .* ]] && continue # writer's in-flight temp file, not yet published
    if [[ ! -f "$RESP_DIR/$base" ]]; then
      pending=1
      answer_one "$req"
    fi
  done
  shopt -u nullglob
  if [[ -f "$EXDIR/DONE" && "$pending" -eq 0 ]]; then
    break
  fi
  sleep "$POLL"
done

echo "responder: DONE, every published request answered -- wiping private CODEX_HOME"
