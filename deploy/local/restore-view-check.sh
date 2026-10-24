#!/usr/bin/env bash
# Sourced by trial-data.sh. Post-restore check: every view in a ClickHouse
# backup must exist after RESTORE and carry the same SELECT body (database
# qualifiers normalized). Fails closed: a missing view or an unparseable
# backup metadata file is a failure, never a skip.

# rvc_body <src_db> <dst_db>: stdin = CREATE/ATTACH VIEW text; stdout = the
# view kind plus everything after the view name (column list, TO target,
# ENGINE, SELECT), without the UUID clause and with database qualifiers
# stripped outside quotes. rc 1 = not a view definition.
rvc_body() {
  local src="$1" dst="$2"
  tr '\n\t' '  ' | awk -v src="$src" -v dst="$dst" '
    function isid(c) { return c ~ /[A-Za-z0-9_]/ }
    function strip(body,   out, i, c, q, n, k, d, prev, bq) {
      out = ""; q = ""; i = 1; n = length(body)
      while (i <= n) {
        c = substr(body, i, 1)
        if (q != "") { out = out c; if (c == q) q = ""; i++; continue }
        if (c == "\047" || c == "\"") { q = c; out = out c; i++; continue }
        prev = (i > 1) ? substr(body, i - 1, 1) : " "
        if (c == "`") {
          for (k = 1; k <= 2; k++) {
            d = (k == 1) ? src : dst
            if (d != "" && substr(body, i, length(d) + 3) == "`" d "`.") { i += length(d) + 3; c = ""; break }
          }
          if (c == "") continue
          q = c; out = out c; i++; continue
        }
        if (!isid(prev)) {
          for (k = 1; k <= 2; k++) {
            d = (k == 1) ? src : dst
            if (d != "" && substr(body, i, length(d) + 1) == d ".") { i += length(d) + 1; c = ""; break }
          }
          if (c == "") continue
        }
        if (c == " " && (substr(out, length(out), 1) == "(" || substr(body, i + 1, 1) == ")")) { i++; continue }
        out = out c; i++
      }
      return out
    }
    {
      s = $0
      gsub(/[ ]+/, " ", s)
      if (!match(s, /^ ?(ATTACH|CREATE)( OR REPLACE)?( MATERIALIZED| LIVE| WINDOW)? VIEW( IF NOT EXISTS)? /)) exit 1
      head = substr(s, 1, RLENGTH); rest = substr(s, RLENGTH + 1)
      kind = ""
      if (head ~ / MATERIALIZED /) kind = "MATERIALIZED "
      else if (head ~ / LIVE /) kind = "LIVE "
      else if (head ~ / WINDOW /) kind = "WINDOW "
      if (substr(rest, 1, 1) == "`") {
        e = index(substr(rest, 2), "`"); if (e == 0) exit 1
        rest = substr(rest, e + 2)
      } else {
        e = index(rest, " "); if (e == 0) exit 1
        rest = substr(rest, e)
      }
      sub(/^ UUID \047[^\047]*\047/, "", rest)
      gsub(/^ +| +$/, "", rest)
      if (rest !~ / AS (SELECT|WITH|\()/) exit 1
      print kind "VIEW " strip(rest)
    }'
  return "${PIPESTATUS[1]}"
}

# rvc_unescape_name: ClickHouse escapes names in metadata file names (%XX).
rvc_unescape_name() {
  printf '%b' "$(printf '%s' "$1" | sed -E 's/%([0-9A-Fa-f]{2})/\\x\1/g')"
}

# rvc_escape_name: the inverse for the characters a database name may carry.
rvc_escape_name() {
  printf '%s' "$1" | sed -e 's/-/%2D/g' -e 's/\./%2E/g'
}

# rvc_restored_create_query <db> <view>: prints the server's create_query.
# trial-data.sh overrides this to query the pod; tests override it too.
rvc_restored_create_query() { return 1; }

# rvc_check_views <metadata_dir> <src_db> <dst_db>
# metadata_dir holds the backup's <view>.sql files for src_db. rc 0 = every
# view matches; rc 1 = prints one line per failing view name.
rvc_check_views() {
  local dir="$1" src="$2" dst="$3" f name want got bad=0 seen=0
  [[ -d "$dir" ]] || { echo "rvc: no metadata dir $dir" >&2; return 1; }
  for f in "$dir"/*.sql; do
    [[ -e "$f" ]] || continue
    if [[ ! -s "$f" ]]; then
      echo "UNPARSEABLE backup metadata: $(basename "$f") (empty)"; bad=1; continue
    fi
    if ! grep -Eq '^(ATTACH|CREATE)( OR REPLACE)?( MATERIALIZED| LIVE| WINDOW)? VIEW ' "$f"; then
      grep -Eqi '(^|[^A-Za-z])VIEW[ ]' "$f" && { echo "UNPARSEABLE backup metadata: $(basename "$f")"; bad=1; }
      continue
    fi
    name="$(rvc_unescape_name "$(basename "$f" .sql)")"
    seen=$((seen + 1))
    if ! want="$(rvc_body "$src" "$dst" <"$f")" || [[ -z "$want" ]]; then
      echo "UNPARSEABLE backup metadata: $name"; bad=1; continue
    fi
    if ! got="$(rvc_restored_create_query "$dst" "$name")" || [[ -z "$got" ]]; then
      echo "MISSING after restore: $name"; bad=1; continue
    fi
    if ! got="$(printf '%s' "$got" | rvc_body "$src" "$dst")" || [[ -z "$got" ]]; then
      echo "UNPARSEABLE restored definition: $name"; bad=1; continue
    fi
    [[ "$want" == "$got" ]] || { echo "DEFINITION DIFFERS: $name"; bad=1; }
  done
  return "$bad"
}
