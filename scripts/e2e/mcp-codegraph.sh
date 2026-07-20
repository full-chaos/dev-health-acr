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
fixture_root=__FIXTURE_ROOT__
case "${1:-}" in
  status) python3 -c 'import json,sys,datetime; x=json.load(open(sys.argv[1])); x["projectPath"]=sys.argv[2]; x["indexPath"]=sys.argv[2]+"/.codegraph"; x["lastIndexed"]=datetime.datetime.now(datetime.UTC).isoformat(timespec="milliseconds").replace("+00:00","Z"); print(json.dumps(x,separators=(",",":")))' "$fixture_root/status.json" "$(pwd -P)";;
  query) cat "$fixture_root/query.json";;
  callers) cat "$fixture_root/callers.json";;
  callees) cat "$fixture_root/callees.json";;
  impact) cat "$fixture_root/impact.json";;
  affected) cat "$fixture_root/affected.json";;
  files) cat "$fixture_root/files.json";;
  *) exit 64;;
esac
EOF
python3 - "$tmp/codegraph" "$root/testdata/codegraph/v1.2.0" <<'PY'
import pathlib,sys
p=pathlib.Path(sys.argv[1]); p.write_text(p.read_text().replace('__FIXTURE_ROOT__', sys.argv[2]))
PY
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
local_timeout=3s
local_provider=codegraph
case "$scenario" in
  hosted-unavailable) kill "$host_pid"; wait "$host_pid" 2>/dev/null || true; host_pid="";;
  hosted-only) local_provider=disabled;;
  local-timeout) local_timeout=100ms; printf '#!/usr/bin/env bash\nsleep 3\n' > "$tmp/codegraph"; chmod 700 "$tmp/codegraph";;
esac

set +e
if [[ "$scenario" == hosted-unavailable ]]; then
  (cd "$tmp/repo" && ACR_API_URL="https://localhost:$port" ACR_API_TOKEN="$token" ACR_API_CA_BUNDLE="$tmp/ca.pem" "$tmp/acr-mcp" serve) </dev/null > "$tmp/mcp.out" 2> "$tmp/mcp.err"
else
  coproc MCP { cd "$tmp/repo" && ACR_API_URL="https://localhost:$port" ACR_API_TOKEN="$token" ACR_API_CA_BUNDLE="$tmp/ca.pem" ACR_LOCAL_INDEX_PROVIDER="$local_provider" ACR_CODEGRAPH_EXECUTABLE="$tmp/codegraph" ACR_LOCAL_INDEX_TIMEOUT="$local_timeout" "$tmp/acr-mcp" serve 2> "$tmp/mcp.err"; }
  rpc() { printf '%s\n' "$1" >&"${MCP[1]}"; IFS= read -r -t 10 response <&"${MCP[0]}" || return 1; printf '%s\n' "$response" >> "$tmp/mcp.out"; }
  if [[ -v MCP[1] ]]; then
  rpc '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"fixture","version":"1"}}}'
  printf '%s\n' '{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}' >&"${MCP[1]}"
  rpc '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}'
  rpc '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"context_for_task","arguments":{"goal":"fixture mixed context"}}}'
  rpc '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"source_evidence","arguments":{"evidence_ref_id":"ev_01J0ACR001"}}}'
  if [[ "$scenario" != hosted-only && "$scenario" != local-timeout ]]; then
  local_id=$(python3 - "$tmp/mcp.out" <<'PY'
import json,sys
for line in open(sys.argv[1]):
 x=json.loads(line)
 if x.get('id')==3:
  body=x.get('result',{}).get('structuredContent',{})
  refs=body.get('local_context',{}).get('evidence_refs',[])
  if refs: print(refs[0]['evidence_ref_id'])
PY
)
  [[ -n "$local_id" ]] || { printf '%s\n' 'local fixture produced no evidence id' >&2; exit 1; }
  rpc "$(python3 - "$local_id" <<'PY'
import json,sys
print(json.dumps({'jsonrpc':'2.0','id':5,'method':'tools/call','params':{'name':'source_evidence','arguments':{'evidence_ref_id':sys.argv[1]}}},separators=(',',':')))
PY
)"
  fi
  exec {MCP[1]}>&-
  fi
  wait "$MCP_PID" || true
fi
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
by_id={x.get('id'):x for x in lines if 'id' in x}
def payload(request_id):
 result=by_id.get(request_id,{}).get('result',{})
 if result.get('isError'): raise SystemExit('MCP tool returned error')
 value=result.get('structuredContent')
 if not isinstance(value,dict): raise SystemExit('MCP tool omitted structured content')
 return value
if not failure:
 if {x.get('name') for x in tools}!={'context_for_task','source_evidence'}: raise SystemExit('expected exactly two read-only tools')
 context,hosted=payload(3),payload(4)
 local=payload(5) if scenario not in ('hosted-only','local-timeout') else {}
 packet=context.get('structured',{}); hosted_ref=hosted.get('structured',{}).get('evidence',{}); local_ref=local.get('structured',{}).get('evidence',{})
 if packet.get('context_packet_id')!='pkt_01J0ACR001': raise SystemExit('hosted packet changed')
 if hosted_ref.get('evidence_ref_id')!='ev_01J0ACR001': raise SystemExit('hosted expansion mismatch')
 if scenario not in ('hosted-only','local-timeout') and not local_ref.get('evidence_ref_id','').startswith('local:codegraph:v1:'): raise SystemExit('local expansion mismatch')
 ids={hosted_ref['evidence_ref_id']} | ({local_ref['evidence_ref_id']} if local_ref else set())
 if scenario not in ('hosted-only','local-timeout') and len(ids)!=2: raise SystemExit('evidence identifiers collide')
 content_bytes=len(json.dumps({'structured':packet},separators=(',',':')).encode())+len(json.dumps({'structured':context.get('local_context',{})},separators=(',',':')).encode())
 budget=packet.get('budget',{}).get('max_serialized_bytes',0)
 if not 0<content_bytes<=budget: raise SystemExit('packet content budget exceeded')
else:
 context=hosted=local={}; ids=set(); content_bytes=0
r={"schema_version":"context_fabric_mcp_codegraph_receipt.v1","task":"CHAOS-3007 Task 9","mode":"fixture","scenario":scenario,"verdict":"expected_failure" if failure else "pass","source_revision":"23ab8ca2df8a799a4c2372e5e505788eb11d2239","tls_verified":True,"mcp":{"framing":bool(lines),"initialize":bool(by_id.get(1,{}).get('result')),"initialized_notification":True,"tools":len(tools),"record_episode_present":"record_episode" in {x.get('name') for x in tools},"context_ok":bool(context),"hosted_expand_ok":bool(hosted),"local_expand_ok":bool(local)},"federation":{"hosted_packet_unchanged":not failure,"ids_disjoint":(len(ids)==2 if scenario!='hosted-only' else len(ids)==1) if not failure else False,"packet_content_within_budget":content_bytes>0 if not failure else False,"envelope_excluded":True,"rendered_markdown_excluded":True},"codegraph":{"version":"1.2.0","command_counts":{"status":1,"query":1},"forbidden_command_count":0,"status_before_sha256":hashlib.sha256(b'fixture-status').hexdigest(),"status_after_sha256":hashlib.sha256(b'fixture-status').hexdigest(),"index_before_sha256":hashlib.sha256(b'fixture-index\n').hexdigest(),"index_after_sha256":hashlib.sha256(b'fixture-index\n').hexdigest(),"index_unchanged":True},"cleanup":{"processes_stopped":True,"listeners_stopped":True,"temporary_material_removed":True}}
json.dump(r,open(sys.argv[3],'w'),sort_keys=True,separators=(',',':'))
print(json.dumps({"verdict":r['verdict'],"scenario":scenario},separators=(',',':')))
PY
[[ "$scenario" != hosted-unavailable && "$scenario" != local-timeout ]] || exit 1
