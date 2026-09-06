#!/usr/bin/env bash
set -euo pipefail

# Every assertion below reports what it expected and what it saw. They used to
# be bare `exit 1`s, which is invisible in CI: a real failure on main
# (Release run 33997012296, 2026-09-05) produced 31 seconds of silence and then
# `Process completed with exit code 1`, with no way to tell from the log which
# of ~20 assertions had fired. Worse, the last line the log DID carry was
# "privacy verifier failed" -- a negative control passing -- so the log
# actively pointed at the wrong place.
fail() {
  printf 'test-canonical-receipts: %s\n' "$1" >&2
  exit 1
}

root="$(cd "$(dirname "$0")/../.." && pwd -P)"
evidence="$(mktemp -d)"
export ACR_E2E_EVIDENCE_DIR="$evidence"
task8="$evidence/context-fabric-08-no-upload.json"
task9="$evidence/context-fabric-09-mixed-mcp.json"
live="$evidence/context-fabric-09-live-mcp.json"
if ! git -C "$root" diff --quiet; then
  printf 'test requires a clean source worktree\n' >&2
  exit 1
fi
if ! git -C "$root" diff --cached --quiet; then
  printf 'test requires a clean source worktree\n' >&2
  exit 1
fi
if [[ -n "$(git -C "$root" status --porcelain)" ]]; then
  printf 'test requires no untracked source files\n' >&2
  exit 1
fi
backup="$(mktemp -d)"
restored=0
restore_receipts() {
  [[ "$restored" == 0 ]] || return
  restored=1
  for name in context-fabric-08-no-upload.json context-fabric-09-mixed-mcp.json context-fabric-09-live-mcp.json; do
    if [[ -e "$backup/$name" ]]; then cp "$backup/$name" "$evidence/$name"; else rm -f "$evidence/$name"; fi
  done
  rm -rf "$backup" "$evidence"
}
trap restore_receipts EXIT INT TERM
for name in context-fabric-08-no-upload.json context-fabric-09-mixed-mcp.json context-fabric-09-live-mcp.json; do
  [[ ! -e "$evidence/$name" ]] || cp "$evidence/$name" "$backup/$name"
done

printf '{"sentinel":"task8"}\n' > "$task8"
task8_before="$(shasum -a 256 "$task8" | awk '{print $1}')"
for scenario in injected-source-leak injected-path-leak forced-invariant-failure; do
  if "$root/scripts/e2e/local-index-privacy.sh" --scenario "$scenario"; then
    fail "privacy scenario '$scenario' is a negative control and must exit non-zero; it exited 0"
  fi
  [[ "$task8_before" == "$(shasum -a 256 "$task8" | awk '{print $1}')" ]] \
    || fail "privacy scenario '$scenario' rewrote the task-8 receipt; a rejected run must leave it byte-identical"
done
printf '{"stale":true}\n' > "$task8"
if ACR_E2E_FORCE_CANONICAL_FAILURE=1 "$root/scripts/e2e/local-index-privacy.sh" --scenario no-upload; then
  fail "ACR_E2E_FORCE_CANONICAL_FAILURE=1 must force the privacy harness to fail; it exited 0"
fi
[[ ! -e "$task8" ]] || fail "a forced canonical failure must delete the stale task-8 receipt; it still exists"
mutation_target="$root/contracts/examples/v1/capabilities.v1.json"
mutation_hook="$backup/mutate-source"
printf '#!/usr/bin/env bash\nprintf x >> %q\n' "$mutation_target" > "$mutation_hook"
chmod 700 "$mutation_hook"
printf '{"stale":true}\n' > "$task8"
if ACR_E2E_RECEIPT_POST_FSYNC_HOOK="$mutation_hook" "$root/scripts/e2e/local-index-privacy.sh" --scenario no-upload; then
  fail "expected the post-fsync source mutation to be detected and the run to fail; the harness exited 0"
fi
[[ ! -e "$task8" ]] || fail "a source mutation detected after fsync must leave no task-8 receipt; it still exists"
git -C "$root" checkout -- "$mutation_target"

printf '{"sentinel":"task9"}\n' > "$task9"
task9_before="$(shasum -a 256 "$task9" | awk '{print $1}')"
declare -A expected_exit=([local-timeout]=41 [hosted-unavailable]=42 [incompatible-version]=43 [post-response-process-failure]=44)
for scenario in hosted-only local-timeout packet-content-overflow hosted-unavailable incompatible-version writeback-default post-response-process-failure; do
  scenario_log="$backup/$scenario.log"
  set +e
  "$root/scripts/e2e/mcp-codegraph.sh" --scenario "$scenario" >"$scenario_log" 2>&1
  exit_code=$?
  set -e
  case "$scenario" in
    local-timeout|hosted-unavailable|incompatible-version|post-response-process-failure)
      expected_code="${expected_exit[$scenario]}"
      [[ "$exit_code" -eq "$expected_code" ]] \
        || fail "scenario '$scenario' expected exit $expected_code, got $exit_code (log: $scenario_log)"
      grep -Fxq "ACR_E2E_EXPECTED_FAILURE_VALIDATED scenario=$scenario exit_code=$expected_code" "$scenario_log" \
        || fail "scenario '$scenario' exited $expected_code but its log never carried the ACR_E2E_EXPECTED_FAILURE_VALIDATED marker, so the failure was not the expected one (log: $scenario_log)"
      ;;
    *) [[ "$exit_code" -eq 0 ]] || fail "scenario '$scenario' expected exit 0, got $exit_code (log: $scenario_log)" ;;
  esac
  [[ "$task9_before" == "$(shasum -a 256 "$task9" | awk '{print $1}')" ]] \
    || fail "scenario '$scenario' rewrote the task-9 receipt; it must be left byte-identical"
done
forced_failure_log="$backup/forced-expected-failure.log"
set +e
ACR_E2E_FORCE_EXPECTED_FAILURE_ASSERTION=1 "$root/scripts/e2e/mcp-codegraph.sh" --scenario local-timeout >"$forced_failure_log" 2>&1
forced_failure_exit=$?
set -e
[[ "$forced_failure_exit" -eq 1 ]] \
  || fail "ACR_E2E_FORCE_EXPECTED_FAILURE_ASSERTION=1 must make the expected-failure assertion itself fail with exit 1, got $forced_failure_exit"
if grep -Fq 'ACR_E2E_EXPECTED_FAILURE_VALIDATED' "$forced_failure_log"; then
  fail "a forced expected-failure assertion still emitted ACR_E2E_EXPECTED_FAILURE_VALIDATED, so the marker does not prove the assertion ran (log: $forced_failure_log)"
fi
printf '{"stale":true}\n' > "$task9"
if ACR_E2E_FORCE_CANONICAL_FAILURE=1 "$root/scripts/e2e/mcp-codegraph.sh" --scenario mixed; then
  fail "ACR_E2E_FORCE_CANONICAL_FAILURE=1 must force the mcp-codegraph harness to fail; it exited 0"
fi
[[ ! -e "$task9" ]] || fail "a forced canonical failure must delete the stale task-9 receipt; it still exists"

"$root/scripts/e2e/local-index-privacy.sh" --scenario no-upload
"$root/scripts/e2e/mcp-codegraph.sh" --scenario mixed
rm -f "$task9"
barrier="$backup/publication-barrier"
mkdir -p "$barrier"
barrier_hook="$backup/wait-at-publication-barrier"
cat > "$barrier_hook" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
touch "$ACR_E2E_RECEIPT_BARRIER/ready-$$"
while [[ ! -e "$ACR_E2E_RECEIPT_BARRIER/release" ]]; do sleep .01; done
SH
chmod 700 "$barrier_hook"
ACR_E2E_RECEIPT_BARRIER="$barrier" ACR_E2E_RECEIPT_POST_FSYNC_HOOK="$barrier_hook" "$root/scripts/e2e/mcp-codegraph.sh" --scenario mixed & first_writer=$!
ACR_E2E_RECEIPT_BARRIER="$barrier" ACR_E2E_RECEIPT_POST_FSYNC_HOOK="$barrier_hook" "$root/scripts/e2e/mcp-codegraph.sh" --scenario mixed & second_writer=$!
for _ in $(seq 1 1000); do [[ "$(compgen -G "$barrier/ready-*" | wc -l | tr -d ' ')" == 2 ]] && break; sleep .01; done
[[ "$(compgen -G "$barrier/ready-*" | wc -l | tr -d ' ')" == 2 ]] \
  || fail "both concurrent writers should have reached the publication barrier within 10s; saw $(compgen -G "$barrier/ready-*" | wc -l | tr -d ' ') of 2"
touch "$barrier/release"
wait "$first_writer" && wait "$second_writer"
if compgen -G "$evidence/.context-fabric-09-*" >/dev/null; then
  fail "two concurrent writers left a temporary receipt behind in $evidence; publication must be atomic"
fi
faulty="$backup/fixed-temp-writer"
cat > "$faulty" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
set -C
: > "$1/fixed.tmp"
SH
chmod 700 "$faulty"
"$faulty" "$barrier" & faulty_one=$!
"$faulty" "$barrier" & faulty_two=$!
set +e
wait "$faulty_one"; faulty_one_status=$?
wait "$faulty_two"; faulty_two_status=$?
set -e
[[ "$faulty_one_status" -ne "$faulty_two_status" ]] \
  || fail "a fixed-name temp writer must make exactly one of two racing writers fail; both exited $faulty_one_status, so the O_EXCL guard is not exclusive"
rm -f "$barrier/fixed.tmp"
python3 - "$task8" "$task9" "$root" <<'PY'
import json,subprocess,sys
head=subprocess.check_output(['git','-C',sys.argv[3],'rev-parse','HEAD'],text=True).strip()
for path,task in zip(sys.argv[1:3],('CHAOS-3007 Task 8','CHAOS-3007 Task 9')):
    receipt=json.load(open(path))
    assert receipt['task']==task and receipt['source_revision']==head
    assert receipt['source_worktree_clean'] and receipt['source_identity_unchanged']
    assert receipt['harness_sha256'] and receipt['binary_sha256'] and receipt['tls_verified']
task9=json.load(open(sys.argv[2]))
assert task9['scenario']=='mixed' and task9['verdict']=='pass'
assert task9['mcp']['record_episode_rejected'] is True
assert task9['mcp']['session_valid_after_rejected_writeback'] is True
assert task9['writeback']['hosted_episode_posts']==0
PY
task9_before="$(shasum -a 256 "$task9" | awk '{print $1}')"
"$root/scripts/e2e/mcp-codegraph-live.sh" --self-test
[[ "$task9_before" == "$(shasum -a 256 "$task9" | awk '{print $1}')" ]] \
  || fail "mcp-codegraph-live --self-test rewrote the task-9 receipt; a self-test must not touch it"
printf '{"stale":true}\n' > "$live"
for receipt in "$task8" "$task9"; do
  python3 - "$receipt" <<'PY'
import json,os,stat,sys
assert stat.S_IMODE(os.stat(sys.argv[1]).st_mode)==0o600
json.load(open(sys.argv[1]))
PY
done
printf '{"stale":true}\n' > "$live"
if "$root/scripts/e2e/mcp-codegraph-live.sh" --repo /definitely-missing --scenario mixed; then
  fail "a live run against a nonexistent repo must fail; it exited 0"
fi
[[ ! -e "$live" ]] || fail "a failed live run must leave no live receipt; it still exists"
out_of_tree="$(mktemp -d)"
(cd "$out_of_tree" && "$root/scripts/e2e/local-index-privacy.sh" --scenario no-upload)
rm -rf "$out_of_tree"
