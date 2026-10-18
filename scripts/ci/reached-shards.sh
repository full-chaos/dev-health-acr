#!/usr/bin/env bash
set -euo pipefail

# reached-shards.sh decides which `unit` and `race*` shards of ci.yml a
# pull request runs. A shard runs when its package list -- the one
# scripts/ci/test-shard.sh already prints for it, never a second partition --
# holds at least one package the change REACHES. The whole shard then runs, so
# shard budgets and test-shard-closure.sh's union proof are unchanged.
#
# The rule, in order:
#   1. FULL (every shard, nothing skipped) for any event other than
#      pull_request, and whenever this script cannot finish (fail open: an
#      error is FULL, never an empty selection). On a pull_request, EVERY
#      package is reached for a change to go.mod, go.sum, go.work*, Makefile,
#      or anything under .github/, scripts/ci/, ci/, internal/testsupport/,
#      contracts/, testdata/ (the repository root's) or clients/; for a Go file
#      in a directory that is no listed package; and for an empty change list.
#   2. A Go file in package P changes P. A non-Go file changes P when it sits
#      under P/.../testdata/, is one of P's //go:embed inputs, or no longer
#      exists (a deleted or renamed-away file under P may have been an embed
#      input or a file P's tests read, and the current tree cannot say).
#   3. A package is reached when its test binary (`go list -test` Deps of
#      P.test, plus P) holds a changed package.
#   4. Reads the build graph cannot see, derived from test source on every run:
#      a package whose test file both names a root outside its directory
#      (repopath, "go.mod", os.Getwd, a ".." literal) and enumerates files
#      (Walk, WalkDir, ReadDir, Glob, ParseDir, DirFS, `go list`) walks the
#      tree, and any change reaches it; and every changed file reaches the
#      packages whose test source names the file's base name as a literal.
#   A test that opens a file by a COMPUTED name outside rules 1-4 is not seen;
#   main pushes run every shard, so such a miss fails there, not silently.
#   6. Race duplicates, pull_request only, and only with --heavy true (the race
#      jobs are scheduled): a non-race `unit` leg whose packages are all
#      unit-dedicated packages (test-shard.sh --print-unit-dedicated-packages)
#      is skipped when every race job that runs those packages is scheduled
#      (not in skip_jobs); those race jobs then cover the same tests under
#      -race. A leg is kept when any test it runs is skipped under -race: a
#      Test func whose body names raceDetectorEnabled, race.Enabled or
#      testing.Short, or one declared in a `//go:build` file excluding race
#      (race_skipped_tests below); the leg's own test set is read from its
#      -run/-skip race-split.sh list in ci.yml. Their job keys go to
#      dedup_needs, and verify requires each of them to succeed. With --heavy
#      false (a draft without ci-full) no race job runs, so no unit leg is
#      skipped this way. scripts/ci/test-unit-race-dedup.sh proves, by
#      compiled test lists, that every dropped leg's tests run in its race
#      jobs.
#
# Output (stdout, GITHUB_OUTPUT lines):
#   reach_mode=full|reached   reached only for a pull_request
#   reach_reason=<one line>
#   skip_jobs=<JSON list>     job keys to skip whole: an isolated race job
#                             whose packages are not reached, plus `unit` /
#                             `race` when every leg of that matrix is skipped
#   unit_skip=<JSON list>     matrix exclude entries {"shard":N} for `unit`
#   race_skip=<JSON list>     the same for `race`
#   dedup_needs=<JSON list>   race job keys a rule-6 skip relies on
# FULL prints [] for all four lists. A report goes to stderr: every changed
# package with the file that changed it, every reached package with the rule
# that reached it, and every shard decision with a reached package it holds.
#
# Usage: reached-shards.sh --event NAME [--heavy true|false] [--changed FILE] [--reached-out FILE] [--workflow FILE]
#   --heavy        the plan job's heavy output; required on pull_request (any
#                  other value fails the selection, which is FULL)
#   --changed      one changed path per line; default: the diff of the pull
#                  request merge commit (HEAD^1..HEAD) actions/checkout makes
#   --reached-out  also write the reached packages, one per line

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
workflow="$repo_root/.github/workflows/ci.yml"
event=""
heavy=""
changed_file=""
reached_out=""

full_files=(go.mod go.sum go.work go.work.sum Makefile)
full_prefixes=(.github/ scripts/ci/ ci/ internal/testsupport/ contracts/ testdata/ clients/)
root_ref='repopath\.Root\(|repopath\.Path\(|"go\.mod"|os\.Getwd\(\)|"\.\."|"\.\./'
enumerates='filepath\.WalkDir\(|filepath\.Walk\(|fs\.WalkDir\(|os\.ReadDir\(|filepath\.Glob\(|parser\.ParseDir\(|os\.DirFS\(|packages\.Load\(|"go", "list"'
computed_root_path='repopath\.Path\(t, [^"]'

usage() {
  printf 'usage: %s --event NAME [--heavy true|false] [--changed FILE] [--reached-out FILE] [--workflow FILE]\n' "${0##*/}" >&2
}

emit() { # mode reason skip_jobs unit_skip race_skip dedup_needs
  printf 'reach_mode=%s\nreach_reason=%s\nskip_jobs=%s\nunit_skip=%s\nrace_skip=%s\ndedup_needs=%s\n' "$1" "$2" "$3" "$4" "$5" "$6"
}

emit_full() {
  printf 'reached-shards: FULL: %s\n' "$1" >&2
  emit full "$1" '[]' '[]' '[]' '[]'
}

job_block() {
  awk -v job="$1" '
    $0 ~ "^  " job ":" { grab=1; print; next }
    grab && /^  [A-Za-z0-9_-]+:/ { grab=0 }
    grab { print }
  ' "$workflow"
}

list_jobs() {
  awk '
    /^jobs:/ { in_jobs=1; next }
    in_jobs && /^[^[:space:]]/ { in_jobs=0 }
    in_jobs && /^  [A-Za-z0-9_-]+:/ { line=$0; sub(/^  /, "", line); sub(/:.*/, "", line); print line }
  ' "$workflow"
}

# race_skipped_tests DIR -> the top-level Test funcs of DIR's test files that
# do not run under -race: the body names raceDetectorEnabled, race.Enabled or
# testing.Short(), or the file's //go:build line excludes race.
race_skipped_tests() {
  local files=("$1"/*_test.go)
  [ -e "${files[0]}" ] || return 0
  awk '
    function flush() { if (name != "" && hit) print name; name = ""; hit = 0 }
    FNR == 1 { flush(); norace = 0 }
    /^\/\/go:build/ && /!race/ { norace = 1 }
    /^func Test[A-Za-z0-9_]*\(/ { flush(); name = $2; sub(/\(.*/, "", name); hit = norace; next }
    /^func / { flush(); next }
    name != "" && /raceDetectorEnabled|race\.Enabled|testing\.Short\(\)/ { hit = 1 }
    END { flush() }
  ' "${files[@]}" | LC_ALL=C sort -u
}

# leg_regime N -> "run|skip <space-separated names>" for unit leg N's
# GOFLAGS -run/-skip race-split.sh list in ci.yml, or "all" when it has none.
leg_regime() {
  local line flag split mode regex
  line="$(job_block unit | grep -E "^ +$1\) export GOFLAGS=" | head -n1 || true)"
  if [ -z "$line" ]; then
    printf 'all\n'
    return 0
  fi
  flag="$(sed -nE 's/.*-trimpath -(run|skip)=\$\(scripts\/ci\/race-split\.sh [a-z]+ [a-z-]+\)".*/\1/p' <<<"$line")"
  split="$(sed -nE 's/.*race-split\.sh ([a-z]+) ([a-z-]+)\)".*/\1/p' <<<"$line")"
  mode="$(sed -nE 's/.*race-split\.sh ([a-z]+) ([a-z-]+)\)".*/\2/p' <<<"$line")"
  if [ -z "$flag" ] || [ -z "$split" ] || [ -z "$mode" ]; then
    printf 'reached-shards: unit leg %s GOFLAGS line not understood: %s\n' "$1" "$line" >&2
    return 1
  fi
  regex="$(cd "$repo_root" && scripts/ci/race-split.sh "$split" "$mode")" || return 1
  case "$regex" in '^('*')$') ;; *) printf 'reached-shards: race-split.sh %s %s printed %s\n' "$split" "$mode" "$regex" >&2; return 1 ;; esac
  regex="${regex#'^('}"
  printf '%s %s\n' "$flag" "${regex%')$'}" | tr '|' ' '
}

has_testdata_dir() { # path: true when a directory component is testdata
  case "/$1" in */testdata/*) return 0 ;; esac
  return 1
}

# go_list ARGS... -> stdout; on failure prints go's stderr and returns 1.
go_list() {
  local out err status=0
  err="$(mktemp)"
  out="$(cd "$repo_root" && go list "$@" 2>"$err")" || status=$?
  if [ "$status" -ne 0 ]; then
    printf 'reached-shards: go list %s failed (exit %s)\n' "$*" "$status" >&2
    cat "$err" >&2
    rm -f "$err"
    return 1
  fi
  rm -f "$err"
  printf '%s\n' "$out"
}

select_shards() {
  local -a changed=()
  local list line every=""
  case "$heavy" in
    true | false) ;;
    *) printf 'reached-shards: --heavy is %s, expected true or false\n' "${heavy:-<unset>}" >&2; return 1 ;;
  esac
  if [ -n "$changed_file" ]; then
    [ -r "$changed_file" ] || { printf 'reached-shards: cannot read %s\n' "$changed_file" >&2; return 1; }
    list="$(cat "$changed_file")"
  else
    git -C "$repo_root" rev-parse --verify -q 'HEAD^2' >/dev/null \
      || { printf 'reached-shards: HEAD is not a pull request merge commit\n' >&2; return 1; }
    list="$(git -C "$repo_root" diff --name-only --no-renames 'HEAD^1' HEAD)"
  fi
  while IFS= read -r line; do
    [ -n "$line" ] && changed+=("$line")
  done <<<"$list"
  [ "${#changed[@]}" -gt 0 ] || every="no changed file was read"

  local f p
  for f in "${changed[@]}"; do
    [ -z "$every" ] || break
    for p in "${full_files[@]}"; do
      [ "$f" = "$p" ] && { every="shared input changed: $f"; break; }
    done
    for p in "${full_prefixes[@]}"; do
      case "$f" in "$p"*) every="shared input changed: $f"; break ;; esac
    done
  done

  local module_path
  module_path="$(awk '/^module / { print $2; exit }' "$repo_root/go.mod")"
  [ -n "$module_path" ] || { printf 'reached-shards: no module path in go.mod\n' >&2; return 1; }

  local listing ip rel
  local -A dir_pkg=() embed=() test_files=() has_tests=() deps_of=()
  listing="$(go_list -f '{{.ImportPath}}' ./...)" || return 1
  while IFS= read -r ip; do
    [ -n "$ip" ] || continue
    case "$ip" in
      "$module_path") rel=. ;;
      "$module_path"/*) rel="${ip#"$module_path"/}" ;;
      *) printf 'reached-shards: go list printed a package outside %s: %s\n' "$module_path" "$ip" >&2; return 1 ;;
    esac
    dir_pkg[$rel]="$ip"
  done <<<"$listing"
  [ "${#dir_pkg[@]}" -gt 0 ] || { printf 'reached-shards: go list printed no package\n' >&2; return 1; }

  local -a fields
  listing="$(go_list -f '{{.ImportPath}}{{range .EmbedFiles}}|{{.}}{{end}}{{range .TestEmbedFiles}}|{{.}}{{end}}{{range .XTestEmbedFiles}}|{{.}}{{end}}' ./...)" || return 1
  while IFS='|' read -r -a fields; do
    [ "${#fields[@]}" -gt 1 ] || continue
    for f in "${fields[@]:1}"; do embed["${fields[0]}|$f"]=1; done
  done <<<"$listing"

  listing="$(go_list -f '{{.ImportPath}}|{{.Dir}}{{range .TestGoFiles}}|{{.}}{{end}}{{range .XTestGoFiles}}|{{.}}{{end}}' ./...)" || return 1
  while IFS='|' read -r -a fields; do
    [ "${#fields[@]}" -gt 2 ] || continue
    for f in "${fields[@]:2}"; do test_files[${fields[0]}]+="${fields[1]}/$f"$'\n'; done
  done <<<"$listing"

  local owner dep deps
  listing="$(go_list -test -f '{{.ImportPath}}{{range .Deps}}|{{.}}{{end}}' ./...)" || return 1
  while IFS='|' read -r -a fields; do
    owner="${fields[0]}"
    case "$owner" in *' '* | *_test) continue ;; *.test) owner="${owner%.test}" ;; *) continue ;; esac
    deps=" $owner "
    for dep in "${fields[@]:1}"; do
      dep="${dep%% \[*}"
      case "$dep" in "$module_path" | "$module_path"/*) deps+="$dep " ;; esac
    done
    deps_of[$owner]="$deps"
    has_tests[$owner]=1
  done <<<"$listing"

  local -A changed_pkg=()
  local d
  for f in "${changed[@]}"; do
    if [[ "$f" == *.go ]] && ! has_testdata_dir "${f%/*}/"; then
      d=.
      [[ "$f" == */* ]] && d="${f%/*}"
      ip="${dir_pkg[$d]:-}"
      if [ -z "$ip" ]; then
        [ -n "$every" ] || every="Go file outside every listed package: $f"
        continue
      fi
      changed_pkg[$ip]=1
      printf 'reached-shards: changed package %s (%s)\n' "$ip" "$f" >&2
      continue
    fi
    d="$f"
    while :; do
      if [[ "$d" == */* ]]; then d="${d%/*}"; else d=.; fi
      ip="${dir_pkg[$d]:-}"
      if [ -n "$ip" ]; then
        if [ "$d" = . ]; then rel="$f"; else rel="${f#"$d"/}"; fi
        if { [[ "$rel" == */* ]] && has_testdata_dir "${rel%/*}/"; } || [ -n "${embed[$ip|$rel]:-}" ] || [ ! -e "$repo_root/$f" ]; then
          changed_pkg[$ip]=1
          printf 'reached-shards: changed package %s (%s)\n' "$ip" "$f" >&2
        fi
        break
      fi
      [ "$d" = . ] && break
    done
  done

  local -A reached=() basis=()
  if [ -n "$every" ]; then
    printf 'reached-shards: every package reached: %s\n' "$every" >&2
    for ip in "${dir_pkg[@]}"; do reached[$ip]=1; basis[$ip]="every package reached: $every"; done
  fi
  for owner in "${!deps_of[@]}"; do
    for ip in "${!changed_pkg[@]}"; do
      case "${deps_of[$owner]}" in
        *" $ip "*)
          reached[$owner]=1
          if [ "$owner" = "$ip" ]; then basis[$owner]="changed"; else basis[$owner]="test binary imports $ip"; fi
          break
          ;;
      esac
    done
  done

  # grep_files GREP-ARGS... -> the test files that match; no match is not an error.
  grep_files() {
    local status=0 out
    out="$(grep -l "$@" -- "${all_test_files[@]}")" || status=$?
    [ "$status" -le 1 ] || { printf 'reached-shards: grep %s failed (exit %s)\n' "$*" "$status" >&2; return 1; }
    printf '%s\n' "$out"
  }

  # One grep per question over every test file, not one per file: the
  # answers map back to packages through file_pkg.
  local tf patterns base walkers=0 literal=0
  local -a all_test_files=()
  local -A file_pkg=() has_root=() has_enum=()
  for ip in "${!test_files[@]}"; do
    [ -n "${has_tests[$ip]:-}" ] || continue
    while IFS= read -r tf; do
      [ -n "$tf" ] || continue
      all_test_files+=("$tf")
      file_pkg[$tf]="$ip"
    done <<<"${test_files[$ip]}"
  done
  [ "${#all_test_files[@]}" -gt 0 ] || { printf 'reached-shards: no test file listed\n' >&2; return 1; }
  local matched
  matched="$(grep_files -E "$root_ref")"
  while IFS= read -r tf; do [ -z "$tf" ] || has_root[$tf]=1; done <<<"$matched"
  matched="$(grep_files -E "$enumerates")"
  while IFS= read -r tf; do [ -z "$tf" ] || has_enum[$tf]=1; done <<<"$matched"
  matched="$(grep_files -E "$computed_root_path")"
  while IFS= read -r tf; do [ -z "$tf" ] || has_root[$tf]=2; done <<<"$matched"
  for tf in "${!has_root[@]}"; do
    if [ "${has_root[$tf]}" = 2 ] || [ -n "${has_enum[$tf]:-}" ]; then
      ip="${file_pkg[$tf]}"
      [ -n "${reached[$ip]:-}" ] || { walkers=$((walkers + 1)); basis[$ip]="tests walk the module tree (${tf#"$repo_root"/})"; }
      reached[$ip]=1
    fi
  done

  patterns="$(mktemp)"
  for f in "${changed[@]}"; do
    base="${f##*/}"
    printf '"%s"\n/%s"\n' "$base" "$base" >>"$patterns"
  done
  matched="$(grep_files -F -f "$patterns")"
  while IFS= read -r tf; do
    [ -n "$tf" ] || continue
    ip="${file_pkg[$tf]}"
    [ -n "${reached[$ip]:-}" ] || { literal=$((literal + 1)); basis[$ip]="test source names a changed file (${tf#"$repo_root"/})"; }
    reached[$ip]=1
  done <<<"$matched"
  rm -f "$patterns"

  if [ -n "$reached_out" ]; then
    printf '%s\n' "${!reached[@]}" | LC_ALL=C sort >"$reached_out"
  fi
  for ip in "${!reached[@]}"; do
    printf 'reached-shards: reached %s (%s)\n' "$ip" "${basis[$ip]:-}"
  done | LC_ALL=C sort >&2
  printf 'reached-shards: %s changed file(s), %s changed package(s), %s reached package(s) (%s by tree walk, %s by a literal file name)\n' \
    "${#changed[@]}" "${#changed_pkg[@]}" "${#reached[@]}" "$walkers" "$literal" >&2

  # holds_reached PACKAGE... -> true when any argument is reached; sets
  # held to the first reached one for the decision log.
  local held=""
  holds_reached() {
    local pkg
    for pkg in "$@"; do [ -n "${reached[$pkg]:-}" ] && { held="$pkg"; return 0; }; done
    return 1
  }

  # Every shard's package list comes from the job's own test-shard.sh line.
  # The calls run in parallel (each one runs `go list ./...`), and each
  # call's exit status is checked: a failed listing fails the selection.
  local job block shards call n subcmd key legdir
  local -a keys=() pids=()
  local -A args_of=() matrix_legs=()
  for job in unit race; do
    block="$(job_block "$job")"
    shards="$(grep -E '^ {8}shard: *\[' <<<"$block" | head -n1 | sed -E 's/.*\[([^]]*)\].*/\1/' | tr ',' ' ')"
    call="$(grep -oE 'scripts/ci/test-shard\.sh [^)]*' <<<"$block" | head -n1)"
    if [ -z "$shards" ] || [ -z "$call" ]; then
      printf 'reached-shards: %s has no shard matrix or test-shard.sh call\n' "$job" >&2
      return 1
    fi
    call="${call#scripts/ci/test-shard.sh }"
    for n in $shards; do
      keys+=("$job:$n")
      # shellcheck disable=SC2016 # the literal GitHub expression text
      args_of[$job:$n]="${call//'${{ matrix.shard }}'/$n}"
      matrix_legs[$job]+="$n "
    done
  done
  local all_jobs
  all_jobs="$(list_jobs)"
  [ -n "$all_jobs" ] || { printf 'reached-shards: %s lists no job\n' "$workflow" >&2; return 1; }
  while IFS= read -r job; do
    block="$(job_block "$job")"
    subcmd="$(grep -oE 'test-shard\.sh[[:space:]]+isolated(-[a-z]+)?([[:space:]]+[a-z0-9]+)?' <<<"$block" | head -n1 | awk '{$1=""; sub(/^ /,""); print}' || true)"
    [ -n "$subcmd" ] || continue
    keys+=("$job")
    args_of[$job]="$subcmd"
  done <<<"$all_jobs"

  legdir="$(mktemp -d)"
  for key in "${keys[@]}"; do
    # shellcheck disable=SC2086 # the job's own argument list, split on purpose
    (cd "$repo_root" && scripts/ci/test-shard.sh ${args_of[$key]}) >"$legdir/${key//:/.}" 2>"$legdir/${key//:/.}.err" &
    pids+=("$!")
  done
  local i failed=0
  for i in "${!pids[@]}"; do
    if ! wait "${pids[$i]}"; then
      printf 'reached-shards: test-shard.sh %s failed for %s\n' "${args_of[${keys[$i]}]}" "${keys[$i]}" >&2
      cat "$legdir/${keys[$i]//:/.}.err" >&2
      failed=1
    fi
  done
  [ "$failed" -eq 0 ] || { rm -rf "$legdir"; return 1; }

  local skip_jobs="" unit_skip="" race_skip="" skips all_skipped
  local -a pkgs
  for key in "${keys[@]}"; do
    read -r -a pkgs <"$legdir/${key//:/.}" || true
    [ "${#pkgs[@]}" -gt 0 ] || { printf 'reached-shards: %s selects no package\n' "$key" >&2; rm -rf "$legdir"; return 1; }
    if holds_reached "${pkgs[@]}"; then
      printf 'reached-shards: run  %s (holds %s)\n' "$key" "$held" >&2
      continue
    fi
    printf 'reached-shards: skip %s (holds no reached package)\n' "$key" >&2
    case "$key" in
      unit:*) unit_skip+="${unit_skip:+,}{\"shard\":${key#unit:}}" ;;
      race:*) race_skip+="${race_skip:+,}{\"shard\":${key#race:}}" ;;
      *) skip_jobs+="${skip_jobs:+,}\"$key\"" ;;
    esac
  done
  local dedicated dedup_needs="" pkg cover covers ok
  if [ "$heavy" = true ]; then
    dedicated=" $(cd "$repo_root" && scripts/ci/test-shard.sh --print-unit-dedicated-packages) "
    [ "$dedicated" != "  " ] || { printf 'reached-shards: no unit-dedicated package listed\n' >&2; rm -rf "$legdir"; return 1; }
    for key in "${keys[@]}"; do
      case "$key" in unit:*) ;; *) continue ;; esac
      case ",$unit_skip," in *",{\"shard\":${key#unit:}},"*) continue ;; esac
      read -r -a pkgs <"$legdir/${key//:/.}" || true
      ok=1
      covers=""
      for pkg in "${pkgs[@]}"; do
        case "$dedicated" in *" $pkg "*) ;; *) ok=0; break ;; esac
        cover=0
        for job in "${keys[@]}"; do
          case "$job" in unit:* | race:*) continue ;; esac
          case " $(cat "$legdir/$job") " in *" $pkg "*) ;; *) continue ;; esac
          cover=1
          case ",$skip_jobs," in *",\"$job\","*) ok=0 ;; esac
          covers+=" $job"
        done
        [ "$cover" -eq 1 ] || ok=0
      done
      [ "$ok" -eq 1 ] || continue
      local regime names skipped t
      regime="$(leg_regime "${key#unit:}")" || { rm -rf "$legdir"; return 1; }
      names=" ${regime#* } "
      for pkg in "${pkgs[@]}"; do
        skipped="$(race_skipped_tests "$repo_root/${pkg#"$module_path"/}")"
        for t in $skipped; do
          case "$regime" in
            all) ok=0 ;;
            run\ *) case "$names" in *" $t "*) ok=0 ;; esac ;;
            skip\ *) case "$names" in *" $t "*) ;; *) ok=0 ;; esac ;;
          esac
          [ "$ok" -eq 1 ] || { printf 'reached-shards: keep %s: %s skips under -race\n' "$key" "$t" >&2; break; }
        done
        [ "$ok" -eq 1 ] || break
      done
      [ "$ok" -eq 1 ] || continue
      printf 'reached-shards: skip %s (race duplicate of%s)\n' "$key" "$covers" >&2
      unit_skip+="${unit_skip:+,}{\"shard\":${key#unit:}}"
      for job in $covers; do
        case ",$dedup_needs," in *",\"$job\","*) ;; *) dedup_needs+="${dedup_needs:+,}\"$job\"" ;; esac
      done
    done
  fi
  rm -rf "$legdir"
  for job in unit race; do
    case "$job" in unit) skips="$unit_skip" ;; race) skips="$race_skip" ;; esac
    all_skipped=1
    for n in ${matrix_legs[$job]}; do
      case ",$skips," in *",{\"shard\":$n},"*) ;; *) all_skipped=0 ;; esac
    done
    [ "$all_skipped" -eq 0 ] || skip_jobs="\"$job\"${skip_jobs:+,}$skip_jobs"
  done

  emit reached "${every:-${#changed_pkg[@]} changed package(s) reach ${#reached[@]} package(s)}" \
    "[$skip_jobs]" "[$unit_skip]" "[$race_skip]" "[$dedup_needs]"
}

main() {
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --event) event="${2-}"; shift 2 ;;
      --heavy) heavy="${2-}"; shift 2 ;;
      --changed) changed_file="${2-}"; shift 2 ;;
      --reached-out) reached_out="${2-}"; shift 2 ;;
      --workflow) workflow="${2-}"; shift 2 ;;
      *) usage; exit 2 ;;
    esac
  done
  [ -n "$event" ] || { usage; exit 2; }
  [ -z "$reached_out" ] || : >"$reached_out"

  if [ "$event" != pull_request ]; then
    emit_full "event $event runs every shard"
    return 0
  fi

  local out status
  out="$(mktemp)"
  set +e
  (
    set -e
    select_shards >"$out"
  )
  status=$?
  set -e
  if [ "$status" -ne 0 ] || ! grep -q '^reach_mode=' "$out"; then
    rm -f "$out"
    [ -z "$reached_out" ] || : >"$reached_out"
    emit_full "selection failed (exit $status); every shard runs"
    return 0
  fi
  cat "$out"
  rm -f "$out"
}

main "$@"
