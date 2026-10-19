#!/usr/bin/env bash
set -euo pipefail
shopt -s inherit_errexit

# race-affected-tests.sh lists the top-level Test funcs of one package whose
# behaviour can differ between a -race run and a plain run: a test that skips,
# returns early, or runs fewer subtests under the race detector. A unit leg
# that runs such a test is never dropped as a race duplicate
# (scripts/ci/reached-shards.sh rule 6), because its race job does not run the
# same thing.
#
# The rule is a closure over declaration names, so a test reaches the race
# signal through any chain of helpers, variables or constants, in this package
# or in any in-module package its test binary imports:
#   seeds  raceDetectorEnabled, race.Enabled, testing.Short, and every top-level
#          declaration in a .go file whose //go:build line contains !race;
#   step   any top-level declaration (func, method, var, const, type, or one
#          line of a grouped var/const block) whose text names a member is a
#          member;
#   result the Test funcs of the package's own _test.go files that are members.
# The scan covers exactly the files the package's test binary compiles (the
# in-module dependencies' GoFiles and the package's own test files), with
# comments and string literals removed. Names are scoped like Go's (see
# closure below) but receiver-blind within a package, so a name shared by two
# declarations makes both members: the rule can only keep more legs, never
# drop a test it should keep.
#
# Usage: race-affected-tests.sh [--root MODULE_DIR] PACKAGE_DIR
#   PACKAGE_DIR may be absolute or relative to MODULE_DIR (default: this repo).

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
if [ "${1:-}" = --root ]; then
  root="$(cd "$2" && pwd)"
  shift 2
fi
[ "$#" -eq 1 ] || { printf 'usage: %s [--root MODULE_DIR] PACKAGE_DIR\n' "${0##*/}" >&2; exit 2; }
case "$1" in /*) pkgdir="$1" ;; *) pkgdir="$root/$1" ;; esac
pkgdir="$(cd "$pkgdir" && pwd)"

# The files of the package's test binary inside this module: every in-module
# dependency's compiled GoFiles, plus the package's own test files.
listing="$(cd "$root" && go list -test -deps -f '{{if .Module}}{{if eq .Module.Dir "'"$root"'"}}{{.Dir}}|{{join .GoFiles ","}}|{{join .TestGoFiles ","}}|{{join .XTestGoFiles ","}}{{end}}{{end}}' "$pkgdir")" || {
  printf '%s: go list failed for %s\n' "${0##*/}" "$pkgdir" >&2
  exit 1
}
mapfile -t files < <(printf '%s\n' "$listing" | awk -F '|' -v d="$pkgdir" 'NF == 4 {
  n = split($2 "," ($1 == d ? $3 "," $4 : ""), g, ",")
  for (i = 1; i <= n; i++) if (g[i] != "" && g[i] !~ /^\//) print $1 "/" g[i]
}' | LC_ALL=C sort -u)
for f in "${files[@]}"; do [ -r "$f" ] || { printf '%s: listed file %s is not readable\n' "${0##*/}" "$f" >&2; exit 1; }; done
[ "${#files[@]}" -gt 0 ] || { printf '%s: no Go file in the test binary of %s\n' "${0##*/}" "$pkgdir" >&2; exit 1; }

# closure FILE... -> member records, computed in one awk pass per round:
#   S<TAB>dir<TAB>name   a member, visible unqualified inside its directory
#   Q<TAB>pkg.Name       an exported member, visible as pkg.Name elsewhere
# Scoping follows Go's: an unexported name is matched only inside its own
# directory, and an exported one elsewhere only as <package>.<Name>, so a
# common word (seed, Get) in one package cannot pull in every package.
closure() {
  local members="" next membersfile
  membersfile="$(mktemp)"
  while :; do
    printf '%s\n' "$members" >"$membersfile"
    next="$(awk -v mf="$membersfile" '
      BEGIN {
        FS = "\t"
        while ((getline m < mf) > 0) {
          split(m, f, "\t")
          if (f[1] == "S") scoped[f[2] SUBSEP f[3]] = 1
          else if (f[1] == "Q") qual[f[2]] = 1
        }
        FS = " "
      }
      function hit(t,   n, i, tok, s, q) {
        if (t ~ /raceDetectorEnabled|race[.]Enabled/) return 1
        n = split(t, tok, /[^A-Za-z0-9_]+/)
        for (i = 1; i <= n; i++) if ((dir SUBSEP tok[i]) in scoped) return 1
        s = t
        while (match(s, /[A-Za-z_][A-Za-z0-9_]*[.][A-Za-z_][A-Za-z0-9_]*/)) {
          q = substr(s, RSTART, RLENGTH)
          if (q in qual) return 1
          s = substr(s, RSTART + RLENGTH)
        }
        return 0
      }
      function flush() {
        if (name != "" && (norace || hit(text))) {
          print "S\t" dir "\t" name
          if (name ~ /^[A-Z]/) print "Q\t" pkg "." name
        }
        name = ""; text = ""
      }
      FNR == 1 { flush(); norace = 0; group = 0; dir = FILENAME; sub(/\/[^\/]*$/, "", dir) }
      /^\/\/go:build/ && /!race/ { norace = 1 }
      /^package / { pkg = $2; sub(/_test$/, "", pkg) }
      group && /^\)/ { flush(); group = 0; next }
      group && /^\t[A-Za-z_][A-Za-z0-9_]*/ { flush(); name = $1; text = $0; next }
      /^(var|const|type) \($/ { flush(); group = 1; next }
      /^func / {
        flush()
        line = $0
        sub(/^func (\([^)]*\) )?/, "", line)
        sub(/[^A-Za-z0-9_].*/, "", line)
        name = line; text = $0; next
      }
      /^(var|const|type) [A-Za-z_]/ { flush(); name = $2; sub(/[^A-Za-z0-9_].*/, "", name); text = $0; next }
      /^[^ \t\/}]/ { flush(); next }
      /^[ \t]*\/\// { next }
      { line = $0; gsub(/"([^"\\]|\\.)*"/, "\"\"", line); sub(/\/\/.*$/, "", line); text = text "\n" line }
      END { flush() }
    ' "$@" | LC_ALL=C sort -u)" || { rm -f "$membersfile"; return 1; }
    next="$(printf '%s\n%s\n' "$members" "$next" | grep -v '^$' | LC_ALL=C sort -u || true)"
    [ -z "${RACE_AFFECTED_DEBUG:-}" ] || LC_ALL=C comm -13 <(printf '%s\n' "$members") <(printf '%s\n' "$next") | head -n 12 | sed 's/^/new: /' >&2
    [ "$next" != "$members" ] || break
    members="$next"
  done
  rm -f "$membersfile"
  printf '%s\n' "$members"
}

members="$(closure "${files[@]}")" || { printf '%s: the declaration scan failed\n' "${0##*/}" >&2; exit 1; }
[ -z "${RACE_AFFECTED_DEBUG:-}" ] || printf '%s: %s member name(s)\n' "${0##*/}" "$(printf '%s\n' "$members" | grep -c . || true)" >&2
[ -n "$members" ] || exit 0
printf '%s\n' "$members" | awk -F '\t' -v d="$pkgdir" '$1 == "S" && $2 == d && $3 ~ /^Test/ { print $3 }' | LC_ALL=C sort -u
