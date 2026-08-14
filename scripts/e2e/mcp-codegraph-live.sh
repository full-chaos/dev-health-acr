#!/usr/bin/env bash
set -euo pipefail

usage() { echo "usage: $0 --repo <path> --scenario mixed | $0 --self-test | $0 --self-test-mutation" >&2; }
repo=""; mode=live; scenario=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --repo) repo=${2:-}; shift 2 ;;
    --scenario) scenario=${2:-}; shift 2 ;;
    --self-test) mode=self-test; shift ;;
    --self-test-mutation) mode=self-test-mutation; shift ;;
    *) usage; exit 2 ;;
  esac
done

root="$(cd "$(dirname "$0")/../.." && pwd -P)"
evidence_dir="${ACR_E2E_EVIDENCE_DIR:-$root/.omo/evidence}"
receipt_name="${ACR_E2E_LIVE_RECEIPT_NAME:-context-fabric-09-live-mcp.json}"
live_receipt="$evidence_dir/$receipt_name"
source_revision="$(git -C "$root" rev-parse HEAD)"
[[ -z "$(git -C "$root" status --porcelain)" ]] || { printf 'canonical source worktree must be clean\n' >&2; exit 1; }
self_test_evidence=""
if [[ "$mode" != live ]]; then
  self_test_evidence="$(mktemp -d)"
  evidence_dir="$self_test_evidence"
  receipt_name="context-fabric-09-self-test-mcp.json"
  live_receipt="$evidence_dir/$receipt_name"
fi
mkdir -p "$evidence_dir"
rm -f "$live_receipt"
tmp=""
mcp_pid=""
host_pid=""
cleanup() {
  local command_status=$?
  trap - EXIT INT TERM
  if [[ -n "$mcp_pid" ]]; then kill "$mcp_pid" 2>/dev/null || true; fi
  if [[ -n "$host_pid" ]]; then kill "$host_pid" 2>/dev/null || true; fi
  if [[ -n "$mcp_pid" ]]; then wait "$mcp_pid" 2>/dev/null || true; fi
  if [[ -n "$host_pid" ]]; then wait "$host_pid" 2>/dev/null || true; fi
  if [[ -n "$tmp" ]]; then rm -rf "$tmp"; fi
  if [[ -n "$self_test_evidence" ]]; then rm -rf "$self_test_evidence"; fi
  exit "$command_status"
}
trap cleanup EXIT INT TERM

if [[ "$mode" != live ]]; then
  [[ -z "$repo$scenario" ]] || { usage; exit 2; }
  set +e
  "$0" >/dev/null 2>&1
  missing_flags_status=$?
  set -e
  [[ "$missing_flags_status" == 2 ]] || exit 1
  tmp="$(mktemp -d)"
  mkdir -p "$tmp/repo/.codegraph" "$tmp/bin"
  git -C "$tmp/repo" init -q
  git -C "$tmp/repo" config user.email fixture@example.test
  git -C "$tmp/repo" config user.name fixture
  printf 'package fixture\n' > "$tmp/repo/fixture.go"
  git -C "$tmp/repo" add fixture.go && git -C "$tmp/repo" commit -qm fixture
  git -C "$tmp/repo" remote add origin https://github.com/full-chaos/dev-health-acr.git
  printf 'fixture-index\n' > "$tmp/repo/.codegraph/codegraph.db"
  cat > "$tmp/bin/codegraph" <<EOF
#!/usr/bin/env bash
set -euo pipefail
case "\${1:-}" in
  status) python3 -c 'import json,sys; x=json.load(open(sys.argv[1])); x["projectPath"]=sys.argv[2]; x["indexPath"]=sys.argv[2]+"/.codegraph"; print(json.dumps(x,separators=(",",":")))' "$root/testdata/codegraph/v1.2.0/status.json" "\$(pwd -P)" ;;
  query) cat "$root/testdata/codegraph/v1.2.0/query.json" ;;
  callers) cat "$root/testdata/codegraph/v1.2.0/callers.json" ;;
  callees) cat "$root/testdata/codegraph/v1.2.0/callees.json" ;;
  impact) cat "$root/testdata/codegraph/v1.2.0/impact.json" ;;
  affected) cat "$root/testdata/codegraph/v1.2.0/affected.json" ;;
  files) cat "$root/testdata/codegraph/v1.2.0/files.json" ;;
  *) exit 64 ;;
esac
EOF
  chmod 700 "$tmp/bin/codegraph"
  mkdir -p "$tmp/evidence"
  if [[ "$mode" == self-test-mutation ]]; then
    ACR_E2E_EVIDENCE_DIR="$tmp/evidence" ACR_E2E_LIVE_RECEIPT_NAME=context-fabric-09-self-test-mcp.json ACR_E2E_RECEIPT_MODE=self-test ACR_E2E_INDEX_KIND=fixture ACR_E2E_MUTATE_INDEX_AFTER_PREFLIGHT=1 PATH="$tmp/bin:$PATH" "$0" --repo "$tmp/repo" --scenario mixed
  else
    ACR_E2E_EVIDENCE_DIR="$tmp/evidence" ACR_E2E_LIVE_RECEIPT_NAME=context-fabric-09-self-test-mcp.json ACR_E2E_RECEIPT_MODE=self-test ACR_E2E_INDEX_KIND=fixture PATH="$tmp/bin:$PATH" "$0" --repo "$tmp/repo" --scenario mixed
    python3 - "$tmp/evidence/context-fabric-09-self-test-mcp.json" <<'PY'
import json,sys
receipt=json.load(open(sys.argv[1]))
assert receipt['mode']=='self-test'
assert receipt['codegraph']['index_kind']=='fixture'
assert receipt['workspace_head_unchanged']
PY
  fi
  exit $?
fi

[[ -n "$repo" && "$scenario" == mixed ]] || { usage; exit 2; }
repo="$(cd "$repo" && pwd -P)"
validate_index() {
  python3 - "$repo" <<'PY'
import os,stat,sys
repo=os.path.realpath(sys.argv[1]); index=os.path.join(repo,'.codegraph')
uid=os.getuid(); managed=os.path.realpath(os.path.expanduser('~/.omo/codegraph/projects'))
if os.path.islink(index):
 target=os.path.realpath(index)
 if os.path.commonpath((target,managed)) != managed or os.path.dirname(target) != managed: raise SystemExit(1)
else: target=index
if not os.path.isdir(target): raise SystemExit(1)
db=os.path.join(target,'codegraph.db')
if os.path.islink(db) or not os.path.isfile(db) or os.path.getsize(db) == 0: raise SystemExit(1)
for path in (target,db):
 s=os.stat(path,follow_symlinks=False)
 if s.st_uid != uid or stat.S_IMODE(s.st_mode) & 0o022: raise SystemExit(1)
PY
}
validate_index

real_codegraph="$(command -v codegraph || true)"
[[ -n "$real_codegraph" ]] || exit 1
real_codegraph="$(python3 - "$real_codegraph" <<'PY'
import os,sys
print(os.path.realpath(sys.argv[1]))
PY
)"
[[ -f "$real_codegraph" && -x "$real_codegraph" ]] || exit 1

db_identity() {
  python3 - "$repo/.codegraph/codegraph.db" <<'PY'
import hashlib,json,os,stat,sys
s=os.stat(sys.argv[1],follow_symlinks=False)
print(json.dumps({"device":s.st_dev,"inode":s.st_ino,"size":s.st_size,"mode":stat.S_IMODE(s.st_mode),"sha256":hashlib.file_digest(open(sys.argv[1],"rb"),"sha256").hexdigest()},sort_keys=True,separators=(",",":")))
PY
}
status_hash() {
  (cd "$repo" && "$real_codegraph" status --json) | shasum -a 256 | cut -d' ' -f1
}
validate_status() {
  local status
  status="$(cd "$repo" && "$real_codegraph" status --json)"
  python3 - "$status" "$repo" <<'PY'
import json,sys
x=json.loads(sys.argv[1]); repo=sys.argv[2]
v=tuple(map(int,x["version"].split(".")[:3]))
assert (1,2,0)<=v<(2,0,0) and x.get("initialized")
assert x.get("projectPath")==repo and x.get("indexPath")==repo+"/.codegraph"
PY
}

before_identity="$(db_identity)"
status_before="$(status_hash)"
workspace_head_before="$(git -C "$repo" rev-parse HEAD)"
validate_status
if [[ "${ACR_E2E_MUTATE_INDEX_AFTER_PREFLIGHT:-}" == 1 ]]; then
  printf x >> "$repo/.codegraph/codegraph.db"
fi

tmp="$(mktemp -d)"
umask 077
command_log="$tmp/codegraph-commands.log"
: > "$command_log"
chmod 600 "$command_log"
wrapper="$tmp/codegraph"
cat > "$wrapper" <<EOF
#!/usr/bin/env bash
set -euo pipefail
case "\${1:-}" in
  status|query|callers|callees|impact|affected|files) printf '%s\\n' "\$1" >> "$command_log" ;;
  *) exit 64 ;;
esac
exec "$real_codegraph" "\$@"
EOF
chmod 700 "$wrapper"

openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj /CN=acr-mcp-live-ca \
  -keyout "$tmp/ca.key" -out "$tmp/ca.pem" >/dev/null 2>&1
openssl req -newkey rsa:2048 -nodes -subj /CN=localhost -keyout "$tmp/server.key" -out "$tmp/server.csr" >/dev/null 2>&1
printf 'subjectAltName=DNS:localhost,IP:127.0.0.1\n' > "$tmp/ext.cnf"
openssl x509 -req -in "$tmp/server.csr" -CA "$tmp/ca.pem" -CAkey "$tmp/ca.key" -CAcreateserial -days 1 -out "$tmp/server.pem" -extfile "$tmp/ext.cnf" >/dev/null 2>&1
chmod 600 "$tmp/ca.pem" "$tmp/ca.key" "$tmp/server.key"

cat > "$tmp/host.py" <<'PY'
import http.server,json,ssl,sys
root=sys.argv[1]
caps={"schema_version":"capabilities.v1","service":"dev-health-acr","service_version":"0.1.0","minimum_sidecar_version":"0.1.0","supported_schema_versions":json.load(open(root+"/contracts/examples/v1/capabilities.v1.json"))["supported_schema_versions"],"enabled_tools":["context_for_task","source_evidence"],"entitlements":{"agent_context_runtime":True},"permissions":{"context_read":True,"evidence_read":True,"episode_write":False},"limits":{"max_items":30,"max_output_tokens":4000,"max_serialized_bytes":262144,"requests_per_minute":60},"generated_at":"2026-07-10T14:00:00Z"}
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
 def do_POST(self): self.rfile.read(int(self.headers.get('content-length','0'))); self.reply(packet)
s=http.server.ThreadingHTTPServer(('127.0.0.1',0),H)
c=ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER); c.load_cert_chain(sys.argv[2],sys.argv[3]); s.socket=c.wrap_socket(s.socket,server_side=True); print(s.server_port,flush=True); s.serve_forever()
PY
python3 "$tmp/host.py" "$root" "$tmp/server.pem" "$tmp/server.key" > "$tmp/port" 2> "$tmp/host.err" & host_pid=$!
for _ in $(seq 1 50); do [[ -s "$tmp/port" ]] && break; sleep .05; done
[[ -s "$tmp/port" ]] || { printf '%s\n' 'live fixture host did not start' >&2; exit 1; }
port=$(<"$tmp/port")

go -C "$root" build -ldflags '-X github.com/full-chaos/dev-health-acr/internal/version.Version=0.1.0 -X github.com/full-chaos/dev-health-acr/internal/version.Commit=0123456789abcdef0123456789abcdef01234567 -X github.com/full-chaos/dev-health-acr/internal/version.Date=2026-07-10T14:00:00Z' -o "$tmp/acr-mcp" ./cmd/acr-mcp
source_identity_unchanged=false
if [[ "$(git -C "$root" rev-parse HEAD)" == "$source_revision" && -z "$(git -C "$root" status --porcelain)" ]]; then source_identity_unchanged=true; fi
[[ "$source_identity_unchanged" == true ]] || { printf 'canonical source identity changed during build\n' >&2; exit 1; }
token='fcacr_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA'
coproc MCP { cd "$repo" && ACR_API_URL="https://localhost:$port" ACR_API_TOKEN="$token" ACR_API_CA_BUNDLE="$tmp/ca.pem" ACR_LOCAL_INDEX_PROVIDER=codegraph ACR_CODEGRAPH_EXECUTABLE="$wrapper" ACR_LOCAL_INDEX_TIMEOUT=15s "$tmp/acr-mcp" serve 2> "$tmp/mcp.err"; }
# shellcheck disable=SC2153
mcp_pid=${MCP_PID:-}
rpc() { printf '%s\n' "$1" >&"${MCP[1]}"; IFS= read -r -t 15 response <&"${MCP[0]}" || return 1; printf '%s\n' "$response" >> "$tmp/mcp.out"; }
rpc '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"live-fixture","version":"1"}}}'
printf '%s\n' '{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}' >&"${MCP[1]}"
rpc '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}'
rpc '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"context_for_task","arguments":{"goal":"CodeGraph MCP live context"}}}'
rpc '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"source_evidence","arguments":{"evidence_ref_id":"ev_01J0ACR001"}}}'
local_id="$(python3 - "$tmp/mcp.out" <<'PY'
import json,sys
for line in open(sys.argv[1]):
 x=json.loads(line)
 if x.get('id')==3:
  refs=x.get('result',{}).get('structuredContent',{}).get('local_context',{}).get('evidence_refs',[])
  if refs: print(refs[0]['evidence_ref_id'])
PY
)"
if [[ -z "$local_id" ]]; then
  python3 - "$tmp/mcp.out" "$command_log" <<'PY' >&2
import json,sys
commands={}
for line in open(sys.argv[2],encoding='utf-8'):
 name=line.strip(); commands[name]=commands.get(name,0)+1
for line in open(sys.argv[1],encoding='utf-8'):
 x=json.loads(line)
 if x.get('id')==3:
  result=x.get('result',{}); local=result.get('structuredContent',{}).get('local_context',{})
  structured=result.get('structuredContent',{})
  print('live CodeGraph produced no evidence id: tool_error=%s structured=%s keys=%s status=%s warnings=%d commands=%s' % (bool(result.get('isError')),isinstance(structured,dict),','.join(sorted(structured)) if isinstance(structured,dict) else 'none',local.get('status','absent'),len(local.get('warnings',[])),json.dumps(commands,sort_keys=True,separators=(',',':'))))
  break
PY
  exit 1
fi
rpc "$(python3 - "$local_id" <<'PY'
import json,sys
print(json.dumps({'jsonrpc':'2.0','id':5,'method':'tools/call','params':{'name':'source_evidence','arguments':{'evidence_ref_id':sys.argv[1]}}},separators=(',',':')))
PY
)"
# shellcheck disable=SC1083
eval "exec ${MCP[1]}>&-"
wait "$mcp_pid"
mcp_pid=""

after_identity="$(db_identity)"
status_after="$(status_hash)"
workspace_head_after="$(git -C "$repo" rev-parse HEAD)"
validate_status
# Detect persistent target/DB replacement before and after the live session;
# this snapshot guard does not claim to close every swap-and-restore race.
[[ "$before_identity" == "$after_identity" && "$status_before" == "$status_after" ]] || exit 1

SOURCE_ROOT="$root" SOURCE_REVISION="$source_revision" SOURCE_IDENTITY_UNCHANGED="$source_identity_unchanged" HARNESS_SHA256="$(shasum -a 256 "$0" | awk '{print $1}')" BINARY_SHA256="$(shasum -a 256 "$tmp/acr-mcp" | awk '{print $1}')" WORKSPACE_HEAD_BEFORE="$workspace_head_before" WORKSPACE_HEAD_AFTER="$workspace_head_after" RECEIPT_MODE="${ACR_E2E_RECEIPT_MODE:-live}" INDEX_KIND="${ACR_E2E_INDEX_KIND:-live}" python3 - "$tmp/mcp.out" "$command_log" "$live_receipt" "$before_identity" "$after_identity" "$status_before" "$status_after" <<'PY'
import collections,json,sys
lines=[]
for line in open(sys.argv[1],encoding='utf-8'):
 try: lines.append(json.loads(line))
 except json.JSONDecodeError: raise SystemExit('malformed MCP framing')
by_id={x.get('id'):x for x in lines if 'id' in x}
def payload(request_id):
 result=by_id.get(request_id,{}).get('result',{})
 if result.get('isError'): raise SystemExit('MCP tool returned error')
 value=result.get('structuredContent')
 if not isinstance(value,dict): raise SystemExit('MCP tool omitted structured content')
 return value
tools=by_id.get(2,{}).get('result',{}).get('tools',[])
if {x.get('name') for x in tools}!={'context_for_task','source_evidence'} or len(tools)!=2: raise SystemExit('expected exactly two read-only tools')
context,hosted,local=payload(3),payload(4),payload(5)
packet=context.get('structured',{}); hosted_ref=hosted.get('structured',{}).get('evidence',{}); local_ref=local.get('structured',{}).get('evidence',{})
if packet.get('context_packet_id')!='pkt_01J0ACR001': raise SystemExit('hosted packet changed')
if hosted_ref.get('evidence_ref_id')!='ev_01J0ACR001': raise SystemExit('hosted expansion mismatch')
if not local_ref.get('evidence_ref_id','').startswith('local:codegraph:v1:'): raise SystemExit('local expansion mismatch')
if len({hosted_ref['evidence_ref_id'],local_ref['evidence_ref_id']})!=2: raise SystemExit('evidence identifiers collide')
local_context=context.get('local_context',{})
if not local_context.get('warnings') is not None or not local_context.get('evidence_refs',[{}])[0].get('provenance'): raise SystemExit('local warnings or provenance missing')
content_bytes=len(json.dumps({'structured':packet},separators=(',',':')).encode())+len(json.dumps({'local_context':local_context},separators=(',',':')).encode())
budget=packet.get('budget',{}).get('max_serialized_bytes',0)
if not 0<content_bytes<=budget: raise SystemExit('packet content budget exceeded')
allowed={'status','query','callers','callees','impact','affected','files'}
counts=collections.Counter(line.strip() for line in open(sys.argv[2],encoding='utf-8') if line.strip())
if set(counts)-allowed or not counts.get('status') or not counts.get('query'): raise SystemExit('unexpected or incomplete CodeGraph command audit')
before=json.loads(sys.argv[4]); after=json.loads(sys.argv[5])
if before!=after: raise SystemExit('real index changed')
import os
workspace_before=os.environ['WORKSPACE_HEAD_BEFORE']; workspace_after=os.environ['WORKSPACE_HEAD_AFTER']
if workspace_before!=workspace_after: raise SystemExit('workspace HEAD changed')
r={'schema_version':'context_fabric_mcp_codegraph_receipt.v1','task':'CHAOS-3007 Task 9','mode':os.environ['RECEIPT_MODE'],'scenario':'mixed','verdict':'pass','source_revision':os.environ['SOURCE_REVISION'],'source_worktree_clean':True,'source_identity_unchanged':os.environ['SOURCE_IDENTITY_UNCHANGED']=='true','harness_sha256':os.environ['HARNESS_SHA256'],'binary_sha256':os.environ['BINARY_SHA256'],'tls_verified':True,'workspace_head_before':workspace_before,'workspace_head_after':workspace_after,'workspace_head_unchanged':workspace_before==workspace_after,'indexed_commit_state':'unknown','mcp':{'framing':bool(lines),'initialize':bool(by_id.get(1,{}).get('result')),'initialized_notification':True,'tools':len(tools),'record_episode_present':'record_episode' in {x.get('name') for x in tools},'context_ok':True,'hosted_expand_ok':True,'local_expand_ok':True},'federation':{'hosted_packet_unchanged':True,'ids_disjoint':True,'packet_content_within_budget':True,'envelope_excluded':True,'federated_budget_excluded':True,'rendered_markdown_excluded':True},'codegraph':{'command_counts':dict(sorted(counts.items())),'forbidden_command_count':0,'status_before_sha256':sys.argv[6],'status_after_sha256':sys.argv[7],'index_before':before,'index_after':after,'persistent_identity_replacement_detected':False,'index_kind':os.environ['INDEX_KIND']},'cleanup':{'processes_stopped':True,'listeners_stopped':True,'temporary_material_removed':True}}
import os,subprocess,tempfile
root=os.environ['SOURCE_ROOT']
fd,temporary=tempfile.mkstemp(prefix='.context-fabric-09-live-',dir=os.path.dirname(sys.argv[3]))
try:
 os.fchmod(fd,0o600)
 with os.fdopen(fd,'w',encoding='utf-8') as output: json.dump(r,output,sort_keys=True,separators=(',',':')); output.write('\n'); output.flush(); os.fsync(output.fileno())
 hook=os.environ.get('ACR_E2E_RECEIPT_POST_FSYNC_HOOK')
 if hook: subprocess.run([hook],check=True)
 if subprocess.check_output(['git','-C',root,'rev-parse','HEAD'],text=True).strip()!=r['source_revision'] or subprocess.check_output(['git','-C',root,'status','--porcelain'],text=True): raise SystemExit('source changed before receipt publication')
 os.replace(temporary,sys.argv[3])
finally:
 if os.path.exists(temporary): os.unlink(temporary)
PY
