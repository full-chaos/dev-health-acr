#!/usr/bin/env bash
# Sourced by trial-data.sh. Post-restore check: every view in a ClickHouse
# backup must exist after RESTORE and carry the same SELECT body (database
# qualifiers normalized). Fails closed: a missing view or an unparseable
# backup metadata file is a failure, never a skip.

# rvc_body <src_db> <dst_db>: stdin = CREATE/ATTACH VIEW text; stdout = the
# normalized body after the view header. rc 1 = no header/body found.
rvc_body() {
  local src="$1" dst="$2"
  tr '\n\t' '  ' | awk -v src="$src" -v dst="$dst" '
    {
      s = $0
      gsub(/[ ]+/, " ", s)
      if (s !~ /^ ?(ATTACH|CREATE)( OR REPLACE)?( MATERIALIZED| LIVE| WINDOW)? VIEW /) exit 1
      depth = 0; q = ""; cut = 0
      for (i = 1; i <= length(s); i++) {
        c = substr(s, i, 1)
        if (q != "") { if (c == q) q = ""; continue }
        if (c == "\047" || c == "`" || c == "\"") { q = c; continue }
        if (c == "(") depth++
        else if (c == ")") depth--
        else if (depth == 0 && substr(s, i, 4) == " AS ") { cut = i + 4; break }
      }
      if (cut == 0) exit 1
      body = substr(s, cut)
      n = split(src "|" dst, dbs, "|")
      for (k = 1; k <= n; k++) {
        if (dbs[k] == "") continue
        gsub("`" dbs[k] "`[.]", "", body)
        out = ""; rest = body; pat = dbs[k] "."
        while ((p = index(rest, pat)) > 0) {
          prev = (p > 1) ? substr(rest, p - 1, 1) : " "
          if (prev ~ /[A-Za-z0-9_]/) { out = out substr(rest, 1, p + length(pat) - 1); rest = substr(rest, p + length(pat)) }
          else { out = out substr(rest, 1, p - 1); rest = substr(rest, p + length(pat)) }
        }
        body = out rest
      }
      gsub(/^ +| +$/, "", body)
      print body
    }'
  return "${PIPESTATUS[1]}"
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
    if ! grep -Eq '^(ATTACH|CREATE)( OR REPLACE)?( MATERIALIZED| LIVE| WINDOW)? VIEW ' "$f"; then
      grep -Eqi '(^|[^A-Za-z])VIEW[ ]' "$f" && { echo "UNPARSEABLE backup metadata: $(basename "$f")"; bad=1; }
      continue
    fi
    name="$(basename "$f" .sql)"
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
