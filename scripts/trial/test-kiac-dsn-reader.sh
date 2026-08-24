#!/usr/bin/env bash
# CHAOS-4186 round-5 confirmation gap: no suite in this directory exercised
# common.sh's kiac-branch `dsn --env` reader (trial_wire_common_env's
# line-by-line split-on-first-`=` + fixed 13-key `case` allowlist, which
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
fake_dsn_bin() {
  local lines="$1"
  cat >"$tmp/fake-trial-data.sh" <<FAKE
#!/usr/bin/env bash
if [[ "\${1:-}" == "dsn" && "\${2:-}" == "--env" ]]; then
  cat <<'LINES'
$lines
LINES
  exit 0
fi
exit 1
FAKE
  chmod +x "$tmp/fake-trial-data.sh"
}

# run_reader sources common.sh and calls trial_wire_common_env under the
# kiac plane, with ACR_TRIAL_KIAC_DSN_BIN pointed at the fake. Echoes
# "OK <pg_host> <pg_password> <ch_dsn-without-password> <falkor_addr>" on
# success, or "ERR <exit_code>" on failure. The `|| echo "ERR $?"` is the
# same idiom test-connect-retry.sh's run_selftest uses: a bare failing
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
      echo "OK $ACR_TEST_TRIAL_PG_HOST $ACR_TEST_TRIAL_PG_PASSWORD $(sed -E "s#.*@##" <<<"$ACR_TEST_TRIAL_CLICKHOUSE_DSN") $ACR_TEST_TRIAL_FALKOR_ADDR"
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
check "happy path: 13 valid keys resolve pg/ch/falkor" \
  "OK 127.0.0.1 pw 127.0.0.1:30502/default 127.0.0.1:30503" \
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
  "OK 127.0.0.1 $ADVERSARIAL_PW 127.0.0.1:30502/default 127.0.0.1:30503" \
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

# 4. A missing expected key hard-fails (13-line count check catches a
# short producer).
fake_dsn_bin "$(sed '/ACR_TEST_TRIAL_FALKOR_PORT/d' <<<"$VALID_LINES")"
check "missing expected key hard-fails" \
  "ERR 1" \
  "$(run_reader)"

# 5. A duplicate key standing in for a different missing key still
# hard-fails: line count reaches 13, but the truly-missing variable stays
# empty and trips the per-variable non-empty check (round-5 review's own
# question -- verified here, not just asserted).
fake_dsn_bin "$(sed 's/ACR_TEST_TRIAL_FALKOR_PORT=30503/ACR_TEST_TRIAL_PG_HOST=127.0.0.1/' <<<"$VALID_LINES")"
check "duplicate key + one truly-missing key still hard-fails (count==13 alone is not enough)" \
  "ERR 1" \
  "$(run_reader)"


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
    "$(grep -c '^ACR_CONTEXT_FABRIC_FALKOR_TLS=false$' <<<"$producer_output")"
  check "producer emits ACR_CONTEXT_FABRIC_FALKOR_ALLOW_INSECURE=true" \
    "1" \
    "$(grep -c '^ACR_CONTEXT_FABRIC_FALKOR_ALLOW_INSECURE=true$' <<<"$producer_output")"
else
  echo "skip: producer pin (KUBECONFIG not set -- needs a live kiac plane, same requirement test-connect-retry.sh has elsewhere)"
fi

if [[ "$failures" -gt 0 ]]; then
  echo "kiac-dsn-reader checks FAILED ($failures)" >&2
  exit 1
fi
echo "kiac-dsn-reader checks passed"
