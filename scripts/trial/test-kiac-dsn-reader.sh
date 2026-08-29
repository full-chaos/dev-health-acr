#!/usr/bin/env bash
# CHAOS-4186 round-5 confirmation gap: no suite in this directory exercised
# common.sh's kiac-branch `dsn --env` reader (trial_wire_common_env's
# line-by-line split-on-first-`=` + fixed 15-key `case` allowlist, which
# replaced an earlier eval-based design after two review rounds found real
# injection bugs in it). test-connect-retry.sh pins ACR_TRIAL_DATA_PLANE=
# compose for its own unrelated reasons and never reaches this branch;
# test-shard-plan.sh's plan-only mode stubs trial_wire_common_env entirely;
# test-repo-root-guard.sh only tests the sourcing guard. This file is the
# missing direct coverage.
#
# Against the REAL trial_wire_common_env, via ACR_TRIAL_KIAC_DSN_BIN (a
# testability hook added alongside this file) pointing at a fake
# "trial-data.sh" that prints fabricated `dsn --env` lines instead of
# talking to a live kiac cluster -- never a reimplementation of the
# parser's own logic.
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
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

# fake_dsn_bin writes a "trial-data.sh" stand-in at "$tmp/fake-trial-data.sh"
# whose `dsn --env` subcommand prints exactly $1 (the fabricated line
# output) to stdout and exits 0; any other invocation exits 1.
#
# CHAOS-4302: no heredoc anywhere in this generator, at write time OR at
# the generated script's own runtime. $lines is written to a companion
# data file via `printf` (a plain file write, no pipe), and the generated
# script's payload line is just `cat` of that file -- never a nested
# `cat <<'LINES'` inside the written script, which would itself be a small
# heredoc a future run of the generated script could deadlock on.
fake_dsn_bin() {
  local lines="$1"
  printf '%s\n' "$lines" >"$tmp/fake-lines.txt"
  {
    printf '%s\n' '#!/usr/bin/env bash'
    printf '%s\n' 'if [[ "${1:-}" == "dsn" && "${2:-}" == "--env" ]]; then'
    printf '  %s\n' "cat \"$tmp/fake-lines.txt\""
    printf '  %s\n' 'exit 0'
    printf '%s\n' 'fi'
    printf '%s\n' 'exit 1'
  } >"$tmp/fake-trial-data.sh"
  chmod +x "$tmp/fake-trial-data.sh"
}

# run_reader sources common.sh and calls trial_wire_common_env under the
# kiac plane, with ACR_TRIAL_KIAC_DSN_BIN pointed at the fake. Echoes
# "OK <pg_host> <pg_password> <ch_dsn-without-password> <falkor_addr>
# <falkor_tls> <falkor_allow_insecure>" on success, or "ERR <exit_code>"
# on failure. The last two fields exist so the happy-path check below
# actually asserts the exported ACR_CONTEXT_FABRIC_FALKOR_TLS/
# ALLOW_INSECURE values -- removing their export in common.sh must fail
# this test, not just fail to error. The `|| echo "ERR $?"` is the same
# idiom test-connect-retry.sh's run_selftest uses: a bare failing
# command inside a `$(...)` assignment would otherwise trip this script's
# own `set -e` before the FAIL/ok reporting below ever runs.
run_reader() {
  (cd "$script_dir/../.." && env \
    ACR_TRIAL_DATA_PLANE=kiac \
    ACR_TRIAL_KIAC_DSN_BIN="$tmp/fake-trial-data.sh" \
    bash -c '
      set -euo pipefail
      source scripts/trial/common.sh
      trial_wire_common_env
      echo "OK $ACR_TEST_TRIAL_PG_HOST $ACR_TEST_TRIAL_PG_PASSWORD $(printf "%s" "$ACR_TEST_TRIAL_CLICKHOUSE_DSN" | sed -E "s#.*@##") $ACR_TEST_TRIAL_FALKOR_ADDR ${ACR_CONTEXT_FABRIC_FALKOR_TLS:-<unset>} ${ACR_CONTEXT_FABRIC_FALKOR_ALLOW_INSECURE:-<unset>}"
    ' 2>"$tmp/stderr.log") || echo "ERR $?"
}

VALID_LINES='ACR_TEST_TRIAL_PG_HOST=127.0.0.1
ACR_TEST_TRIAL_PG_PORT=30500
ACR_TEST_TRIAL_PG_USER=devhealth
ACR_TEST_TRIAL_PG_PASSWORD=pw
ACR_TEST_TRIAL_PG_DB=acr
ACR_TEST_TRIAL_CH_HOST=127.0.0.1
ACR_TEST_TRIAL_CH_PORT=30502
ACR_TEST_TRIAL_CH_HTTP_PORT=30501
ACR_TEST_TRIAL_CH_USER=ch
ACR_TEST_TRIAL_CH_PASSWORD=pw
ACR_TEST_TRIAL_CH_DB=default
ACR_TEST_TRIAL_FALKOR_HOST=127.0.0.1
ACR_TEST_TRIAL_FALKOR_PORT=30503
ACR_CONTEXT_FABRIC_FALKOR_TLS=false
ACR_CONTEXT_FABRIC_FALKOR_ALLOW_INSECURE=true'

# 1. Happy path: all 15 expected keys, correctly assigned and exported to
# the calling shell (not just visible inside trial_wire_common_env).
fake_dsn_bin "$VALID_LINES"
check "happy path: 15 valid keys resolve pg/ch/falkor/falkor-tls" \
  "OK 127.0.0.1 pw 127.0.0.1:30502/default 127.0.0.1:30503 false true" \
  "$(run_reader)"

# 2. A value containing shell metacharacters ($, =, spaces) survives
# verbatim -- never eval'd, never rewritten. Round-3 review's own
# adversarial case: a password containing the literal text
# "ACR_TEST_TRIAL_" must not be corrupted by any prefix rewrite either
# (there is no rewrite left in this design, but a regression that
# reintroduced one would show up here). The injected file target lives
# under $tmp, never the repo, so even a real regression can't leave stray
# files in the working tree.
ADVERSARIAL_PW="has \$(touch $tmp/INJECTED) =and=equals and ACR_TEST_TRIAL_ inside it"
fake_dsn_bin "${VALID_LINES/ACR_TEST_TRIAL_PG_PASSWORD=pw/ACR_TEST_TRIAL_PG_PASSWORD=$ADVERSARIAL_PW}"
check "adversarial value survives verbatim, no injection, no corruption" \
  "OK 127.0.0.1 $ADVERSARIAL_PW 127.0.0.1:30502/default 127.0.0.1:30503 false true" \
  "$(run_reader)"
check "adversarial value never executed (no INJECTED file created)" \
  "1" \
  "$([[ ! -f "$tmp/INJECTED" ]] && echo 1 || echo 0)"

# 3. An unrecognized key (including one carrying an injection payload)
# must hard-fail before ever reaching an assignment -- the allowlist's
# whole point.
fake_dsn_bin "$VALID_LINES
INJECTED_MALICIOUS_KEY=\$(touch $tmp/PWNED)"
check "unrecognized key hard-fails" \
  "ERR 1" \
  "$(run_reader)"
check "unrecognized key's payload never executed" \
  "1" \
  "$([[ ! -f "$tmp/PWNED" ]] && echo 1 || echo 0)"

# 4. A missing expected key hard-fails (15-line count check catches a
# short producer).
fake_dsn_bin "$(printf '%s\n' "$VALID_LINES" | sed '/ACR_TEST_TRIAL_FALKOR_PORT/d')"
check "missing expected key hard-fails" \
  "ERR 1" \
  "$(run_reader)"

# 5. A duplicate key standing in for a different missing key still
# hard-fails: line count reaches 15, but the truly-missing variable stays
# empty and trips the per-variable non-empty check (round-5 review's own
# question -- verified here, not just asserted).
fake_dsn_bin "$(printf '%s\n' "$VALID_LINES" | sed 's/ACR_TEST_TRIAL_FALKOR_PORT=30503/ACR_TEST_TRIAL_PG_HOST=127.0.0.1/')"
check "duplicate key + one truly-missing key still hard-fails (count==15 alone is not enough)" \
  "ERR 1" \
  "$(run_reader)"

# 5b. CHAOS-4228: an IPv6 ACR_TEST_TRIAL_CH_HOST must come out of
# trial_wire_common_env's kiac branch bracketed inside
# ACR_TEST_TRIAL_CLICKHOUSE_DSN (bracket_host_if_ipv6 in common.sh) --
# unbracketed, the DSN's own trailing `:<port>` would be indistinguishable
# from one more `:`-separated group of the address.
fake_dsn_bin "${VALID_LINES/ACR_TEST_TRIAL_CH_HOST=127.0.0.1/ACR_TEST_TRIAL_CH_HOST=2001:db8::1}"
check "an IPv6 ACR_TEST_TRIAL_CH_HOST is bracketed in ACR_TEST_TRIAL_CLICKHOUSE_DSN" \
  "OK 127.0.0.1 pw [2001:db8::1]:30502/default 127.0.0.1:30503 false true" \
  "$(run_reader)"

# 5c. CHAOS-4228 (codex R1, real High): an IPv6 ACR_TEST_TRIAL_FALKOR_HOST
# must also come out bracketed in ACR_TEST_TRIAL_FALKOR_ADDR --
# falkorgraph.Config.validate() parses this with Go's net.SplitHostPort,
# which hard-rejects an unbracketed IPv6 host:port (not just an ambiguous
# string: a real startup failure for the FalkorDB client).
fake_dsn_bin "${VALID_LINES/ACR_TEST_TRIAL_FALKOR_HOST=127.0.0.1/ACR_TEST_TRIAL_FALKOR_HOST=2001:db8::1}"
check "an IPv6 ACR_TEST_TRIAL_FALKOR_HOST is bracketed in ACR_TEST_TRIAL_FALKOR_ADDR" \
  "OK 127.0.0.1 pw 127.0.0.1:30502/default [2001:db8::1]:30503 false true" \
  "$(run_reader)"

# 5d. CHAOS-4228 (codex R1, coverage gap): an IPv6 ACR_TEST_TRIAL_PG_HOST
# must also come out bracketed in ACR_TEST_TRIAL_POSTGRES_DSN
# (common.sh's own postgres:// DSN, the other site this ticket's fix
# touches besides the ClickHouse DSN above). A dedicated small reader
# (not run_reader(), which every OTHER check above shares) keeps this
# addition from changing any existing check's expected string.
run_pg_dsn_reader() {
  (cd "$script_dir/../.." && env \
    ACR_TRIAL_DATA_PLANE=kiac \
    ACR_TRIAL_KIAC_DSN_BIN="$tmp/fake-trial-data.sh" \
    bash -c '
      set -euo pipefail
      source scripts/trial/common.sh
      trial_wire_common_env
      printf "%s" "$ACR_TEST_TRIAL_POSTGRES_DSN" | sed -E "s#.*@##; s#/.*##"
    ' 2>"$tmp/stderr.log") || echo "ERR $?"
}
fake_dsn_bin "${VALID_LINES/ACR_TEST_TRIAL_PG_HOST=127.0.0.1/ACR_TEST_TRIAL_PG_HOST=2001:db8::1}"
check "an IPv6 ACR_TEST_TRIAL_PG_HOST is bracketed in ACR_TEST_TRIAL_POSTGRES_DSN" \
  "[2001:db8::1]:30500" \
  "$(run_pg_dsn_reader)"

# 5e. CHAOS-4228 (codex R2, real completeness gap): the SIX-VAR ESCAPE
# HATCH (override branch, common.sh:239-254) is a DISTINCT code path from
# the kiac branch every check above exercises -- its own ch_dsn/
# falkor_addr construction needs its own proof, not an inference from the
# kiac branch sharing the same bracket_host_if_ipv6 helper. Uses the REAL
# trial_wire_common_env and REAL trial_secret (the same ops/.env every
# other live check in this directory already depends on --
# test-connect-retry.sh's own six-var-override case proves it is
# available here); only the CH_DB portion of the DSN is unpredictable
# (a real ops/.env secret), so the check strips it, asserting only the
# authority (host:port) every fix touches. THREE DISTINCT IPv6 literals
# for PG vs CH vs FALKOR so no assertion could pass on another one's
# value (codex R3, real Medium: the first version of this case left
# ACR_TRIAL_PG_HOST at 127.0.0.1, so a regression at common.sh:451's own
# override-branch pg_host construction could pass this file unnoticed --
# fixed by asserting all three legs).
run_override_reader() {
  (cd "$script_dir/../.." && env \
    ACR_TRIAL_PG_HOST=2001:db7::1 ACR_TRIAL_PG_PORT=5432 \
    ACR_TRIAL_CH_HOST=2001:db8::1 ACR_TRIAL_CH_PORT=9000 \
    ACR_TRIAL_FALKOR_HOST=2001:db9::1 ACR_TRIAL_FALKOR_PORT=6379 \
    bash -c '
      set -euo pipefail
      source scripts/trial/common.sh
      trial_wire_common_env
      pg_authority="$(printf "%s" "$ACR_TEST_TRIAL_POSTGRES_DSN" | sed -E "s#.*@##; s#/.*##")"
      ch_authority="$(printf "%s" "$ACR_TEST_TRIAL_CLICKHOUSE_DSN" | sed -E "s#.*@##; s#/.*##")"
      printf "%s %s %s" "$pg_authority" "$ch_authority" "$ACR_TEST_TRIAL_FALKOR_ADDR"
    ' 2>"$tmp/stderr.log") || echo "ERR $?"
}
check "override branch: IPv6 ACR_TRIAL_PG_HOST/CH_HOST/FALKOR_HOST all bracketed" \
  "[2001:db7::1]:5432 [2001:db8::1]:9000 [2001:db9::1]:6379" \
  "$(run_override_reader)"

# 5b. CHAOS-4525: ACR_TRIAL_PG_DATABASE selects the database inside the
# resolved instance. Two checks, because the default is the half that
# matters most -- an unset knob must leave every existing recipe on the
# standing `acr` database, byte-identical to the pre-4525 behavior.
run_pg_database_reader() {
  local db_env=("$@")
  (cd "$script_dir/../.." && env "${db_env[@]}" \
    ACR_TRIAL_DATA_PLANE=override \
    ACR_TRIAL_PG_HOST=127.0.0.1 ACR_TRIAL_PG_PORT=5432 \
    ACR_TRIAL_CH_HOST=127.0.0.1 ACR_TRIAL_CH_PORT=9000 \
    ACR_TRIAL_FALKOR_HOST=127.0.0.1 ACR_TRIAL_FALKOR_PORT=6379 \
    bash -c '
      set -euo pipefail
      source scripts/trial/common.sh
      trial_wire_common_env
      printf "%s" "$ACR_TEST_TRIAL_POSTGRES_DSN" | sed -E "s#.*@[^/]*/##; s#\?.*##"
    ' 2>/dev/null) || echo "ERR $?"
}
check "pg database defaults to the standing acr database when the knob is unset" \
  "acr" \
  "$(run_pg_database_reader ACR_TRIAL_PG_DATABASE=)"
check "ACR_TRIAL_PG_DATABASE selects an isolated database on the same instance" \
  "acr_trial_seed_probe" \
  "$(run_pg_database_reader ACR_TRIAL_PG_DATABASE=acr_trial_seed_probe)"

# 5c. CHAOS-4525 (codex review P2, PR #330): an isolated database must not be
# allowed to run against the LEGACY EPOCH-0 graph in silence.
#
# ACR_TRIAL_PG_DATABASE's advertised use is "a freshly created and migrated
# database", and a freshly migrated database has an EMPTY
# acr.context_fabric_graph_lifecycle. The epoch resolver then finds no serving
# epoch and the run reads the bare epoch-0 graph key -- which exists and holds
# stale data, so the run COMPLETES and produces a plausible artifact measured
# against the wrong graph. That is the CHAOS-4100 rerun-#2 incident, and it is
# exactly the class of failure that must be loud.
fake_psql() {
  local rows="$1"
  {
    printf '%s\n' '#!/usr/bin/env bash'
    if [[ "$rows" == "ERROR" ]]; then
      printf '%s\n' 'exit 1'
    else
      printf '%s\n' "echo '$rows'"
    fi
  } >"$tmp/fake-psql"
  chmod +x "$tmp/fake-psql"
}

run_lifecycle_guard() {
  local db="$1"
  (cd "$script_dir/../.." && env \
    ACR_TRIAL_DATA_PLANE=override \
    ACR_TRIAL_PG_HOST=127.0.0.1 ACR_TRIAL_PG_PORT=5432 \
    ACR_TRIAL_CH_HOST=127.0.0.1 ACR_TRIAL_CH_PORT=9000 \
    ACR_TRIAL_FALKOR_HOST=127.0.0.1 ACR_TRIAL_FALKOR_PORT=6379 \
    ACR_TRIAL_PG_DATABASE="$db" ACR_TRIAL_PSQL_BIN="$tmp/fake-psql" \
    bash -c '
      set -euo pipefail
      source scripts/trial/common.sh
      trial_wire_common_env
      trial_wire_graph_lifecycle_env
      echo ALLOWED
    ' 2>/dev/null) || echo "REFUSED"
}

fake_psql 0
check "an isolated database with NO lifecycle row is refused" \
  "REFUSED" "$(run_lifecycle_guard acr_seeds_probe)"

fake_psql 1
check "an isolated database WITH a lifecycle row is allowed" \
  "ALLOWED" "$(run_lifecycle_guard acr_seeds_probe | tail -1)"

fake_psql ERROR
check "an unreadable lifecycle table is refused, never assumed empty or fine" \
  "REFUSED" "$(run_lifecycle_guard acr_seeds_probe)"

fake_psql 0
check "the standing acr database is exempt (no psql dependency for existing recipes)" \
  "ALLOWED" "$(run_lifecycle_guard acr | tail -1)"

# 6. Producer pin (CHAOS-4186 follow-up, real incident): the REAL
# trial-data.sh (not the fake used above) must emit
# ACR_CONTEXT_FABRIC_FALKOR_TLS=false and
# ACR_CONTEXT_FABRIC_FALKOR_ALLOW_INSECURE=true against the live kiac
# plane -- both are static (the trial FalkorDB is always plaintext), so
# this is the one check in this file with a live-cluster requirement
# (same as test-connect-retry.sh's own live dependency elsewhere in
# this directory), guarded rather than silently skipped: KUBECONFIG
# must already point at a running acr-trial-data plane.
if [[ -n "${KUBECONFIG:-}" && -f "${KUBECONFIG:-/nonexistent}" ]]; then
  producer_output="$(cd "$script_dir/../.." && deploy/local/trial-data.sh dsn --env 2>/dev/null || true)"
  check "producer emits ACR_CONTEXT_FABRIC_FALKOR_TLS=false" \
    "1" \
    "$(printf '%s\n' "$producer_output" | grep -c '^ACR_CONTEXT_FABRIC_FALKOR_TLS=false$')"
  check "producer emits ACR_CONTEXT_FABRIC_FALKOR_ALLOW_INSECURE=true" \
    "1" \
    "$(printf '%s\n' "$producer_output" | grep -c '^ACR_CONTEXT_FABRIC_FALKOR_ALLOW_INSECURE=true$')"
else
  echo "skip: producer pin (KUBECONFIG not set -- needs a live kiac plane, same requirement test-connect-retry.sh has elsewhere)"
fi

if [[ "$failures" -gt 0 ]]; then
  echo "kiac-dsn-reader checks FAILED ($failures)" >&2
  exit 1
fi
echo "kiac-dsn-reader checks passed"
