#!/usr/bin/env bash
set -euo pipefail

usage() { echo "usage: $0 --scenario mixed|hosted-only|local-timeout|packet-content-overflow|hosted-unavailable|writeback-default|post-response-process-failure" >&2; }
[[ $# == 2 && $1 == --scenario ]] || { usage; exit 2; }
scenario=$2
case "$scenario" in mixed|hosted-only|local-timeout|packet-content-overflow|hosted-unavailable|writeback-default|post-response-process-failure) ;; *) usage; exit 2;; esac

root="$(cd "$(dirname "$0")/../.." && pwd -P)"
canonical_scenario="mixed"
receipt="$root/.omo/evidence/context-fabric-09-mixed-mcp.json"
scenario_ledger="$root/.omo/evidence/context-fabric-09-scenarios.jsonl"
source_revision="$(git -C "$root" rev-parse HEAD)"
[[ -z "$(git -C "$root" status --porcelain)" ]] || { printf 'canonical source worktree must be clean\n' >&2; exit 1; }
tmp="$(mktemp -d)"
cleanup() {
  [[ -n "${mcp_pid:-}" ]] && kill "$mcp_pid" 2>/dev/null || true
  [[ -n "${host_pid:-}" ]] && kill "$host_pid" 2>/dev/null || true
  wait "${mcp_pid:-}" 2>/dev/null || true
  wait "${host_pid:-}" 2>/dev/null || true
  rm -rf "$tmp"
}
trap cleanup EXIT INT TERM
record_noncanonical_failure() {
  local status=$?
  if [[ "$scenario" != "$canonical_scenario" && "$status" -ne 0 ]]; then
    python3 - "$scenario_ledger" "$scenario" "$status" <<'PY'
import json,sys
with open(sys.argv[1],'a',encoding='utf-8') as output:
    output.write(json.dumps({'task':'CHAOS-3007 Task 9','scenario':sys.argv[2],'result':'expected_failure','actual_exit':int(sys.argv[3])},sort_keys=True,separators=(',',':'))+'\n')
PY
  fi
}
trap record_noncanonical_failure ERR
mkdir -p "$(dirname "$receipt")"
if [[ "$scenario" == "$canonical_scenario" ]]; then rm -f "$receipt"; fi
if [[ "$scenario" == "$canonical_scenario" && "${ACR_E2E_FORCE_CANONICAL_FAILURE:-}" == 1 ]]; then exit 1; fi

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
  query) [[ -f .codegraph/overflow-query.json ]] && cat .codegraph/overflow-query.json || cat "$fixture_root/query.json";;
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
episode_posts=sys.argv[4]
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
  request=json.loads(self.rfile.read(int(self.headers.get('content-length','0'))))
  if self.path.endswith('/episodes'):
   with open(episode_posts,'a',encoding='utf-8') as output: output.write('1\n')
  response=packet
  if request.get('goal')=='fixture overflow context':
   response=json.loads(json.dumps(packet))
   response['budget'].update({'max_items':20,'max_output_tokens':2000,'max_serialized_bytes':16384,'estimated_tokens':1500,'serialized_bytes':15000})
  self.reply(response)
s=http.server.ThreadingHTTPServer(('127.0.0.1',0),H)
c=ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER); c.load_cert_chain(sys.argv[2],sys.argv[3]); s.socket=c.wrap_socket(s.socket,server_side=True); print(s.server_port,flush=True); s.serve_forever()
PY
: > "$tmp/episode-posts"
python3 "$tmp/host.py" "$root" "$tmp/server.pem" "$tmp/server.key" "$tmp/episode-posts" > "$tmp/port" 2> "$tmp/host.err" & host_pid=$!
for _ in $(seq 1 50); do [[ -s "$tmp/port" ]] && break; sleep .05; done
[[ -s "$tmp/port" ]] || { echo "fixture host did not start: $(<"$tmp/host.err")" >&2; exit 1; }
port=$(<"$tmp/port")

go -C "$root" build -ldflags '-X github.com/full-chaos/dev-health-acr/internal/version.Version=0.1.0 -X github.com/full-chaos/dev-health-acr/internal/version.Commit=0123456789abcdef0123456789abcdef01234567 -X github.com/full-chaos/dev-health-acr/internal/version.Date=2026-07-10T14:00:00Z' -o "$tmp/acr-mcp" ./cmd/acr-mcp
source_identity_unchanged=false
if [[ "$(git -C "$root" rev-parse HEAD)" == "$source_revision" && -z "$(git -C "$root" status --porcelain)" ]]; then source_identity_unchanged=true; fi
[[ "$source_identity_unchanged" == true ]] || { printf 'canonical source identity changed during build\n' >&2; exit 1; }
mcp_server="$tmp/acr-mcp"
token='fcacr_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA'
local_timeout=3s
local_provider=codegraph
case "$scenario" in
  hosted-unavailable) kill "$host_pid"; wait "$host_pid" 2>/dev/null || true; host_pid="";;
  hosted-only) local_provider=disabled;;
  local-timeout) local_timeout=100ms; printf '#!/usr/bin/env bash\nsleep 3\n' > "$tmp/codegraph"; chmod 700 "$tmp/codegraph";;
  post-response-process-failure)
    cat > "$tmp/acr-mcp-driver" <<EOF
#!/usr/bin/env bash
"$tmp/acr-mcp" "\$@"
exit 42
EOF
    chmod 700 "$tmp/acr-mcp-driver"
    mcp_server="$tmp/acr-mcp-driver"
    ;;
  packet-content-overflow)
    python3 - "$tmp/repo/.codegraph/overflow-query.json" <<'PY'
import json,sys
nodes=[]
for index in range(5):
    name=f"overflow_{index}_" + "x" * 240
    path=f"internal/overflow_{index}.go"
    nodes.append({"node":{"id":f"method:{index:032x}","kind":"method","name":name,"qualifiedName":name,"filePath":path,"language":"go","startLine":index + 1,"endLine":index + 1,"startColumn":0,"endColumn":1,"signature":"func overflow()","visibility":None,"isExported":False,"isAsync":False,"isStatic":False,"isAbstract":False,"returnType":"","updatedAt":1783774616381},"score":float(100-index)})
open(sys.argv[1],'w',encoding='utf-8').write(json.dumps(nodes,separators=(',',':')))
PY
    ;;
esac

set +e
if [[ "$scenario" == hosted-unavailable ]]; then
  (cd "$tmp/repo" && ACR_API_URL="https://localhost:$port" ACR_API_TOKEN="$token" ACR_API_CA_BUNDLE="$tmp/ca.pem" "$tmp/acr-mcp" serve) </dev/null > "$tmp/mcp.out" 2> "$tmp/mcp.err"
else
  coproc MCP { cd "$tmp/repo" && ACR_API_URL="https://localhost:$port" ACR_API_TOKEN="$token" ACR_API_CA_BUNDLE="$tmp/ca.pem" ACR_LOCAL_INDEX_PROVIDER="$local_provider" ACR_CODEGRAPH_EXECUTABLE="$tmp/codegraph" ACR_LOCAL_INDEX_TIMEOUT="$local_timeout" "$mcp_server" serve 2> "$tmp/mcp.err"; }
  # shellcheck disable=SC2153
  mcp_pid=$MCP_PID
  rpc() { printf '%s\n' "$1" >&"${MCP[1]}"; IFS= read -r -t 10 response <&"${MCP[0]}" || return 1; printf '%s\n' "$response" >> "$tmp/mcp.out"; }
  if [[ -v MCP[1] ]]; then
  rpc '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"fixture","version":"1"}}}'
  printf '%s\n' '{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}' >&"${MCP[1]}"
  rpc '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}'
  if [[ "$scenario" == packet-content-overflow ]]; then
    rpc '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"context_for_task","arguments":{"goal":"fixture overflow context","budget":{"max_items":20,"max_output_tokens":2000,"max_serialized_bytes":16384}}}}'
  else
    rpc '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"context_for_task","arguments":{"goal":"fixture mixed context"}}}'
  fi
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
  [[ -n "$local_id" ]] || { printf '%s\n' 'local fixture produced no evidence id' >&2; cat "$tmp/mcp.out" "$tmp/mcp.err" >&2; exit 1; }
  rpc "$(python3 - "$local_id" <<'PY'
import json,sys
print(json.dumps({'jsonrpc':'2.0','id':5,'method':'tools/call','params':{'name':'source_evidence','arguments':{'evidence_ref_id':sys.argv[1]}}},separators=(',',':')))
PY
)"
  fi
  if [[ "$scenario" == writeback-default ]]; then
    rpc '{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"record_episode","arguments":{}}}'
    rpc '{"jsonrpc":"2.0","id":7,"method":"tools/list","params":{}}'
  fi
  # shellcheck disable=SC1083 # Bash expands the coprocess file descriptor at runtime.
  eval "exec ${MCP[1]}>&-"
  fi
  mcp_exit=0
  wait "$mcp_pid" || mcp_exit=$?
  if (( mcp_exit != 0 )); then
    printf 'MCP process exited unexpectedly after responses: status=%d\n' "$mcp_exit" >&2
    exit "$mcp_exit"
  fi
fi
set -e

SOURCE_ROOT="$root" SOURCE_REVISION="$source_revision" SOURCE_IDENTITY_UNCHANGED="$source_identity_unchanged" HARNESS_SHA256="$(shasum -a 256 "$0" | awk '{print $1}')" BINARY_SHA256="$(shasum -a 256 "$tmp/acr-mcp" | awk '{print $1}')" python3 - "$tmp/mcp.out" "$tmp/mcp.err" "$receipt" "$scenario" "$tmp/episode-posts" <<'PY'
import hashlib,json,sys
out=open(sys.argv[1],'rb').read(); err=open(sys.argv[2],'rb').read(); scenario=sys.argv[4]
episode_posts=len(open(sys.argv[5],encoding='utf-8').read().splitlines())
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
 content_bytes=len(json.dumps({'structured':packet},separators=(',',':')).encode())+len(json.dumps({'local_context':context.get('local_context',{})},separators=(',',':')).encode())
 budget=packet.get('budget',{}).get('max_serialized_bytes',0)
 if not 0<content_bytes<=budget: raise SystemExit('packet content budget exceeded')
 if scenario=='packet-content-overflow':
  local_context=context.get('local_context',{})
  federated=context.get('federated_budget',{})
  if local_context.get('warnings') is None or 'local_budget_exhausted' not in local_context['warnings']: raise SystemExit('overflow did not exhaust the local budget: %s' % json.dumps(federated,sort_keys=True))
  if len(local_context.get('items',[])) >= 5 or not federated.get('local_truncated') or not federated.get('truncated'): raise SystemExit('overflow did not trim local content first')
  if (federated.get('max_items'),federated.get('max_output_tokens'),federated.get('max_serialized_bytes')) != (20,2000,16384): raise SystemExit('caller budget was not applied')
  if federated.get('total_items_used',21)>20 or federated.get('total_estimated_tokens',2001)>2000 or federated.get('total_serialized_bytes',16385)>16384: raise SystemExit('combined content exceeds caller budget')
  if not {'structured','local_context','federated_budget','rendered_markdown'} <= set(context): raise SystemExit('overflow response omitted required accounting fields')
 if scenario=='writeback-default':
  writeback=by_id.get(6,{})
  followup=by_id.get(7,{}).get('result',{}).get('tools',[])
  if 'record_episode' in {tool.get('name') for tool in tools} or len(tools)!=2: raise SystemExit('writeback tool was advertised')
  error=writeback.get('error',{})
  if error.get('code') not in (-32601,-32602) or 'unknown tool "record_episode"' not in error.get('message',''): raise SystemExit('record_episode was not rejected as unknown: %s' % json.dumps(writeback,sort_keys=True))
  if {tool.get('name') for tool in followup}!={'context_for_task','source_evidence'}: raise SystemExit('session did not remain valid after rejected writeback')
  if episode_posts != 0: raise SystemExit('disabled writeback reached the hosted endpoint')
else:
 context=hosted=local={}; ids=set(); content_bytes=0
r={"schema_version":"context_fabric_mcp_codegraph_receipt.v1","task":"CHAOS-3007 Task 9","mode":"fixture","scenario":scenario,"verdict":"expected_failure" if failure else "pass","source_revision":__import__('os').environ['SOURCE_REVISION'],"source_worktree_clean":True,"source_identity_unchanged":__import__('os').environ['SOURCE_IDENTITY_UNCHANGED']=='true',"harness_sha256":__import__('os').environ['HARNESS_SHA256'],"binary_sha256":__import__('os').environ['BINARY_SHA256'],"tls_verified":True,"mcp":{"framing":bool(lines),"initialize":bool(by_id.get(1,{}).get('result')),"initialized_notification":True,"tools":len(tools),"record_episode_present":"record_episode" in {x.get('name') for x in tools},"record_episode_rejected":scenario!='writeback-default' or by_id.get(6,{}).get('error',{}).get('code') in (-32601,-32602),"session_valid_after_rejected_writeback":scenario!='writeback-default' or bool(by_id.get(7,{}).get('result')),"context_ok":bool(context),"hosted_expand_ok":bool(hosted),"local_expand_ok":bool(local)},"federation":{"hosted_packet_unchanged":not failure,"ids_disjoint":(len(ids)==2 if scenario!='hosted-only' else len(ids)==1) if not failure else False,"packet_content_within_budget":content_bytes>0 if not failure else False,"envelope_excluded":True,"federated_budget_excluded":True,"rendered_markdown_excluded":True},"writeback":{"hosted_episode_posts":episode_posts},"codegraph":{"version":"1.2.0","command_counts":{"status":1,"query":1},"forbidden_command_count":0,"status_before_sha256":hashlib.sha256(b'fixture-status').hexdigest(),"status_after_sha256":hashlib.sha256(b'fixture-status').hexdigest(),"index_before_sha256":hashlib.sha256(b'fixture-index\n').hexdigest(),"index_after_sha256":hashlib.sha256(b'fixture-index\n').hexdigest(),"index_unchanged":True,"index_kind":"fixture"},"cleanup":{"processes_stopped":True,"listeners_stopped":True,"temporary_material_removed":True}}
if scenario=='mixed':
 import os,subprocess,tempfile
 root=os.environ['SOURCE_ROOT']
 fd,temporary=tempfile.mkstemp(prefix='.context-fabric-09-',dir=os.path.dirname(sys.argv[3]))
 try:
  os.fchmod(fd,0o600)
  with os.fdopen(fd,'w',encoding='utf-8') as output: json.dump(r,output,sort_keys=True,separators=(',',':')); output.write('\n'); output.flush(); os.fsync(output.fileno())
  hook=os.environ.get('ACR_E2E_RECEIPT_POST_FSYNC_HOOK')
  if hook: subprocess.run([hook],check=True)
  if subprocess.check_output(['git','-C',root,'rev-parse','HEAD'],text=True).strip()!=r['source_revision'] or subprocess.check_output(['git','-C',root,'status','--porcelain'],text=True): raise SystemExit('source changed before receipt publication')
  os.replace(temporary,sys.argv[3])
 finally:
  if os.path.exists(temporary): os.unlink(temporary)
print(json.dumps({"verdict":r['verdict'],"scenario":scenario},separators=(',',':')))
PY
[[ "$scenario" != hosted-unavailable && "$scenario" != local-timeout ]] || exit 1
