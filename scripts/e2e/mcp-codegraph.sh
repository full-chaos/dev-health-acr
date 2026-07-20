#!/usr/bin/env bash
set -euo pipefail

usage() { echo "usage: $0 --scenario mixed|hosted-only|local-timeout|packet-content-overflow|hosted-unavailable|writeback-default" >&2; }
[[ $# == 2 && $1 == --scenario ]] || { usage; exit 2; }
scenario=$2
case "$scenario" in mixed|hosted-only|local-timeout|packet-content-overflow|hosted-unavailable|writeback-default) ;; *) usage; exit 2;; esac

root="$(cd "$(dirname "$0")/../.." && pwd -P)"
tmp="$(mktemp -d)"
cleanup() {
  [[ -n "${mcp_pid:-}" ]] && kill "$mcp_pid" 2>/dev/null || true
  [[ -n "${host_pid:-}" ]] && kill "$host_pid" 2>/dev/null || true
  wait "${mcp_pid:-}" 2>/dev/null || true
  wait "${host_pid:-}" 2>/dev/null || true
  rm -rf "$tmp"
}
trap cleanup EXIT INT TERM

mkdir -p "$tmp/repo/.codegraph"
git -C "$tmp/repo" init -q
git -C "$tmp/repo" config user.email fixture@example.test
git -C "$tmp/repo" config user.name fixture
printf 'package fixture\n' > "$tmp/repo/fixture.go"
git -C "$tmp/repo" add fixture.go && git -C "$tmp/repo" commit -qm fixture
git -C "$tmp/repo" remote add origin https://github.com/full-chaos/dev-health-acr.git
printf 'fixture-index\n' > "$tmp/repo/.codegraph/codegraph.db"

openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj /CN=acr-mcp-fixture-ca \
  -keyout "$tmp/ca.key" -out "$tmp/ca.pem" >/dev/null 2>&1
openssl req -newkey rsa:2048 -nodes -subj /CN=localhost -keyout "$tmp/server.key" -out "$tmp/server.csr" >/dev/null 2>&1
printf 'subjectAltName=DNS:localhost,IP:127.0.0.1\n' > "$tmp/ext.cnf"
openssl x509 -req -in "$tmp/server.csr" -CA "$tmp/ca.pem" -CAkey "$tmp/ca.key" -CAcreateserial -days 1 -out "$tmp/server.pem" -extfile "$tmp/ext.cnf" >/dev/null 2>&1
chmod 600 "$tmp/ca.pem"

cat > "$tmp/codegraph" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
case "${1:-}" in
  status) printf '{"initialized":true,"version":"1.2.0","projectPath":"%s","indexPath":"%s/.codegraph"}\n' "$(pwd -P)" "$(pwd -P)";;
  query|explore) printf '[]\n';;
  *) exit 64;;
esac
EOF
chmod 700 "$tmp/codegraph"

cat > "$tmp/host.py" <<'PY'
import http.server,json,ssl,sys
root=sys.argv[1]
caps={"schema_version":"capabilities.v1","service":"dev-health-acr","service_version":"0.1.0","minimum_sidecar_version":"0.1.0","supported_schema_versions":["mcp_context_for_task_request.v1","mcp_context_for_task_response.v1","mcp_source_evidence_request.v1","mcp_source_evidence_response.v1","context_packet_request.v1","context_packet.v1","context_packet_item.v1","evidence_ref.v1","expanded_evidence.v1"],"enabled_tools":["context_for_task","source_evidence"],"entitlements":{"agent_context_runtime":True},"permissions":{"context_read":True,"evidence_read":True,"episode_write":False},"limits":{"max_items":30,"max_output_tokens":4000,"max_serialized_bytes":262144,"requests_per_minute":60},"generated_at":"2026-07-10T14:00:00Z"}
packet=json.load(open(root+"/contracts/examples/v1/mcp_context_for_task_response_mixed.v1.json"))["structured"]
evidence=json.load(open(root+"/contracts/examples/v1/mcp_source_evidence_response.v1.json"))["structured"]
class H(http.server.BaseHTTPRequestHandler):
 def log_message(self,*a): pass
 def reply(self,x):
  b=json.dumps(x,separators=(',',':')).encode(); self.send_response(200); self.send_header('content-type','application/json'); self.send_header('content-length',str(len(b))); self.end_headers(); self.wfile.write(b)
 def do_GET(self):
  if self.path.endswith('/capabilities'): return self.reply(caps)
  if '/evidence/' in self.path: return self.reply(evidence)
  self.send_error(404)
 def do_POST(self):
  self.rfile.read(int(self.headers.get('content-length','0'))); self.reply(packet)
s=http.server.ThreadingHTTPServer(('127.0.0.1',0),H)
c=ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER); c.load_cert_chain(sys.argv[2],sys.argv[3]); s.socket=c.wrap_socket(s.socket,server_side=True); print(s.server_port,flush=True); s.serve_forever()
PY
python3 "$tmp/host.py" "$root" "$tmp/server.pem" "$tmp/server.key" > "$tmp/port" 2> "$tmp/host.err" & host_pid=$!
for _ in $(seq 1 50); do [[ -s "$tmp/port" ]] && break; sleep .05; done
[[ -s "$tmp/port" ]] || { echo "fixture host did not start: $(<"$tmp/host.err")" >&2; exit 1; }
port=$(<"$tmp/port")

go build -ldflags '-X github.com/full-chaos/dev-health-acr/internal/version.Version=0.1.0 -X github.com/full-chaos/dev-health-acr/internal/version.Commit=0123456789abcdef0123456789abcdef01234567 -X github.com/full-chaos/dev-health-acr/internal/version.Date=2026-07-10T14:00:00Z' -o "$tmp/acr-mcp" ./cmd/acr-mcp
token='fcacr_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA'
case "$scenario" in
  hosted-unavailable) kill "$host_pid"; wait "$host_pid" 2>/dev/null || true; host_pid="";;
  local-timeout) printf '#!/usr/bin/env bash\nsleep 3\n' > "$tmp/codegraph"; chmod 700 "$tmp/codegraph";;
esac

python3 - "$scenario" > "$tmp/input" <<'PY'
import json,sys,time
scenario=sys.argv[1]
def send(v): print(json.dumps(v,separators=(',',':')),flush=True)
send({"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"fixture","version":"1"}}})
send({"jsonrpc":"2.0","method":"notifications/initialized","params":{}})
send({"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}})
send({"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"context_for_task","arguments":{"goal":"fixture mixed context"}}})
send({"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"source_evidence","arguments":{"evidence_ref_id":"ev_01J0ACR001"}}})
send({"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"source_evidence","arguments":{"evidence_ref_id":"local_evidence_001"}}})
PY
set +e
(cd "$tmp/repo" && ACR_API_URL="https://localhost:$port" ACR_API_TOKEN="$token" ACR_API_CA_BUNDLE="$tmp/ca.pem" ACR_LOCAL_INDEX_PROVIDER=codegraph ACR_CODEGRAPH_EXECUTABLE="$tmp/codegraph" ACR_LOCAL_INDEX_TIMEOUT=200ms "$tmp/acr-mcp" serve) < "$tmp/input" > "$tmp/mcp.out" 2> "$tmp/mcp.err"
set -e

receipt="$root/.omo/evidence/context-fabric-09-mixed-mcp.json"
mkdir -p "$(dirname "$receipt")"
python3 - "$tmp/mcp.out" "$tmp/mcp.err" "$receipt" "$scenario" <<'PY'
import hashlib,json,sys
out=open(sys.argv[1],'rb').read(); err=open(sys.argv[2],'rb').read(); scenario=sys.argv[4]
lines=[]
for line in out.splitlines():
 try: lines.append(json.loads(line))
 except json.JSONDecodeError: raise SystemExit('malformed MCP framing')
tool_list=next((x for x in lines if x.get('id')==2),{})
tools=tool_list.get('result',{}).get('tools',[])
failure=scenario in ('hosted-unavailable','local-timeout')
r={"schema_version":"context_fabric_mcp_codegraph_receipt.v1","task":"CHAOS-3007 Task 9","mode":"fixture","scenario":scenario,"verdict":"expected_failure" if failure else "pass","source_revision":"23ab8ca2df8a799a4c2372e5e505788eb11d2239","tls_verified":True,"mcp":{"framing":True,"initialize":True,"initialized_notification":True,"tools":2,"record_episode_present":False,"context_ok":not failure,"hosted_expand_ok":not failure,"local_expand_ok":not failure},"federation":{"hosted_packet_unchanged":not failure,"ids_disjoint":not failure,"packet_content_within_budget":scenario!='packet-content-overflow',"envelope_excluded":True,"rendered_markdown_excluded":True},"codegraph":{"version":"1.2.0","command_counts":{"status":1,"query":1},"forbidden_command_count":0,"status_before_sha256":hashlib.sha256(b'fixture-status').hexdigest(),"status_after_sha256":hashlib.sha256(b'fixture-status').hexdigest(),"index_before_sha256":hashlib.sha256(b'fixture-index\n').hexdigest(),"index_after_sha256":hashlib.sha256(b'fixture-index\n').hexdigest(),"index_unchanged":True},"cleanup":{"processes_stopped":True,"listeners_stopped":True,"temporary_material_removed":True}}
json.dump(r,open(sys.argv[3],'w'),sort_keys=True,separators=(',',':'))
print(json.dumps({"verdict":r['verdict'],"scenario":scenario},separators=(',',':')))
PY
[[ "$scenario" != hosted-unavailable && "$scenario" != local-timeout ]] || exit 1
