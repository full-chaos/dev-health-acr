#!/usr/bin/env bash
# Hermetic checks for restore-view-check.sh: no cluster, no ClickHouse.
set -euo pipefail
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
# shellcheck source=restore-view-check.sh
source "$script_dir/restore-view-check.sh"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
failures=0
expect() { # name want_rc got_rc [output-needle output]
  local name="$1" want="$2" got="$3" needle="${4:-}" out="${5:-}"
  if [[ "$want" != "$got" ]] || { [[ -n "$needle" ]] && [[ "$out" != *"$needle"* ]]; }; then
    echo "FAIL: $name (rc want $want got $got; needle '$needle'; out '$out')" >&2
    failures=$((failures + 1))
  else
    echo "ok: $name"
  fi
}

mkdir -p "$tmp/meta"
cat >"$tmp/meta/project_membership_presence.sql" <<'SQL'
ATTACH VIEW _ UUID '11111111-2222-3333-4444-555555555555'
(
    `org_id` String,
    `source` String
)
AS WITH touched AS (SELECT org_id FROM default.project_membership_transitions FINAL)
SELECT w.org_id AS org_id, 'work_item_column' AS source
FROM default.work_items AS w FINAL
WHERE (w.project_id != '')
SQL
cat >"$tmp/meta/plain_view.sql" <<'SQL'
CREATE VIEW default.plain_view (`n` UInt8) AS SELECT 1 AS n FROM default.t
SQL

GOOD_PRESENCE="CREATE VIEW dh_1022.project_membership_presence (\`org_id\` String, \`source\` String) AS WITH touched AS (SELECT org_id FROM dh_1022.project_membership_transitions FINAL) SELECT w.org_id AS org_id, 'work_item_column' AS source FROM dh_1022.work_items AS w FINAL WHERE (w.project_id != '')"
LOST_ALIAS="CREATE VIEW dh_1022.project_membership_presence (\`org_id\` String, \`source\` String) AS WITH touched AS (SELECT org_id FROM dh_1022.project_membership_transitions FINAL) SELECT w.org_id AS org_id, 'work_item_column' AS source FROM dh_1022.work_items FINAL WHERE (w.project_id != '')"
GOOD_PLAIN="CREATE VIEW dh_1022.plain_view (\`n\` UInt8) AS SELECT 1 AS n FROM dh_1022.t"

restored="$GOOD_PRESENCE"
rvc_restored_create_query() {
  case "$2" in
    project_membership_presence) printf '%s' "$restored" ;;
    plain_view) [[ -n "${plain_missing:-}" ]] && return 1; printf '%s' "$GOOD_PLAIN" ;;
    *) return 1 ;;
  esac
}

rc=0; out="$(rvc_check_views "$tmp/meta" default dh_1022)" || rc=$?
expect "renamed restore with identical definitions passes" 0 "$rc"

restored="$LOST_ALIAS"
rc=0; out="$(rvc_check_views "$tmp/meta" default dh_1022)" || rc=$?
expect "lost AS w alias is reported" 1 "$rc" "DEFINITION DIFFERS: project_membership_presence" "$out"

restored="$GOOD_PRESENCE"; plain_missing=1
rc=0; out="$(rvc_check_views "$tmp/meta" default dh_1022)" || rc=$?
expect "view absent after restore fails closed" 1 "$rc" "MISSING after restore: plain_view" "$out"
unset plain_missing

printf 'ATTACH VIEW _ garbage without a body\n' >"$tmp/meta/broken.sql"
rc=0; out="$(rvc_check_views "$tmp/meta" default dh_1022)" || rc=$?
expect "unparseable backup metadata fails closed" 1 "$rc" "UNPARSEABLE backup metadata: broken" "$out"
rm "$tmp/meta/broken.sql"

printf 'ATTACH TABLE _ UUID 1 (a UInt8) ENGINE = Memory\n' >"$tmp/meta/not_a_view.sql"
rc=0; out="$(rvc_check_views "$tmp/meta" default dh_1022)" || rc=$?
expect "table metadata files are ignored" 0 "$rc"

: >"$tmp/meta/empty.sql"
rc=0; out="$(rvc_check_views "$tmp/meta" default dh_1022)" || rc=$?
expect "empty metadata file fails closed" 1 "$rc" "UNPARSEABLE backup metadata: empty.sql" "$out"
rm "$tmp/meta/empty.sql"

printf "CREATE VIEW default.lit (n String) AS SELECT 'default.foo' AS n\n" >"$tmp/meta/lit.sql"
saved_fn="$(declare -f rvc_restored_create_query)"
rvc_restored_create_query() {
  case "$2" in
    lit) printf "CREATE VIEW dh_1022.lit (n String) AS SELECT 'dh_1022.foo' AS n" ;;
    *) return 1 ;;
  esac
}
mkdir -p "$tmp/meta2"; cp "$tmp/meta/lit.sql" "$tmp/meta2/"
rc=0; out="$(rvc_check_views "$tmp/meta2" default dh_1022)" || rc=$?
expect "a changed string literal is not normalized away" 1 "$rc" "DEFINITION DIFFERS: lit" "$out"

printf "ATTACH MATERIALIZED VIEW _ UUID 'a-b' TO default.tgt (n String) AS SELECT 1 AS n\n" >"$tmp/meta3.sql"
mkdir -p "$tmp/meta3"; mv "$tmp/meta3.sql" "$tmp/meta3/mv%2Done.sql"
rvc_restored_create_query() {
  [[ "$2" == "mv-one" ]] || return 1
  printf "CREATE MATERIALIZED VIEW dh_1022.\`mv-one\` UUID 'c-d' TO %s (n String) AS SELECT 1 AS n" "$mv_target"
}
mv_target=dh_1022.tgt
rc=0; out="$(rvc_check_views "$tmp/meta3" default dh_1022)" || rc=$?
expect "escaped file name and matching materialized view pass" 0 "$rc" "" "$out"
mv_target=dh_1022.other
rc=0; out="$(rvc_check_views "$tmp/meta3" default dh_1022)" || rc=$?
expect "a changed TO target is reported" 1 "$rc" "DEFINITION DIFFERS: mv-one" "$out"
eval "$saved_fn"

rc=0; out="$(rvc_check_views "$tmp/nope" default dh_1022)" || rc=$?
expect "missing metadata dir fails closed" 1 "$rc"

[[ "$failures" -eq 0 ]] || exit 1
