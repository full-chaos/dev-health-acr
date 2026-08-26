#!/usr/bin/env bash
# Usage: run-responder-api.sh <exchange-dir> [poll-seconds]
#
# CHAOS-4313: OpenAI-API-backed sibling of run-responder-codex.sh. Answers
# the SAME file-exchange envelope contract (file_exchange_runtime_test.go's
# own ENVELOPE CONTRACT -- session_nonce/system/prompt/output_schema/
# instructions in, {"session_nonce","output"} out) via
# cmd/acr-trial-responder-api, a small Go program that calls the OpenAI
# Chat Completions API directly with structured JSON-schema output and
# 429/5xx backoff, instead of a `codex exec` subscription subprocess.
#
# Ruling (chris, 2026-08-26 05:30 PDT): all trial responder runs move to
# the OpenAI API -- trial volume is decreasing, and codex-exec's CPU load
# on the host now costs more than API spend. This supersedes the
# "harnesses not API keys" standing rule for the measurement responder
# ONLY -- codex REVIEWS are unchanged, and run-responder-codex.sh is
# retained for replaying historical runs
# (ACR_TEST_TRIAL_RESPONDER_TRANSPORT=codex).
#
# Key hygiene (non-negotiable): OPENAI_API_KEY is resolved from
# trial_secret OPENAI_API_KEY (ops/.env, the SAME source
# internal/contextfabric/embedprovider already uses for live embeddings)
# and passed to the Go binary as a PROCESS ENVIRONMENT VARIABLE ONLY --
# never as an argv element (ps/procfs-visible), never echoed, never
# logged. This script never enables xtrace (`set -x`/`bash -x`) and never
# prints the key in any diagnostic message. See cmd/acr-trial-responder-api
# for the same fail-closed check repeated at the Go binary's own entry
# point (defense in depth, not tribal knowledge).
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

EXDIR="${1:?exchange dir required}"
POLL="${2:-2}"

mkdir -p "$EXDIR/requests" "$EXDIR/responses" "$EXDIR/_responder_logs"

MODEL="${ACR_TEST_TRIAL_RESPONDER_MODEL:-gpt-5.6-luna}"
# CHAOS-4313 follow-up: no substituted default here, unlike MODEL above --
# an unset value stays empty, and cmd/acr-trial-responder-api's own main()
# leaves ReasoningEffort unset on the request when it sees an empty string
# (see that file's own doc comment).
EFFORT="${ACR_TEST_TRIAL_RESPONDER_EFFORT:-}"

# codex xhigh review round 1 (High, confirmed): this script's OWN
# `set -euo pipefail` never enables xtrace, but a CALLER that invokes it
# under an inherited `bash -x` (SHELLOPTS carrying `xtrace`, or a wrapping
# `bash -x run-two-turn.sh`) would have bash echo the expanded
# `OPENAI_API_KEY="$api_key" ... go run ...` command line -- KEY VALUE
# INCLUDED -- to stderr before running it, and run-two-turn.sh/
# run-two-turn-parallel.sh both redirect this whole script's stderr into
# `$exdir/_responder_driver.log`. Same incident class as the 2026-08-25
# 19:58 PDT standing rule (scripts/trial/common.sh's own dsn --env reader).
# The primary defense is the standing rule itself (never `bash -x` a
# script sourcing common.sh); this is defense in depth, scoped to exactly
# the two lines that ever touch the key, restoring xtrace afterward only
# if it was ALREADY on (never turning it on where it wasn't).
_responder_api_xtrace_was_on=0
case "$-" in *x*) _responder_api_xtrace_was_on=1 ;; esac
{ set +x; } 2>/dev/null

api_key="$(trial_secret OPENAI_API_KEY)"
# codex xhigh review round 3 (High, confirmed): trim, don't just check -z --
# a whitespace-only value passes -z (it is non-empty) but the Go binary's
# own defense-in-depth check (strings.TrimSpace, main.go) would still
# refuse it; this script is meant to be correct standalone too (same
# discipline the primary check in trial_require_responder_transport_ready,
# common.sh, now applies).
api_key="${api_key#"${api_key%%[![:space:]]*}"}"
api_key="${api_key%"${api_key##*[![:space:]]}"}"
if [[ -z "$api_key" ]]; then
  echo "run-responder-api.sh: OPENAI_API_KEY not found (or whitespace-only) in $dev_health_root/ops/.env -- cannot answer via the OpenAI API (see this script's own key-hygiene header comment)" >&2
  exit 1
fi

# OPENAI_API_KEY is exported ONLY into this `go run` child's own
# environment, as a command-scoped prefix assignment on the same line --
# never a separate `export` (which would leak into every OTHER command
# this script, or a caller sourcing it, runs afterward) and never an argv
# element (go run's own args are just $EXDIR/$POLL; the key reaches the
# child via envp, invisible to `ps`/`/proc/*/cmdline`).
( cd "$repo_root" && OPENAI_API_KEY="$api_key" ACR_TEST_TRIAL_RESPONDER_MODEL="$MODEL" ACR_TEST_TRIAL_RESPONDER_EFFORT="$EFFORT" go run ./cmd/acr-trial-responder-api "$EXDIR" "$POLL" )
_responder_api_exit=$?
[[ "$_responder_api_xtrace_was_on" -eq 1 ]] && set -x
exit "$_responder_api_exit"
