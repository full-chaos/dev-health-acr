#!/usr/bin/env bash
set -euo pipefail

scenario=""
if [[ "${1:-}" == "--scenario" ]]; then scenario="${2:-}"; fi
case "$scenario" in healthy|missing-index|unsupported-version|path-sentinel) ;; *) echo "usage: $0 --scenario healthy|missing-index|unsupported-version|path-sentinel" >&2; exit 2;; esac
root="$(cd "$(dirname "$0")/../.." && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
bin="$tmp/acr-mcp"
repo="$tmp/repo"
mkdir -p "$repo"
mkdir -p "$repo/.codegraph/index"
git -C "$repo" init -q
git -C "$repo" config user.email doctor@example.test
git -C "$repo" config user.name doctor
printf 'package repo\n' > "$repo/repo.go"
git -C "$repo" add repo.go && git -C "$repo" commit -qm init
git -C "$repo" remote add origin https://github.com/full-chaos/acr.git
fake="$tmp/codegraph"
cat > "$fake" <<'EOF'
#!/usr/bin/env bash
EOF
printf 'scenario=%q\n' "$scenario" >> "$fake"
cat >> "$fake" <<'EOF'
set -euo pipefail
case "$1" in
status)
  if [[ "$scenario" == missing-index ]]; then printf '{"initialized":false,"version":"1.2.0"}\n'; exit 0; fi
  version=1.2.0; [[ "$scenario" == unsupported-version ]] && version=2.0.0
  root="$(pwd -P)"
  printf '{"initialized":true,"version":"%s","projectPath":"%s","indexPath":"%s/.codegraph","lastIndexed":"%s","fileCount":1,"nodeCount":1,"edgeCount":1,"dbSizeBytes":1,"backend":"node-sqlite","journalMode":"wal","nodesByKind":{},"languages":["go"],"pendingChanges":{"added":0,"modified":0,"removed":0},"worktreeMismatch":null,"index":{"builtWithVersion":"%s","builtWithExtractionVersion":24,"currentExtractionVersion":24,"reindexRecommended":false}}\n' "$version" "$root" "$root" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$version";;
query) printf '[]\n';;
*) exit 64;;
esac
EOF
chmod +x "$fake"
(cd "$root" && go build -o "$bin" ./cmd/acr-mcp)
out="$tmp/doctor.json"
(cd "$repo" && ACR_LOCAL_INDEX_PROVIDER=codegraph ACR_CODEGRAPH_EXECUTABLE="$fake" "$bin" doctor --offline > "$out")
python3 - "$out" "$scenario" "$tmp" <<'PY'
import json, pathlib, sys
d=json.load(open(sys.argv[1])); local=d["local_index"]; scenario=sys.argv[2]; sentinel=sys.argv[3]
assert d["status"] in {"incomplete_configuration","invalid_configuration","ok"}
if scenario == "healthy": assert local["query_succeeded"] and local["result_count"] == 0
if scenario == "missing-index": assert local["error_code"] == "local_index_missing"
if scenario == "unsupported-version": assert local["error_code"] == "local_index_incompatible_version"
if scenario == "path-sentinel":
    raw=pathlib.Path(sys.argv[1]).read_text(); assert sentinel not in raw and ("codegraph" not in raw.lower() or "provider_mode" in raw)
PY
cat "$out"
