#!/usr/bin/env bash
# CHAOS-4313 acceptance: "Red-first: launcher with TRANSPORT=api and no key
# fails closed with a named error before any request is published."
#
# Exercises common.sh's own trial_responder_transport/trial_responder_script/
# trial_require_responder_transport_ready functions DIRECTLY -- the same
# "consumer-level check against the REAL function, never a reimplementation"
# precedent test-connect-retry.sh already sets in this directory -- not a
# live launcher run (no annex, no database, no network, no exchange dir).
#
# Needs ops/.env for real (the SAME requirement every other live trial
# script in this directory has, via common.sh's non-plan-only path): NOT
# part of `make shard-plan`/`make verify`. It never reads the real
# OPENAI_API_KEY value out of it -- trial_secret is locally overridden
# inside a subshell for each case below, so this file never depends on
# (and never proves anything about) whether a real key happens to be
# configured on the machine it runs on.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

failures=0
check() {
  local label="$1" want="$2" got="$3"
  if [[ "$got" != "$want" ]]; then
    echo "FAIL: $label" >&2
    echo "  want: $want" >&2
    echo "  got:  $got" >&2
    failures=$((failures + 1))
  else
    echo "ok: $label"
  fi
}

# 1. RED-FIRST: transport=api, no key -> fails closed BEFORE anything else
# (this call alone touches no exchange dir, no go test, no network) --
# names OPENAI_API_KEY in its error, exits nonzero. `exit 1` inside
# trial_require_responder_transport_ready terminates whichever shell calls
# it directly, so this (and every other exit-prone case below) runs in a
# subshell -- trial_secret's override there is local to that subshell only,
# never leaking into later checks.
status=0
( trial_secret() { printf ''; }
  ACR_TEST_TRIAL_RESPONDER_TRANSPORT=api trial_require_responder_transport_ready ) \
  2>"$tmp/err-no-key.log" || status=$?
check "transport=api, no key: exit status is nonzero" "1" "$([[ "$status" != 0 ]] && echo 1 || echo 0)"
check "transport=api, no key: error names OPENAI_API_KEY" "1" "$(grep -c 'OPENAI_API_KEY' "$tmp/err-no-key.log")"
check "transport=api, no key: error names ACR_TEST_TRIAL_RESPONDER_TRANSPORT=api" "1" "$(grep -c 'ACR_TEST_TRIAL_RESPONDER_TRANSPORT=api' "$tmp/err-no-key.log")"

# 2. GREEN: transport=api, key present -> succeeds (no error, no exit).
status=0
( trial_secret() { printf 'sk-fake-not-a-real-key'; }
  ACR_TEST_TRIAL_RESPONDER_TRANSPORT=api trial_require_responder_transport_ready ) \
  2>"$tmp/err-with-key.log" || status=$?
check "transport=api, key present: exit status is zero" "0" "$status"
check "transport=api, key present: no error output" "0" "$(wc -c <"$tmp/err-with-key.log" | tr -d ' ')"

# 3. Default transport (unset ACR_TEST_TRIAL_RESPONDER_TRANSPORT) resolves
# to "api" -- the CHAOS-4313 cutover: api from this point on, codex retained
# only for replaying historical runs.
check "default (unset) transport resolves to api" "api" "$(unset ACR_TEST_TRIAL_RESPONDER_TRANSPORT; trial_responder_transport)"

# 4. codex transport is still selectable explicitly (historical replay).
check "explicit transport=codex resolves to codex" "codex" "$(ACR_TEST_TRIAL_RESPONDER_TRANSPORT=codex trial_responder_transport)"

# 4b. codex xhigh review round 1 (Low, confirmed): a SET-but-whitespace-only
# value must default to "api" too, matching the Go side's
# strings.TrimSpace(...) == "" -> "api" behavior for the identical env var
# (twoTurnResponderTransport, chaos3742_two_turn_confirmation_test.go) --
# bash's own `${VAR:-default}` only applies to unset/empty, never
# whitespace-only, so this used to disagree with Go and fall through to the
# unrecognized-value refusal instead.
check "whitespace-only transport resolves to api" "api" "$(ACR_TEST_TRIAL_RESPONDER_TRANSPORT="   " trial_responder_transport)"

# 5. An unrecognized transport value is refused, not silently defaulted.
status=0
(ACR_TEST_TRIAL_RESPONDER_TRANSPORT=bogus trial_responder_transport >/dev/null) 2>"$tmp/err-bogus.log" || status=$?
check "unrecognized transport is refused: exit status is nonzero" "1" "$([[ "$status" != 0 ]] && echo 1 || echo 0)"
check "unrecognized transport error names the bad value" "1" "$(grep -c 'bogus' "$tmp/err-bogus.log")"

# 5b. RED-FIRST regression proof (codex xhigh review round 1, confirmed):
# the SAME bogus-transport refusal, but through the EXACT nesting shape
# every real launcher actually uses -- an OUTER command substitution
# capturing trial_responder_script's own output
# (`"$(trial_responder_script)" ... &` in run-two-turn.sh /
# run-two-turn-parallel.sh). A prior version of trial_responder_script
# resolved the transport via `case "$(trial_responder_transport)" in`
# (or even a captured intermediate `local transport;
# transport="$(trial_responder_transport)"` assignment) -- both silently
# swallowed the nested `exit 1` once trial_responder_script was ITSELF
# being captured here, returning an EMPTY string with exit 0 instead of
# refusing. Confirmed failing against that shape before this file's fix
# landed; this case is what would catch a regression back to it.
status=0
out="$(ACR_TEST_TRIAL_RESPONDER_TRANSPORT=bogus trial_responder_script 2>"$tmp/err-nested-bogus.log")" || status=$?
check "nested capture: bogus transport still refused (exit nonzero)" "1" "$([[ "$status" != 0 ]] && echo 1 || echo 0)"
check "nested capture: bogus transport still refused (no path printed)" "0" "$(printf '%s' "$out" | wc -c | tr -d ' ')"
check "nested capture: error still names the bad value" "1" "$(grep -c 'bogus' "$tmp/err-nested-bogus.log")"

# 6. trial_responder_script resolves to the right sibling script per
# transport -- the exact function every launcher backgrounds directly.
check "transport=api resolves to run-responder-api.sh" "1" \
  "$(ACR_TEST_TRIAL_RESPONDER_TRANSPORT=api trial_responder_script | grep -c 'run-responder-api\.sh$')"
check "transport=codex resolves to run-responder-codex.sh" "1" \
  "$(ACR_TEST_TRIAL_RESPONDER_TRANSPORT=codex trial_responder_script | grep -c 'run-responder-codex\.sh$')"

# 7. transport=codex readiness still checks for CODEX_HOME/auth.json
# (unchanged behavior, retained for historical replay) -- point CODEX_HOME
# at an empty temp dir so this never depends on the real machine's login
# state.
status=0
(CODEX_HOME="$tmp/empty-codex-home" ACR_TEST_TRIAL_RESPONDER_TRANSPORT=codex trial_require_responder_transport_ready) \
  2>"$tmp/err-codex-nologin.log" || status=$?
check "transport=codex, no auth.json: exit status is nonzero" "1" "$([[ "$status" != 0 ]] && echo 1 || echo 0)"
check "transport=codex, no auth.json: error names auth.json" "1" "$(grep -c 'auth.json' "$tmp/err-codex-nologin.log")"

# 8. codex xhigh review round 3 (High, confirmed): a SET-but-whitespace-only
# OPENAI_API_KEY must be refused the same as an unset/empty one -- a bare
# `-z` check accepts it (non-empty), then cmd/acr-trial-responder-api's own
# strings.TrimSpace(...)=="" check refuses it later, well after the go test
# has already started publishing requests. Same red-first requirement as
# case 1 above, just for a whitespace-only key instead of a missing one.
status=0
( trial_secret() { printf '   '; }
  ACR_TEST_TRIAL_RESPONDER_TRANSPORT=api trial_require_responder_transport_ready ) \
  2>"$tmp/err-whitespace-key.log" || status=$?
check "transport=api, whitespace-only key: exit status is nonzero" "1" "$([[ "$status" != 0 ]] && echo 1 || echo 0)"
check "transport=api, whitespace-only key: error names OPENAI_API_KEY" "1" "$(grep -c 'OPENAI_API_KEY' "$tmp/err-whitespace-key.log")"

# 9. codex xhigh review round 3 (Medium, confirmed): trial_responder_model
# resolves the SAME concrete default under transport=api that
# run-responder-api.sh's own MODEL variable would otherwise apply silently
# -- an unset ACR_TEST_TRIAL_RESPONDER_MODEL must resolve to "gpt-5.6-luna"
# so the go test's own provenance and the responder that actually answered
# agree, instead of the go test side recording the literal string
# "ambient-default" for a call that was, in fact, answered by a known
# model. transport=codex keeps "ambient-default" (empty here) unchanged --
# run-responder-codex.sh's own default is genuinely unknown to this harness.
# trial_responder_model's own precondition (TRIAL_RESPONDER_TRANSPORT
# already set) is satisfied here by the preceding trial_responder_transport
# call inside the SAME subshell.
check "transport=api, unset model: resolves to gpt-5.6-luna" "gpt-5.6-luna" \
  "$(unset ACR_TEST_TRIAL_RESPONDER_MODEL; ACR_TEST_TRIAL_RESPONDER_TRANSPORT=api trial_responder_transport >/dev/null; trial_responder_model)"
check "transport=codex, unset model: resolves to empty (ambient-default)" "" \
  "$(unset ACR_TEST_TRIAL_RESPONDER_MODEL; ACR_TEST_TRIAL_RESPONDER_TRANSPORT=codex trial_responder_transport >/dev/null; trial_responder_model)"
check "transport=api, explicit model: passes through unchanged" "gpt-5.6-sol" \
  "$(ACR_TEST_TRIAL_RESPONDER_MODEL=gpt-5.6-sol ACR_TEST_TRIAL_RESPONDER_TRANSPORT=api trial_responder_transport >/dev/null; ACR_TEST_TRIAL_RESPONDER_MODEL=gpt-5.6-sol trial_responder_model)"

if [[ "$failures" -gt 0 ]]; then
  echo "responder-transport-readiness checks FAILED ($failures)" >&2
  exit 1
fi
echo "responder-transport-readiness checks passed"
