#!/usr/bin/env bash
# CHAOS-3742 trial runner shared setup. Sourced by every script in this
# directory -- never invoked directly.
#
# Repo-root-relative throughout (no hard-coded absolute paths), so these
# scripts work from any checkout, following the scripts/clients/*.sh
# convention. Secrets are ALWAYS sourced from ops/.env at RUNTIME, never
# baked into a script file (sol review F12).
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"

# git-aware repo root (sol review R2 residual): correct regardless of the
# checkout's directory NAME, unlike a hard-coded "acr-wt-trial" path
# component would be. Falls back to the path-relative derivation only if
# git itself is unavailable.
#
# sol review F1 round 2: under `set -e` (line 9), a bare assignment whose
# RHS command substitution fails (`x=$(failing-cmd)`) kills the script on
# the spot -- that failure is NOT exempted from -e just because it
# happened inside `$(...)`. Every fallback below therefore lives inside an
# `if`/`elif` CONDITION, never a bare assignment: -e is suspended while
# evaluating the command list of an if/elif/while/until condition, so a
# failing probe there falls through to the next branch instead of exiting
# the script. (Sol proved the pre-fix version dead-code by simulating a
# missing git: it died on this exact line 127 before ever reaching the
# "if git itself is unavailable" branch that line's own comment promised.)
if repo_root="$(cd "$script_dir" && git rev-parse --show-toplevel 2>/dev/null)" && [[ -n "$repo_root" ]]; then
  :
else
  repo_root="$(cd "$script_dir/../.." && pwd -P)"
fi

# CWD-VS-SCRIPT REPO FAIL-FAST (2026-08-23, live incident): repo_root above
# is resolved from THIS SCRIPT FILE'S OWN on-disk location (via
# BASH_SOURCE/script_dir), which is correct in isolation -- but a caller
# invoking these scripts by a RELATIVE path (`./scripts/trial/run-two-turn.sh`)
# from the WRONG worktree's own directory has bash resolve and exec that
# OTHER worktree's copy of this very file before it ever runs a single
# line here, so repo_root ends up self-consistently "correct" for a
# worktree that was never the caller's intent. A live run hit exactly
# this: an operator's shell cwd had drifted to a stale worktree several
# commits behind; the launched scripts (this one included) compiled and
# ran entirely from that stale tree while a SEPARATE provenance signal
# (ACR_TEST_TRIAL_BASE_SHA / origin/main, a ref shared across every
# worktree of this repo) happened to still read the intended, current
# commit -- so the artifact's own provenance fields looked plausible while
# the code that actually produced it was stale. Nothing in the chain
# above is individually wrong; the caller's cwd is simply never checked
# against what actually got resolved.
#
# The fix: compare repo_root (this script's OWN worktree) against
# whatever `git rev-parse --show-toplevel` reports for the CALLER's raw
# cwd (no cd, so it reflects whatever worktree the operator was actually
# standing in when the command was typed) -- a caller running from
# outside any git worktree at all (empty result) is not itself an error
# (e.g. invoked via an absolute path from $HOME), only a DISAGREEING
# worktree is.
if caller_toplevel="$(git rev-parse --show-toplevel 2>/dev/null)" && [[ -n "$caller_toplevel" && "$caller_toplevel" != "$repo_root" ]]; then
  echo "common.sh: refusing to run -- the invoking shell's cwd resolves to a DIFFERENT worktree than this script's own file location." >&2
  echo "  caller's cwd worktree : $caller_toplevel" >&2
  echo "  this script's worktree: $repo_root" >&2
  echo "  This is exactly the shape of the 2026-08-23 stale-checkout incident (see this check's own comment) -- cd into $repo_root (or invoke this script by an absolute path from there) before retrying." >&2
  exit 1
fi

# dev_health_root resolves via the git COMMON dir, not repo_root/.. --
# repo_root (--show-toplevel) is the CURRENT worktree's own root, which for
# a lane checked out under dev-health/worktrees/acr/<branch> sits two
# levels deeper than a plain dev-health/acr checkout; repo_root/.. would
# then land inside dev-health/worktrees/acr instead of dev-health, and
# ops/.env and .remember/ (both outside this repo) would never be found.
# --git-common-dir resolves to the SAME .git for every linked worktree of
# this repo, so its grandparent is dev-health regardless of which worktree
# sourced this script.
#
# sol review F1, round 3 (closing the class, not another spot-guard):
# round 1 found --git-common-dir alone insufficient (a plain checkout at
# a non-root cwd gets a path relative to the CALLER's cwd back); round 2
# found the fallback chain itself was dead code under `set -e` (a bare
# `x=$(failing-cmd)` aborts the script even though it "looks like" part
# of an if-condition, unless it truly is one); round 3's mutation showed
# a probe can SUCCEED (exit 0) while returning a well-formed but WRONG
# path (e.g. a --path-format=absolute shim pointing at a directory that
# does not exist) -- no per-step guard on any single git invocation can
# rule that out in general. Patching a fourth spot-guard would just be
# the fourth round of the same defect class.
#
# Instead: validate the RESULT, not the mechanics that produced it. Each
# tier below produces a CANDIDATE path, and a candidate is accepted only
# if it passes a landmark check (ops/.env exists under it) -- the one
# thing every consumer of dev_health_root actually needs. A wrong path,
# an empty path, a nonexistent path, and a cd failure are then all the
# SAME outcome (landmark check fails, fall through to the next tier), so
# no future regression in any one tier's git invocation can reopen this
# bug class. The whole resolution runs inside one function, called
# exactly once via the `if` below -- that `if` is the ONLY place in this
# routine `set -e` can see a failure; everything inside the function
# runs under an explicit `set +e` of its own (subshells/command
# substitutions INHERIT -e from the caller, so without this an early
# tier's failure would abort the function itself before trying the next
# tier, silently reintroducing round 2's exact bug one level down).
#
# Accepted residual (chris, 2026-08-16): the landmark check proves a
# candidate LOOKS like the dev-health root (ops/.env present under it);
# it is not a unique-identity proof. A stray ops/.env sitting at a WRONG
# candidate (e.g. worktrees/acr/ops/.env, reachable only when git is
# absent and tier 3's repo_root/.. derivation is active -- see tier 3
# below) would false-accept that candidate. Contrived filesystem state
# only, not something a normal checkout produces; downstream corpus
# SHA-256 self-certification and credential validity bound the damage
# if it ever happened. Do not add a second landmark or an identity check
# here without a measured need -- this is a deliberate stopping point,
# not an oversight.
resolve_dev_health_root() (
  set +e
  local candidate legacy_common_git_dir

  # Tier 1: git --path-format=absolute --git-common-dir (git >= 2.31,
  # always an absolute path regardless of the caller's cwd).
  candidate="$(cd "$script_dir" && git rev-parse --path-format=absolute --git-common-dir 2>/dev/null)"
  [[ -n "$candidate" ]] && candidate="$(cd "$candidate/../.." 2>/dev/null && pwd -P)"
  if [[ -n "$candidate" && -f "$candidate/ops/.env" ]]; then
    printf '%s\n' "$candidate"
    return 0
  fi

  # Tier 2: bare git --git-common-dir (older git rejects --path-format).
  # May print a path RELATIVE TO $script_dir's cwd, so it is resolved in
  # the same subshell/cwd that produced it.
  legacy_common_git_dir="$(cd "$script_dir" && git rev-parse --git-common-dir 2>/dev/null)"
  [[ -n "$legacy_common_git_dir" ]] && candidate="$(cd "$script_dir" && cd "$legacy_common_git_dir/../.." 2>/dev/null && pwd -P)"
  if [[ -n "$candidate" && -f "$candidate/ops/.env" ]]; then
    printf '%s\n' "$candidate"
    return 0
  fi

  # Tier 3: git unavailable entirely -- the pre-CHAOS-3855 path-relative
  # derivation. Correct for a plain, non-worktree checkout; cannot be
  # correct for a linked worktree (there is no way to find the shared
  # root without asking git), but that combination is not realistic --
  # git is required to create/use a worktree in the first place.
  candidate="$(cd "$repo_root/.." 2>/dev/null && pwd -P)"
  if [[ -n "$candidate" && -f "$candidate/ops/.env" ]]; then
    printf '%s\n' "$candidate"
    return 0
  fi

  return 1
)

if dev_health_root="$(resolve_dev_health_root)"; then
  :
else
  # sol review (ancillary wording defect): this message used to claim
  # exporting ACR_TRIAL_CORPUS/ACR_TRIAL_RESULTS_DIR bypasses this
  # resolution -- false, both are only read AFTER a successful
  # resolution (see the `:=` defaults below), and `exit 1` on the next
  # line means this script never reaches them. There is no env var that
  # bypasses resolve_dev_health_root itself.
  echo "common.sh: could not resolve the dev-health root -- tried git --path-format=absolute --git-common-dir, bare git --git-common-dir, and \$repo_root/.. (from $script_dir), and none of them contained ops/.env. Run this from a checkout where ops/.env exists two levels above this repo (or make sure git is on PATH and resolves this repo's common dir correctly)." >&2
  exit 1
fi

# The withheld corpus and the trial-results output dir live in the parent
# dev-health checkout's .remember/ (outside this repo, never committed
# anywhere) -- overridable, defaulted to this trial's known paths.
#
# CURRENT default (2026-08-24): ext65, paired with the signed
# .remember/trial-results/oracle-annex-v2-ext65.json -- acr-3778-corpus-frozen-annotated.json
# is the CHAOS-3860 holdout and is eval-only forever (never a live-trial
# default; see docs/design/context-fabric-panel-run-manifest.md §4). Before
# changing this default again, confirm the new corpus's sha256 against a
# recent successful run's own provenance.corpus_sha256.
: "${ACR_TRIAL_CORPUS:=$dev_health_root/.remember/acr-3778-corpus-ext65.json}"
: "${ACR_TRIAL_RESULTS_DIR:=$dev_health_root/.remember/trial-results}"
: "${ACR_TRIAL_ORG:=70d529e0-3c06-4597-8480-794fd02328b6}"
mkdir -p "$ACR_TRIAL_RESULTS_DIR"

ops_env="$dev_health_root/ops/.env"
if [[ ! -f "$ops_env" ]]; then
  echo "common.sh: $ops_env not found -- cannot source DB/model credentials" >&2
  exit 1
fi
trial_secret() {
  grep -E "^$1=" "$ops_env" | cut -d= -f2- | tr -d '"'
}

# ACR_TRIAL_DATA_PLANE (CHAOS-4186 round-3 design ruling; replaces the
# per-store `:=` override this file carried before): ONE switch moves ALL
# THREE trial stores together -- "kiac" (default -- chris's standing order,
# 2026-08-24: kiac is THE trial stack for every run, no comparability
# exception) or "compose" (legacy fallback, kept for anyone still standing
# up compose locally). The prior design let a caller override individual
# ACR_TEST_TRIAL_* endpoints piecemeal; codex xhigh review round 3 found
# that run-two-turn-parallel.sh's own Postgres override reached only
# Postgres, silently leaving FalkorDB/ClickHouse on compose -- a hybrid
# measurement with no error. A single switch makes that class of bug
# structurally impossible: there is no partial state to land in between
# "compose" and "kiac".
: "${ACR_TRIAL_DATA_PLANE:=kiac}"

# Escape hatch, ALL-OR-NONE: six raw host/port vars (postgres, clickhouse,
# falkor) let an operator point at something neither switch value names
# (e.g. a one-off diagnostic relay -- see run-two-turn-parallel.sh's own
# CHAOS-4100 A/B incident note). Setting ANY one requires setting every
# other one too -- a partial set is indistinguishable from "forgot one",
# which is exactly the silent-hybrid bug class this file exists to close,
# so it fails closed with the missing names listed rather than filling the
# gap from whichever switch value happens to be in effect.
_acr_trial_override_names=(ACR_TRIAL_PG_HOST ACR_TRIAL_PG_PORT ACR_TRIAL_CH_HOST ACR_TRIAL_CH_PORT ACR_TRIAL_FALKOR_HOST ACR_TRIAL_FALKOR_PORT)
_acr_trial_override_set=0
_acr_trial_override_missing=()
for _acr_trial_override_name in "${_acr_trial_override_names[@]}"; do
  if [[ -n "${!_acr_trial_override_name:-}" ]]; then
    _acr_trial_override_set=1
  else
    _acr_trial_override_missing+=("$_acr_trial_override_name")
  fi
done
if [[ "$_acr_trial_override_set" == "1" && "${#_acr_trial_override_missing[@]}" -gt 0 ]]; then
  echo "common.sh: partial per-store override -- set ${_acr_trial_override_names[*]} together, or set none of them (missing: ${_acr_trial_override_missing[*]})" >&2
  exit 1
fi

# bracket_host_if_ipv6 (CHAOS-4228): a DSN authority is `host:port` --
# unbracketed, an IPv6 literal's own internal `:` characters make the
# boundary with the trailing `:<port>` ambiguous (RFC 3986 requires an
# IPv6 host to be wrapped in `[]` inside a URI authority for exactly this
# reason). A bare IPv4 address or hostname (no `:`) passes through
# unchanged; a host already bracketed is left alone rather than
# double-wrapped.
bracket_host_if_ipv6() {
  local host="$1"
  if [[ "$host" == *:* && "$host" != \[*\] ]]; then
    printf '[%s]' "$host"
  else
    printf '%s' "$host"
  fi
}

trial_wire_common_env() {
  export ACR_POSTGRES_CONNECTION_KIND=direct
  export ACR_TEST_TRIAL_CORPUS="$ACR_TRIAL_CORPUS"
  export ACR_TEST_TRIAL_ORG="$ACR_TRIAL_ORG"

  local pg_host pg_port pg_user pg_password ch_dsn ch_host falkor_addr falkor_host data_plane_label
  # falkor_tls/falkor_allow_insecure: only the kiac branch populates these
  # (the trial FalkorDB's plaintext posture is a kiac-plane-specific fact);
  # left empty for compose/override, so the export below is a no-op there
  # (existing ambient ACR_CONTEXT_FABRIC_FALKOR_TLS/ALLOW_INSECURE, if any,
  # pass through unexported-by-this-function rather than being clobbered).
  local falkor_tls="" falkor_allow_insecure=""

  if [[ "$_acr_trial_override_set" == "1" ]]; then
    # data_plane_label is deliberately NOT "$ACR_TRIAL_DATA_PLANE" here
    # (codex xhigh review, fresh cycle round 1, P1): the six-var escape
    # hatch's all-or-none check only proves COMPLETENESS (all six set),
    # never COHERENCE -- nothing stops an operator pointing the PG pair at
    # kiac and the CH/Falkor pair at compose, an explicit mixed-plane run.
    # Reporting that as "kiac" (or "compose") in the provenance/telemetry
    # line below would be a false claim about which stack the run actually
    # hit; "override" self-declares as non-standard instead.
    data_plane_label="override"
    pg_host="$ACR_TRIAL_PG_HOST"; pg_port="$ACR_TRIAL_PG_PORT"
    pg_user="$(trial_secret POSTGRES_USER)"; pg_password="$(trial_secret POSTGRES_PASSWORD)"
    ch_dsn="clickhouse://$(trial_secret CLICKHOUSE_USER):$(trial_secret CLICKHOUSE_PASSWORD)@$(bracket_host_if_ipv6 "$ACR_TRIAL_CH_HOST"):${ACR_TRIAL_CH_PORT}/$(trial_secret CLICKHOUSE_DB)"
    ch_host="$ACR_TRIAL_CH_HOST"
    # CHAOS-4228 (codex R1, real High): falkor_addr is host:port too, and
    # falkorgraph.Config.validate() parses it with net.SplitHostPort,
    # which requires the same [] bracketing for an IPv6 host -- an
    # unbracketed IPv6 address here fails startup validation outright,
    # not just an ambiguous string.
    falkor_addr="$(bracket_host_if_ipv6 "$ACR_TRIAL_FALKOR_HOST"):${ACR_TRIAL_FALKOR_PORT}"
    falkor_host="$ACR_TRIAL_FALKOR_HOST"
  elif [[ "$ACR_TRIAL_DATA_PLANE" == "kiac" ]]; then
    data_plane_label="kiac"
    # deploy/local/trial-data.sh dsn --env resolves ALL THREE endpoints in
    # one call, reading the credential from the LIVE cluster secret --
    # never independently re-derived here, so this can never drift from
    # what the kiac data plane actually has seeded. Structured key=value
    # output (team-lead design ruling, CHAOS-4186 fresh review cycle,
    # residual finding 6), NOT a DSN string to split on `:`/`@` -- that
    # was delimiter-dependent and broke on an IPv6 host or an `@` in the
    # password.
    #
    # Read line-by-line and assign directly -- NEVER `eval` subprocess
    # output (team-lead ruling, CHAOS-4186 round 3 follow-up): round 3
    # found that even `printf %q`-quoted eval'd text isn't safe, because
    # merged stderr (an incidental kubectl warning) could reach the
    # eval'd string, and the key-renaming rewrite done before eval could
    # corrupt a value. Both were symptoms of shell-interpreting a
    # subprocess's output at all. Splitting on the first `=` and
    # allowlisting the key via a fixed `case` (any other key is a hard
    # error, as is a missing expected key) closes the injection class
    # structurally instead of patching each symptom.
    #
    # stdout only, deliberately NOT `2>&1`: an incidental kubectl warning
    # on stderr must never reach this loop. On failure only, the command
    # is re-run once with stderr merged purely for the diagnostic message.
    #
    # ACR_TRIAL_KIAC_DSN_BIN overrides which executable is asked for
    # `dsn --env`, defaulting to the real trial-data.sh -- a testability
    # hook (same shape as ACR_TRIAL_PSQL_ADMIN_SELFTEST elsewhere in this
    # directory) letting test-kiac-dsn-reader.sh exercise this parser
    # against fabricated output without a live cluster. Substituting the
    # BINARY, never the args or an eval'd command string, so this stays
    # inert to injection the same way the real path is.
    local kiac_dsn_bin="${ACR_TRIAL_KIAC_DSN_BIN:-deploy/local/trial-data.sh}"
    local kiac_env_output
    if ! kiac_env_output="$(cd "$repo_root" && "$kiac_dsn_bin" dsn --env 2>/dev/null)"; then
      local kiac_env_diag
      kiac_env_diag="$(cd "$repo_root" && "$kiac_dsn_bin" dsn --env 2>&1 >/dev/null)"
      echo "common.sh: ACR_TRIAL_DATA_PLANE=kiac but '$kiac_dsn_bin dsn --env' failed -- is the kiac trial data plane applied and KUBECONFIG set? output:" >&2
      echo "$kiac_env_diag" >&2
      exit 1
    fi
    # Prefixed _kiac_env_* names, deliberately NOT the ACR_TEST_TRIAL_*
    # names this function exports further down: `local
    # ACR_TEST_TRIAL_PG_HOST` here would shadow that later `export
    # ACR_TEST_TRIAL_PG_HOST=...` -- export on an already-local variable
    # exports the LOCAL binding, which is erased the moment this function
    # returns, silently discarding it from every caller (confirmed live:
    # "ACR_TEST_TRIAL_PG_HOST: unbound variable" in the calling shell
    # despite this function completing without error).
    local _kiac_env_PG_HOST="" _kiac_env_PG_PORT="" _kiac_env_PG_USER="" _kiac_env_PG_PASSWORD="" _kiac_env_PG_DB=""
    local _kiac_env_CH_HOST="" _kiac_env_CH_PORT="" _kiac_env_CH_HTTP_PORT="" _kiac_env_CH_USER="" _kiac_env_CH_PASSWORD="" _kiac_env_CH_DB=""
    local _kiac_env_FALKOR_HOST="" _kiac_env_FALKOR_PORT=""
    # FALKOR_TLS/FALKOR_ALLOW_INSECURE (CHAOS-4186 follow-up, real
    # incident): the trial FalkorDB is always plaintext, never TLS --
    # acr-projector's own client defaults to TLS=true and hangs a
    # ClientHello against the plaintext port until its 30s request
    # timeout on every tick otherwise. Captured here so any caller
    # sourcing common.sh under the kiac plane inherits the correct
    # values automatically (exported below, same as every other field).
    local _kiac_env_FALKOR_TLS="" _kiac_env_FALKOR_ALLOW_INSECURE=""
    local kiac_env_count=0 kiac_line kiac_key kiac_value
    # CHAOS-4155 launcher fix (lane-4155-p2, live-diagnosed via a
    # credential-guarded `bash -x` pass): this loop used to be `while IFS=
    # read -r kiac_line; do ... done <<<"$kiac_env_output"`. On bash >=5.1
    # a `<<<` here-string is backed by an internal self-pipe rather than a
    # temp file when the string is small (a performance optimization,
    # avoiding the fork/write/rewind a temp file needs) -- on this host
    # that self-pipe deadlocked EVERY TIME (confirmed: xtrace showed the
    # process sleeping forever with zero children ever forked, right at
    # this loop, across parallel AND sequential launcher attempts; not a
    # kiac/network/Docker issue -- trial-data.sh's own output was already
    # captured and verified correct before this loop ever runs). A `for`
    # loop over a manually newline-split `$kiac_env_output` reads the same
    # already-in-memory string with no pipe, no fork, and identical
    # per-line semantics. IFS/noglob are saved and restored explicitly
    # rather than scoped with `local IFS=`/`local -` (this is a branch
    # inside a larger function, not a dedicated one) so nothing after this
    # block inherits a stray no-glob/newline-only word-splitting mode.
    # codex R1 (Medium, confirmed): the first version of this save read
    # bare "$IFS", which -- under this script's own `set -u` -- aborts
    # with "IFS: unbound variable" if a caller had explicitly `unset IFS`
    # rather than leaving it at its default. The original `while IFS=
    # read` loop never had this failure mode: `IFS=` there is a
    # command-scoped prefix assignment for `read` alone, never a read of
    # the ambient variable. `${IFS-}` mirrors that -- expands to empty
    # under `-u` when IFS is unset, instead of erroring -- and
    # `_kiac_ifs_was_unset` records which case it was so the restore
    # below can put IFS back to truly unset rather than merely empty.
    local _kiac_saved_ifs="${IFS-}" _kiac_ifs_was_unset=0 _kiac_had_noglob=0
    [[ -z "${IFS+set}" ]] && _kiac_ifs_was_unset=1
    [[ "$-" == *f* ]] && _kiac_had_noglob=1
    # codex R1 (Low, confirmed): restores the IFS/noglob state at every
    # exit from this loop, including the unrecognized-key hard-fail
    # below -- every OTHER exit 1 in this function also terminates the
    # whole process outright (no continuing caller today), but a helper
    # is cheap insurance against a future caller that traps EXIT/RETURN
    # around trial_wire_common_env and would otherwise observe a stray
    # no-glob, newline-only-split shell state after a failed call.
    _kiac_restore_ifs_noglob() {
      [[ "$_kiac_had_noglob" -eq 1 ]] || set +f
      if [[ "$_kiac_ifs_was_unset" -eq 1 ]]; then
        unset IFS
      else
        IFS="$_kiac_saved_ifs"
      fi
    }
    IFS=$'\n'
    set -f
    for kiac_line in $kiac_env_output; do
      [[ -z "$kiac_line" ]] && continue
      kiac_key="${kiac_line%%=*}"
      kiac_value="${kiac_line#*=}"
      kiac_env_count=$((kiac_env_count + 1))
      case "$kiac_key" in
        ACR_TEST_TRIAL_PG_HOST) _kiac_env_PG_HOST="$kiac_value" ;;
        ACR_TEST_TRIAL_PG_PORT) _kiac_env_PG_PORT="$kiac_value" ;;
        ACR_TEST_TRIAL_PG_USER) _kiac_env_PG_USER="$kiac_value" ;;
        ACR_TEST_TRIAL_PG_PASSWORD) _kiac_env_PG_PASSWORD="$kiac_value" ;;
        ACR_TEST_TRIAL_PG_DB) _kiac_env_PG_DB="$kiac_value" ;;
        ACR_TEST_TRIAL_CH_HOST) _kiac_env_CH_HOST="$kiac_value" ;;
        ACR_TEST_TRIAL_CH_PORT) _kiac_env_CH_PORT="$kiac_value" ;;
        ACR_TEST_TRIAL_CH_HTTP_PORT) _kiac_env_CH_HTTP_PORT="$kiac_value" ;;
        ACR_TEST_TRIAL_CH_USER) _kiac_env_CH_USER="$kiac_value" ;;
        ACR_TEST_TRIAL_CH_PASSWORD) _kiac_env_CH_PASSWORD="$kiac_value" ;;
        ACR_TEST_TRIAL_CH_DB) _kiac_env_CH_DB="$kiac_value" ;;
        ACR_TEST_TRIAL_FALKOR_HOST) _kiac_env_FALKOR_HOST="$kiac_value" ;;
        ACR_TEST_TRIAL_FALKOR_PORT) _kiac_env_FALKOR_PORT="$kiac_value" ;;
        ACR_CONTEXT_FABRIC_FALKOR_TLS) _kiac_env_FALKOR_TLS="$kiac_value" ;;
        ACR_CONTEXT_FABRIC_FALKOR_ALLOW_INSECURE) _kiac_env_FALKOR_ALLOW_INSECURE="$kiac_value" ;;
        *)
          echo "common.sh: 'trial-data.sh dsn --env' emitted an unrecognized key '$kiac_key' -- refusing to assign it" >&2
          _kiac_restore_ifs_noglob
          exit 1
          ;;
      esac
    done
    _kiac_restore_ifs_noglob
    unset -f _kiac_restore_ifs_noglob
    [[ "$kiac_env_count" -eq 15 ]] || { echo "common.sh: 'trial-data.sh dsn --env' emitted $kiac_env_count line(s), expected exactly 15" >&2; exit 1; }
    local _kiac_env_v
    for _kiac_env_v in _kiac_env_PG_HOST _kiac_env_PG_PORT _kiac_env_PG_USER _kiac_env_PG_PASSWORD _kiac_env_PG_DB \
                       _kiac_env_CH_HOST _kiac_env_CH_PORT _kiac_env_CH_HTTP_PORT _kiac_env_CH_USER _kiac_env_CH_PASSWORD _kiac_env_CH_DB \
                       _kiac_env_FALKOR_HOST _kiac_env_FALKOR_PORT \
                       _kiac_env_FALKOR_TLS _kiac_env_FALKOR_ALLOW_INSECURE; do
      [[ -n "${!_kiac_env_v}" ]] || { echo "common.sh: 'trial-data.sh dsn --env' did not set $_kiac_env_v (empty or missing)" >&2; exit 1; }
    done
    pg_host="$_kiac_env_PG_HOST"; pg_port="$_kiac_env_PG_PORT"
    pg_user="$_kiac_env_PG_USER"; pg_password="$_kiac_env_PG_PASSWORD"
    ch_dsn="clickhouse://${_kiac_env_CH_USER}:${_kiac_env_CH_PASSWORD}@$(bracket_host_if_ipv6 "$_kiac_env_CH_HOST"):${_kiac_env_CH_PORT}/${_kiac_env_CH_DB}"
    ch_host="$_kiac_env_CH_HOST"
    # CHAOS-4228: same bracket_host_if_ipv6 as the override branch above.
    falkor_addr="$(bracket_host_if_ipv6 "$_kiac_env_FALKOR_HOST"):${_kiac_env_FALKOR_PORT}"
    falkor_host="$_kiac_env_FALKOR_HOST"
    falkor_tls="$_kiac_env_FALKOR_TLS"
    falkor_allow_insecure="$_kiac_env_FALKOR_ALLOW_INSECURE"
  elif [[ "$ACR_TRIAL_DATA_PLANE" == "compose" ]]; then
    data_plane_label="compose"
    pg_host="127.0.0.1"; pg_port="5432"
    pg_user="$(trial_secret POSTGRES_USER)"; pg_password="$(trial_secret POSTGRES_PASSWORD)"
    ch_dsn="clickhouse://$(trial_secret CLICKHOUSE_USER):$(trial_secret CLICKHOUSE_PASSWORD)@127.0.0.1:9000/$(trial_secret CLICKHOUSE_DB)"
    ch_host="127.0.0.1"
    falkor_addr="127.0.0.1:16379"
    falkor_host="127.0.0.1"
  else
    echo "common.sh: ACR_TRIAL_DATA_PLANE must be 'compose' or 'kiac', got '$ACR_TRIAL_DATA_PLANE'" >&2
    exit 1
  fi

  export ACR_TEST_TRIAL_FALKOR_ADDR="$falkor_addr"
  # CHAOS-4228: same bracket_host_if_ipv6 as the ClickHouse DSN above --
  # this shares $pg_host with run-two-turn-parallel.sh's own per-shard
  # postgres:// DSN (see that script's trial_pg_dsn, same fix applied
  # there independently since it reads PG_HOST as a raw component, not
  # this composed string).
  export ACR_TEST_TRIAL_POSTGRES_DSN="postgres://${pg_user}:${pg_password}@$(bracket_host_if_ipv6 "$pg_host"):${pg_port}/acr?sslmode=disable"
  export ACR_TEST_TRIAL_CLICKHOUSE_DSN="$ch_dsn"
  # Raw components, not just the composed DSN above: run-two-turn-parallel.sh
  # needs these to build a PER-SHARD Postgres DSN (same host/port/user/
  # password, different database name each shard) -- see that script's own
  # PG_HOST/PG_PORT/PG_USER/PG_PASSWORD, which read these instead of
  # deriving their own connection independently (the exact class of bug
  # that produced round 3's hybrid-measurement finding).
  export ACR_TEST_TRIAL_PG_HOST="$pg_host"
  export ACR_TEST_TRIAL_PG_PORT="$pg_port"
  export ACR_TEST_TRIAL_PG_USER="$pg_user"
  export ACR_TEST_TRIAL_PG_PASSWORD="$pg_password"
  # CH_HOST/FALKOR_HOST (CHAOS-4186 data_plane provenance PR): bare hosts,
  # same reasoning as PG_HOST above -- the producer records these directly
  # rather than parsing a host back out of ACR_TEST_TRIAL_CLICKHOUSE_DSN/
  # FALKOR_ADDR, which would reintroduce the exact DSN-string-parsing class
  # this whole cutover just finished eliminating from the shell side.
  export ACR_TEST_TRIAL_CH_HOST="$ch_host"
  export ACR_TEST_TRIAL_FALKOR_HOST="$falkor_host"
  # ACR_CONTEXT_FABRIC_FALKOR_TLS/ALLOW_INSECURE (CHAOS-4186 follow-up,
  # real incident during the VM resize): NOT ACR_TEST_TRIAL_*-prefixed --
  # these are acr-projector's own raw config var names, exported verbatim
  # so a caller that also launches acr-projector against the kiac plane
  # (e.g. a graph-rebuild recipe) inherits the right values without
  # restating them. Empty/no-op on compose or override (see the `local`
  # declaration above). See trial-data.sh's own `dsn --env` comment for
  # why the kiac plane always needs both: acr-projector's client defaults
  # to TLS=true and hangs a ClientHello against the trial FalkorDB's
  # plaintext port until its own 30s request timeout, on every tick,
  # otherwise.
  if [[ -n "$falkor_tls" ]]; then
    export ACR_CONTEXT_FABRIC_FALKOR_TLS="$falkor_tls"
    export ACR_CONTEXT_FABRIC_FALKOR_ALLOW_INSECURE="$falkor_allow_insecure"
  fi
  export ACR_TEST_TRIAL_EMBED_MODEL=text-embedding-3-large
  export ACR_TEST_TRIAL_EMBED_DIMENSION=3072
  export ACR_TEST_TRIAL_EMBED_API_KEY="$(trial_secret OPENAI_API_KEY)"

  # Seeds the `data_plane` report-provenance field (schema v28, CHAOS-4186
  # follow-up PR): exported so the producer reads it directly rather than
  # re-inferring it from a host string, and printed at every launcher's own
  # start so which stack a run hit is visible immediately. "override" (not
  # a false "compose"/"kiac" claim) when the six-var escape hatch is in
  # play -- see that branch's own comment on why coherence isn't provable
  # there. Never prints a credential.
  export ACR_TEST_TRIAL_DATA_PLANE="$data_plane_label"
  # CHAOS-4302: piped through printf, not a `<<<` here-string -- the same
  # small-here-string deadlock class the CHAOS-4155 fix above eliminated
  # from this function's `dsn --env` loop.
  echo "common.sh: data_plane=$data_plane_label pg=${pg_host}:${pg_port} ch=$(printf '%s' "$ch_dsn" | sed -E 's#.*@##') falkor=$falkor_addr" >&2
}

# trial_wire_graph_lifecycle_env (CHAOS-3916, local/trial slice) is
# DELIBERATELY NOT folded into trial_wire_common_env above: that function is
# shared by every live trial script (replay, arm, smoke, reclass, W0, D2B,
# ...), but generative_trial_live_test.go's own trialCaseReport doc comment
# records that ONLY chaos3884_replay_harness_test.go populates
# ResolvedActiveEpoch/GraphLifecycleEnabled in its provenance today -- "every
# other trial script's provenance leaves them at their zero values" even
# though wireProductionEnv (shared by all of them) would silently start
# wiring a REAL, active EpochResolver into their graph readers too if this
# env var were exported common-wide. That is exactly the "measurement fails
# toward fine" class chris's CHAOS-3896 Slice B rider exists to prevent:
# an unrecorded active epoch is worse than none. Call this ADDITIONALLY,
# only from run-replay.sh, so every OTHER trial script stays byte-identical
# (codex xhigh review finding, confirmed).
#
# The TRIAL-PREFIXED name, NOT the bare production
# ACR_CONTEXT_FABRIC_GRAPH_LIFECYCLE_ENABLED one: generative_trial_live_test.go's
# wireProductionEnv calls clearAmbientACREnv first, which wipes any ambient
# bare export -- exactly the trap that function's own doc comment names
# ("precisely why 'export the real var and hope' silently measured epoch 0
# twice before this fix"). wireProductionEnv then re-derives the real var
# from THIS one (ACR_TEST_TRIAL_ prefix survives the clear by design).
# Confirmed live: exporting the bare name still measured wired=false; this
# is what actually reaches the harness. This ONLY wires the epoch resolver
# so the harness reads a REAL answer from Postgres instead of defaulting a
# hardcoded 0 in (confirmed live: the ground-truth org's own lifecycle row
# already resolves to epoch 1, from an earlier, unrelated flip -- reading
# it is a NO-OP on Postgres, never a write) -- it does not itself trigger
# any rebuild; ACR_TEST_TRIAL_POSTGRES_DSN (trial_wire_common_env) is the
# same DSN buildReplayEpochResolver reads, and migration
# 0019_context_fabric_graph_lifecycle.sql (+ related) is already applied to
# the standing stack's acr database.
trial_wire_graph_lifecycle_env() {
  export ACR_TEST_TRIAL_GRAPH_LIFECYCLE_ENABLED=1
}

trial_run_go_test() {
  ( cd "$repo_root" && go test -run TestGenerativeTrialCorpus -count=1 -v -timeout "${1:?timeout required}" ./internal/runtime/hosted )
}

# Manual two-turn sharding (2026-08-24): if you hand-set
# ACR_TEST_TRIAL_SHARD_CASE_INDICES yourself instead of going through
# run-two-turn-parallel.sh, also set ACR_TEST_TRIAL_SHARD_COUNT and
# ACR_TEST_TRIAL_SHARD_INDEX -- all three, or the go test fails closed now
# (it used to silently run the full corpus).
