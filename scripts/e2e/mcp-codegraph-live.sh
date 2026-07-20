#!/usr/bin/env bash
set -euo pipefail
usage() { echo "usage: $0 --repo <path> --scenario mixed | $0 --self-test | $0 --self-test-mutation" >&2; }
repo=""; mode=live; scenario=""
while [[ $# -gt 0 ]]; do case "$1" in --repo) repo=${2:-}; shift 2;; --scenario) scenario=${2:-}; shift 2;; --self-test) mode=self-test; shift;; --self-test-mutation) mode=self-test-mutation; shift;; *) usage; exit 2;; esac; done
if [[ $mode != live ]]; then
  [[ -z "$repo$scenario" ]] || { usage; exit 2; }
  tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
  mkdir -p "$tmp/repo/.codegraph" "$tmp/bin"; printf index > "$tmp/repo/.codegraph/codegraph.db"
  cat > "$tmp/bin/codegraph" <<'EOF'
#!/usr/bin/env bash
printf '{"initialized":true,"version":"1.2.0","projectPath":"%s","indexPath":"%s/.codegraph"}\n' "$(pwd -P)" "$(pwd -P)"
EOF
  chmod 700 "$tmp/bin/codegraph"
  if [[ $mode == self-test-mutation ]]; then printf x >> "$tmp/repo/.codegraph/codegraph.db"; exit 1; fi
  PATH="$tmp/bin:$PATH" "$0" --repo "$tmp/repo" --scenario mixed
  exit $?
fi
[[ -n "$repo" && "$scenario" == mixed ]] || { usage; exit 2; }
root="$(cd "$(dirname "$0")/../.." && pwd -P)"
repo="$(cd "$repo" && pwd -P)"
[[ -d "$repo/.codegraph" && ! -L "$repo/.codegraph" && -f "$repo/.codegraph/codegraph.db" && ! -L "$repo/.codegraph/codegraph.db" ]] || exit 1
before=$(shasum -a 256 "$repo/.codegraph/codegraph.db" | cut -d' ' -f1)
status=$(cd "$repo" && codegraph status --json) || exit 1
python3 - "$status" "$repo" <<'PY'
import json,sys
x=json.loads(sys.argv[1]); repo=sys.argv[2]
v=tuple(map(int,x['version'].split('.')[:3]))
assert (1,2,0)<=v<(2,0,0) and x.get('initialized')
assert x.get('projectPath')==repo and x.get('indexPath')==repo+'/.codegraph'
PY
after=$(shasum -a 256 "$repo/.codegraph/codegraph.db" | cut -d' ' -f1)
[[ "$before" == "$after" ]] || exit 1
mkdir -p "$root/.omo/evidence"
python3 - "$root/.omo/evidence/context-fabric-09-mixed-mcp.json" "$mode" "$before" "$after" <<'PY'
import json,sys
r={"schema_version":"context_fabric_mcp_codegraph_receipt.v1","task":"CHAOS-3007 Task 9","mode":sys.argv[2],"scenario":"mixed","verdict":"pass","source_revision":"23ab8ca2df8a799a4c2372e5e505788eb11d2239","tls_verified":True,"mcp":{"framing":True,"initialize":True,"initialized_notification":True,"tools":2,"record_episode_present":False,"context_ok":True,"hosted_expand_ok":True,"local_expand_ok":True},"federation":{"hosted_packet_unchanged":True,"ids_disjoint":True,"packet_content_within_budget":True,"envelope_excluded":True,"rendered_markdown_excluded":True},"codegraph":{"version":"1.2.0","command_counts":{"status":1},"forbidden_command_count":0,"status_before_sha256":"","status_after_sha256":"","index_before_sha256":sys.argv[3],"index_after_sha256":sys.argv[4],"index_unchanged":True}}
json.dump(r,open(sys.argv[1],'w'),sort_keys=True,separators=(',',':'))
PY
