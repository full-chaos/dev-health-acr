#!/usr/bin/env bash
set -euo pipefail
shopt -s inherit_errexit

# race-affected-tests.sh lists the top-level Test funcs of one package whose
# behaviour can differ between a -race run and a plain run: a test that is not
# compiled, skips, returns early, or runs fewer subtests under the race
# detector. A unit leg that runs such a test is never dropped as a race
# duplicate (scripts/ci/reached-shards.sh rule 6), because its race job does
# not run the same thing.
#
# Go code can observe the race build only through files whose inclusion
# differs between the two builds (any build constraint form: //go:build,
# // +build, file-name suffixes), or through the build info the binary
# carries. So the rule is:
#   seeds  every top-level declaration in a file that `go list -test -deps`
#          includes in exactly one of the plain and the -race builds of the
#          package's test binary (the go tool evaluates the constraints), and
#          every declaration that calls debug.ReadBuildInfo;
#   step   any top-level declaration (func, method, var, const, type, or one
#          line of a grouped var/const block) whose text refers to a member is
#          a member. References resolve the way Go resolves them: an
#          unqualified name to its own package (and to dot-imported
#          packages), a qualified name x.Name through the file's own import of
#          x, whatever alias it uses; and x.Name on a value, which may be a
#          method of any package, to every exported method member of that
#          name (receiver-blind, so the rule can only keep more legs);
#   result the Test funcs of the package's own test files that are members.
# The scan covers exactly the files either build compiles (every in-module
# dependency's GoFiles and the package's own test files), with comments and
# string literals removed. A scan that cannot list or read its input fails; it
# never prints an empty set.
#
# Usage: race-affected-tests.sh [--root MODULE_DIR] [--seeds-out FILE] PACKAGE_DIR
#   PACKAGE_DIR may be absolute or relative to MODULE_DIR (default: this repo).
#   --seeds-out  also write the seed files (module-relative), one per line.

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
seeds_out=""
while [ "$#" -gt 1 ]; do
  case "$1" in
    --root) root="$(cd "$2" && pwd)"; shift 2 ;;
    --seeds-out) seeds_out="$2"; shift 2 ;;
    *) break ;;
  esac
done
[ "$#" -eq 1 ] || { printf 'usage: %s [--root MODULE_DIR] [--seeds-out FILE] PACKAGE_DIR\n' "${0##*/}" >&2; exit 2; }
case "$1" in /*) pkgdir="$1" ;; *) pkgdir="$root/$1" ;; esac
pkgdir="$(cd "$pkgdir" && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# list_build [-race] -> ImportPath|Name|Dir|GoFiles|TestGoFiles|XTestGoFiles
# for every in-module package of the test binary in that build.
list_build() {
  (cd "$root" && go list "$@" -test -deps -f '{{if .Module}}{{if eq .Module.Dir "'"$root"'"}}{{.ImportPath}}|{{.Name}}|{{.Dir}}|{{join .GoFiles ","}}|{{join .TestGoFiles ","}}|{{join .XTestGoFiles ","}}{{end}}{{end}}' "$pkgdir")
}
build_files() { # LISTING -> the compiled files, one path per line
  awk -F '|' -v d="$pkgdir" 'NF == 6 {
    n = split($4 "," ($3 == d ? $5 "," $6 : ""), g, ",")
    for (i = 1; i <= n; i++) if (g[i] != "" && g[i] !~ /^\//) print $3 "/" g[i]
  }' <<<"$1" | LC_ALL=C sort -u
}
plain="$(list_build)" || { printf '%s: go list failed for %s\n' "${0##*/}" "$pkgdir" >&2; exit 1; }
raced="$(list_build -race)" || { printf '%s: go list -race failed for %s\n' "${0##*/}" "$pkgdir" >&2; exit 1; }
build_files "$plain" >"$work/plain.files"
build_files "$raced" >"$work/race.files"
LC_ALL=C comm -3 "$work/plain.files" "$work/race.files" | tr -d '\t' | grep -v '^$' >"$work/seed.files" || true
LC_ALL=C sort -u "$work/plain.files" "$work/race.files" >"$work/all.files"
[ -z "$seeds_out" ] || sed "s#^$root/##" "$work/seed.files" >"$seeds_out"
mapfile -t files <"$work/all.files"
[ "${#files[@]}" -gt 0 ] || { printf '%s: no Go file in the test binary of %s\n' "${0##*/}" "$pkgdir" >&2; exit 1; }
for f in "${files[@]}"; do [ -r "$f" ] || { printf '%s: listed file %s is not readable\n' "${0##*/}" "$f" >&2; exit 1; }; done
# dir<TAB>import path<TAB>package name, for resolving imports.
printf '%s\n%s\n' "$plain" "$raced" | awk -F '|' 'NF == 6 { split($1, p, " "); print $3 "\t" p[1] "\t" $2 }' | LC_ALL=C sort -u >"$work/pkgs"

# closure -> member records, computed in one awk pass per round:
#   S<TAB>dir<TAB>name    a member, visible unqualified inside its directory
#   Q<TAB>path<TAB>name   an exported member, visible through an import of path
#   M<TAB>name            an exported method member, matched receiver-blind
closure() {
  local members="" next
  while :; do
    printf '%s\n' "$members" >"$work/members"
    next="$(awk -v mf="$work/members" -v pf="$work/pkgs" -v sf="$work/seed.files" '
      BEGIN {
        while ((getline m < mf) > 0) {
          split(m, f, "\t")
          if (f[1] == "S") scoped[f[2] SUBSEP f[3]] = 1
          else if (f[1] == "Q") qual[f[2] SUBSEP f[3]] = 1
          else if (f[1] == "M") meth[f[2]] = 1
        }
        while ((getline m < pf) > 0) { split(m, f, "\t"); pathof[f[1]] = f[2]; nameof[f[2]] = f[3] }
        while ((getline m < sf) > 0) seedfile[m] = 1
      }
      function addimport(spec,   n, w, path, al) {
        n = split(spec, w, /[ \t]+/)
        if (n == 1) { path = w[1]; al = "" } else { al = w[1]; path = w[2] }
        gsub(/"/, "", path)
        if (path == "") return
        if (al == "_") return
        if (al == ".") { dot[path] = 1; return }
        if (al == "") { if (path in nameof) al = nameof[path]; else { al = path; sub(/^.*\//, "", al) } }
        alias[al] = path
      }
      function hit(t,   n, i, tok, s, q, x, y, p) {
        if (t ~ /ReadBuildInfo/) return 1
        n = split(t, tok, /[^A-Za-z0-9_]+/)
        for (i = 1; i <= n; i++) {
          if ((dir SUBSEP tok[i]) in scoped) return 1
          for (p in dot) if ((p SUBSEP tok[i]) in qual) return 1
        }
        s = t
        while (match(s, /[A-Za-z_][A-Za-z0-9_]*[.][A-Za-z_][A-Za-z0-9_]*/)) {
          q = substr(s, RSTART, RLENGTH)
          s = substr(s, RSTART + RLENGTH)
          x = q; sub(/[.].*/, "", x); y = q; sub(/^[^.]*[.]/, "", y)
          if (x in alias) { if ((alias[x] SUBSEP y) in qual) return 1 }
          else if (y in meth) return 1
        }
        return 0
      }
      function flush() {
        if (name != "" && (seed || hit(text))) {
          print "S\t" dir "\t" name
          if (name ~ /^[A-Z]/) {
            if (method) print "M\t" name
            else print "Q\t" mypath "\t" name
          }
        }
        name = ""; text = ""; method = 0
      }
      FNR == 1 {
        flush(); group = 0; importing = 0
        dir = FILENAME; sub(/\/[^\/]*$/, "", dir); mypath = pathof[dir]
        seed = (FILENAME in seedfile)
        split("", alias); split("", dot)
      }
      importing && /^\)/ { importing = 0; next }
      importing { line = $0; sub(/\/\/.*$/, "", line); gsub(/^[ \t]+|[ \t]+$/, "", line); if (line != "") addimport(line); next }
      /^import \($/ { flush(); importing = 1; next }
      /^import / { flush(); line = $0; sub(/^import[ \t]+/, "", line); sub(/\/\/.*$/, "", line); addimport(line); next }
      group && /^\)/ { flush(); group = 0; next }
      group && /^\t[A-Za-z_][A-Za-z0-9_]*/ { flush(); name = $1; text = $0; next }
      /^(var|const|type) \($/ { flush(); group = 1; next }
      /^func / {
        flush()
        method = ($0 ~ /^func \(/)
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
    ' "${files[@]}" | LC_ALL=C sort -u)" || return 1
    next="$(printf '%s\n%s\n' "$members" "$next" | grep -v '^$' | LC_ALL=C sort -u || true)"
    [ "$next" != "$members" ] || break
    members="$next"
  done
  printf '%s\n' "$members"
}

members="$(closure)" || { printf '%s: the declaration scan failed\n' "${0##*/}" >&2; exit 1; }
[ -z "${RACE_AFFECTED_DEBUG:-}" ] || {
  printf '%s: %s seed file(s), %s member record(s)\n' "${0##*/}" "$(grep -c . "$work/seed.files" || true)" "$(printf '%s\n' "$members" | grep -c . || true)" >&2
  sed 's/^/seed file: /' "$work/seed.files" >&2
}
[ -n "$members" ] || exit 0
printf '%s\n' "$members" | awk -F '\t' -v d="$pkgdir" '$1 == "S" && $2 == d && $3 ~ /^Test/ { print $3 }' | LC_ALL=C sort -u
