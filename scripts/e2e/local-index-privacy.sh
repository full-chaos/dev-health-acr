#!/usr/bin/env bash
set -euo pipefail

scenario="${2:-}"
if [[ "${1:-}" != "--scenario" || ! "$scenario" =~ ^(no-upload|injected-source-leak|injected-path-leak|forced-invariant-failure)$ ]]; then
  printf 'usage: %s --scenario {no-upload|injected-source-leak|injected-path-leak|forced-invariant-failure}\n' "$0" >&2
  exit 64
fi

root="$(cd "$(dirname "$0")/../.." && pwd -P)"
canonical_scenario="no-upload"
canonical_receipt="$root/.omo/evidence/context-fabric-08-no-upload.json"
scenario_ledger="$root/.omo/evidence/context-fabric-08-scenarios.jsonl"
source_revision="$(git -C "$root" rev-parse HEAD)"
[[ -z "$(git -C "$root" status --porcelain)" ]] || { printf 'canonical source worktree must be clean\n' >&2; exit 1; }
tmp="$(mktemp -d "${TMPDIR:-/tmp}/acr-local-index-privacy.XXXXXX")"
capture="$tmp/capture.jsonl"
receipt="$canonical_receipt"
server_pid=""
source_sentinel="local_source_sentinel:${RANDOM}${RANDOM}"
index_sentinel="local_index_sentinel:${RANDOM}${RANDOM}"
graph_payload_sentinel="graph_payload_sentinel:${RANDOM}${RANDOM}"
local_locator_sentinel="local_locator_sentinel:${RANDOM}${RANDOM}"
absolute_root_sentinel="$tmp/workspace"
raw_boundary_probe="$tmp/raw-boundary-probe"
expansion_counts="$tmp/expansion-counts"

cleanup() {
  [[ -z "$server_pid" ]] || kill "$server_pid" 2>/dev/null || true
  [[ -z "$server_pid" ]] || wait "$server_pid" 2>/dev/null || true
  rm -rf "$tmp" "$root/.tmp/local-index-privacy-driver.go"
}
trap cleanup EXIT INT TERM

mkdir -p "$tmp/workspace/.codegraph" "$root/.omo/evidence" "$root/.tmp"
if [[ "$scenario" == "$canonical_scenario" ]]; then rm -f "$receipt"; fi
if [[ "$scenario" == "$canonical_scenario" && "${ACR_E2E_FORCE_CANONICAL_FAILURE:-}" == 1 ]]; then exit 1; fi
printf '%s\n' "$source_sentinel" >"$tmp/workspace/local-source.txt"
printf '%s\n' "$index_sentinel" >"$tmp/workspace/.codegraph/index.bin"
printf '%s\n' "$index_sentinel" >"$tmp/workspace/.codegraph/codegraph.db"
git -C "$tmp/workspace" init -q
git -C "$tmp/workspace" config user.email privacy@example.invalid
git -C "$tmp/workspace" config user.name privacy
git -C "$tmp/workspace" add local-source.txt .codegraph/index.bin .codegraph/codegraph.db
git -C "$tmp/workspace" commit -qm privacy-fixture
git -C "$tmp/workspace" remote add origin https://github.com/acme/widgets.git
workspace_branch="$(git -C "$tmp/workspace" branch --show-current)"

cat >"$tmp/codegraph" <<EOF
#!/usr/bin/env bash
set -euo pipefail
case "\$1" in
  status) printf '{"initialized":true,"version":"1.2.0","projectPath":"%s","indexPath":"%s/.codegraph","lastIndexed":"$(date -u +%Y-%m-%dT%H:%M:%SZ)","fileCount":1,"nodeCount":1,"edgeCount":0,"dbSizeBytes":1,"backend":"sqlite","journalMode":"wal","nodesByKind":{},"languages":[],"pendingChanges":{"added":0,"modified":0,"removed":0},"worktreeMismatch":null,"index":{"reindexRecommended":false,"builtWithVersion":"1.2.0","builtWithExtractionVersion":1,"currentExtractionVersion":1}}\n' "\$PWD" "\$PWD" ;;
  query) grep -Fqx "$source_sentinel" local-source.txt && grep -Fqx "$index_sentinel" .codegraph/index.bin && printf 'raw-boundary-exercised\n' > "$raw_boundary_probe"; printf '[{"node":{"id":"%s","kind":"function","name":"%s %s %s","qualifiedName":"privacy.Local","filePath":"local-source.txt","startLine":1,"endLine":1,"startColumn":0,"endColumn":1,"language":"Go","signature":"func Local()","updatedAt":0,"isExported":true,"isAsync":false,"isStatic":false,"isAbstract":false,"visibility":null},"score":1,"privacy_source_raw":"%s","privacy_index_raw":"%s"}]\n' "$local_locator_sentinel" "$graph_payload_sentinel" "$local_locator_sentinel" "$absolute_root_sentinel" "$source_sentinel" "$index_sentinel" ;;
  callers) printf '{"symbol":"privacy.Local","callers":[]}\n' ;;
  callees) printf '{"symbol":"privacy.Local","callees":[]}\n' ;;
  impact) printf '{"symbol":"privacy.Local","depth":2,"nodeCount":0,"edgeCount":0,"affected":[]}\n' ;;
  *) printf '[]\n' ;;
esac
EOF
chmod 700 "$tmp/codegraph"

openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj /CN=localhost \
  -addext 'subjectAltName=DNS:localhost,IP:127.0.0.1' -keyout "$tmp/key.pem" -out "$tmp/ca.pem" >/dev/null 2>&1

cat >"$tmp/server.py" <<'PY'
import http.server, json, os, ssl
from pathlib import Path
root=Path(os.environ['PRIVACY_ROOT']); capture=Path(os.environ['PRIVACY_CAPTURE'])
class Handler(http.server.BaseHTTPRequestHandler):
 def log_message(self,*args): pass
 def do_GET(self): self.respond()
 def do_POST(self): self.respond()
 def respond(self):
  body=self.rfile.read(int(self.headers.get('Content-Length','0'))).decode()
  with capture.open('a') as f: f.write(json.dumps({'method':self.command,'path':self.path,'headers':dict(self.headers),'body':body})+'\n')
  if self.path.endswith('/capabilities'): data=(root/'contracts/examples/v1/capabilities.v1.json').read_bytes()
  elif '/evidence/' in self.path: data=(root/'contracts/examples/v1/expanded_evidence.v1.json').read_bytes()
  elif self.path.endswith('/context-packets'): data=(root/'contracts/examples/v1/context_packet.v1.json').read_bytes()
  else: self.send_error(404); return
  self.send_response(200); self.send_header('Content-Type','application/json'); self.send_header('Content-Length',str(len(data))); self.end_headers(); self.wfile.write(data)
s=http.server.ThreadingHTTPServer(('127.0.0.1',0),Handler)
tls=ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
tls.load_cert_chain(os.environ['PRIVACY_CERT'],os.environ['PRIVACY_KEY'])
s.socket=tls.wrap_socket(s.socket,server_side=True)
Path(os.environ['PRIVACY_PORT']).write_text(str(s.server_port))
s.serve_forever()
PY
PRIVACY_ROOT="$root" PRIVACY_CAPTURE="$capture" PRIVACY_PORT="$tmp/port" PRIVACY_CERT="$tmp/ca.pem" PRIVACY_KEY="$tmp/key.pem" python3 "$tmp/server.py" &
server_pid=$!
for _ in {1..50}; do [[ -s "$tmp/port" ]] && break; sleep 0.1; done
[[ -s "$tmp/port" ]] || { printf 'privacy fixture did not start\n' >&2; exit 1; }

go -C "$root" build -o "$tmp/acr-mcp" ./cmd/acr-mcp
source_identity_unchanged=false
if [[ "$(git -C "$root" rev-parse HEAD)" == "$source_revision" && -z "$(git -C "$root" status --porcelain)" ]]; then source_identity_unchanged=true; fi
[[ "$source_identity_unchanged" == true ]] || { printf 'canonical source identity changed during build\n' >&2; exit 1; }
cat >"$root/.tmp/local-index-privacy-driver.go" <<'GO'
package main
import("context";"encoding/json";"os";"os/exec";"strconv";"strings";"time"; mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp")
func mustContain(payload []byte, values ...string){for _,value:=range values{if value==""||!strings.Contains(string(payload),value){panic("local sentinel missing")}}}
func hostedEvidenceCount(path string)int{data,err:=os.ReadFile(path);if err!=nil{panic("capture unavailable")};return strings.Count(string(data),"/api/v1/agent-context/evidence/")}
func main(){graph,locator,workspace:=os.Getenv("GRAPH_PAYLOAD_SENTINEL"),os.Getenv("LOCAL_LOCATOR_SENTINEL"),os.Getenv("ABSOLUTE_ROOT_SENTINEL");ctx,c:=context.WithTimeout(context.Background(),20*time.Second);defer c();cmd:=exec.CommandContext(ctx,os.Getenv("PRIVACY_MCP"),"serve");cmd.Dir=os.Getenv("PRIVACY_WORKSPACE");cmd.Env=os.Environ();cl:=mcpsdk.NewClient(&mcpsdk.Implementation{Name:"privacy",Version:"1.0.0"},nil);s,e:=cl.Connect(ctx,&mcpsdk.CommandTransport{Command:cmd},nil);if e!=nil{panic(e)};defer s.Close();r,e:=s.CallTool(ctx,&mcpsdk.CallToolParams{Name:"context_for_task",Arguments:map[string]any{"goal":"privacy probe","repository":map[string]any{"slug":"acme/widgets"},"scope":map[string]any{"branch":os.Getenv("PRIVACY_BRANCH")}}});if e!=nil||r.IsError{panic("context failed")};if marker,err:=os.ReadFile(os.Getenv("PRIVACY_RAW_BOUNDARY_PROBE"));err!=nil||string(marker)!="raw-boundary-exercised\n"{panic("raw boundary unexercised")};var response struct{LocalContext struct{EvidenceRefs []struct{EvidenceRefID string `json:"evidence_ref_id"`} `json:"evidence_refs"`} `json:"local_context"`};raw,_:=json.Marshal(r.StructuredContent);if json.Unmarshal(raw,&response)!=nil||len(response.LocalContext.EvidenceRefs)!=1{panic("local evidence missing")};mustContain(raw,graph,locator,workspace);before:=hostedEvidenceCount(os.Getenv("PRIVACY_CAPTURE"));r,e=s.CallTool(ctx,&mcpsdk.CallToolParams{Name:"source_evidence",Arguments:map[string]any{"evidence_ref_id":response.LocalContext.EvidenceRefs[0].EvidenceRefID}});if e!=nil||r.IsError{panic("local evidence failed")};after:=hostedEvidenceCount(os.Getenv("PRIVACY_CAPTURE"));if after!=before{panic("local evidence reached hosted")};expanded,_:=json.Marshal(r.StructuredContent);mustContain(expanded,graph,locator,workspace);r,e=s.CallTool(ctx,&mcpsdk.CallToolParams{Name:"source_evidence",Arguments:map[string]any{"evidence_ref_id":"ev_example_001"}});if e!=nil||r.IsError{panic("hosted evidence failed")};final:=hostedEvidenceCount(os.Getenv("PRIVACY_CAPTURE"));if final!=after+1{panic("hosted evidence count mismatch")};os.WriteFile(os.Getenv("PRIVACY_EXPANSION_COUNTS"),[]byte(strconv.Itoa(before)+","+strconv.Itoa(after)+","+strconv.Itoa(final)),0600)}
GO
port="$(<"$tmp/port")"
token="fcacr_$(python3 - <<'PY'
import base64
print(base64.urlsafe_b64encode(bytes([7])*32).decode().rstrip('='))
PY
)"
ACR_API_URL="https://localhost:$port" ACR_API_CA_BUNDLE="$tmp/ca.pem" ACR_API_TOKEN="$token" ACR_SIDECAR_VERSION=1.0.0 ACR_LOCAL_INDEX_PROVIDER=codegraph ACR_CODEGRAPH_EXECUTABLE="$tmp/codegraph" PRIVACY_MCP="$tmp/acr-mcp" PRIVACY_WORKSPACE="$tmp/workspace" PRIVACY_BRANCH="$workspace_branch" GRAPH_PAYLOAD_SENTINEL="$graph_payload_sentinel" LOCAL_LOCATOR_SENTINEL="$local_locator_sentinel" ABSOLUTE_ROOT_SENTINEL="$absolute_root_sentinel" PRIVACY_RAW_BOUNDARY_PROBE="$raw_boundary_probe" PRIVACY_CAPTURE="$capture" PRIVACY_EXPANSION_COUNTS="$expansion_counts" go -C "$root" run "$root/.tmp/local-index-privacy-driver.go"

ACR_API_TOKEN="$token" SOURCE_SENTINEL="$source_sentinel" INDEX_SENTINEL="$index_sentinel" GRAPH_PAYLOAD_SENTINEL="$graph_payload_sentinel" LOCAL_LOCATOR_SENTINEL="$local_locator_sentinel" ABSOLUTE_ROOT_SENTINEL="$absolute_root_sentinel" SOURCE_ROOT="$root" SOURCE_REVISION="$source_revision" SOURCE_CLEAN=true SOURCE_IDENTITY_UNCHANGED="$source_identity_unchanged" HARNESS_SHA256="$(shasum -a 256 "$0" | awk '{print $1}')" BINARY_SHA256="$(shasum -a 256 "$tmp/acr-mcp" | awk '{print $1}')" python3 - "$capture" "$scenario" "$receipt" "$expansion_counts" "$scenario_ledger" <<'PY'
import json,sys
capture,scenario,receipt,expansion_counts,ledger=sys.argv[1:]
rows=[json.loads(line) for line in open(capture)]
sentinels={'source':__import__('os').environ['SOURCE_SENTINEL'],'index':__import__('os').environ['INDEX_SENTINEL'],'absolute_root':__import__('os').environ['ABSOLUTE_ROOT_SENTINEL'],'graph_payload':__import__('os').environ['GRAPH_PAYLOAD_SENTINEL'],'local_locator':__import__('os').environ['LOCAL_LOCATOR_SENTINEL']}
def copy_records(records): return [json.loads(json.dumps(record)) for record in records]
def packet_record(records): return next(record for record in records if record['path'].endswith('/context-packets'))
def sentinel_matches(records):
 joined='\n'.join(json.dumps(record,sort_keys=True) for record in records)
 return [name for name,value in sentinels.items() if value in joined]
def inject(records, field, value):
 mutated=copy_records(records)
 packet=packet_record(mutated)
 body=json.loads(packet['body'])
 body[field]=value
 packet['body']=json.dumps(body,sort_keys=True,separators=(',',':'))
 return mutated
leaks=sentinel_matches(rows)
codes={'injected-source-leak':'local_source_negative_control','injected-path-leak':'local_root_negative_control'}
if scenario=='injected-source-leak': rows=inject(rows,'local_source_negative_control',sentinels['source']); leaks=sentinel_matches(rows)
if scenario=='injected-path-leak': rows=inject(rows,'local_root_negative_control',sentinels['absolute_root']); leaks=sentinel_matches(rows)
for name in ('graph_payload','local_locator'):
 if name not in sentinel_matches(inject(rows,'privacy_'+name+'_negative_control',sentinels[name])): raise SystemExit('in-memory verifier probe failed')
if leaks:
 if scenario=='no-upload': raise SystemExit('privacy verifier rejected sentinel_'+leaks[0])
 print('privacy verifier rejected '+codes[scenario])
 sys.exit(1)
before_local,after_local,after_hosted=(int(value) for value in open(expansion_counts).read().strip().split(','))
if after_local-before_local!=0 or after_hosted-after_local!=1: raise SystemExit('hosted evidence ordering mismatch')
counts={'capabilities':sum(r['method']=='GET' and r['path'].endswith('/capabilities') for r in rows),'context_packet':sum(r['method']=='POST' and r['path'].endswith('/context-packets') for r in rows),'hosted_evidence':sum(r['method']=='GET' and '/evidence/' in r['path'] for r in rows)}
unexpected=len(rows)-sum(counts.values())
authorization=sum(1 for r in rows if r['headers'].get('Authorization')=='Bearer '+__import__('os').environ.get('ACR_API_TOKEN',''))
token=__import__('os').environ.get('ACR_API_TOKEN','')
non_authorization_matches=sum(token in str(value) for row in rows for key,value in row['headers'].items() if key.lower()!='authorization')+sum(token in row['body'] for row in rows)
shape_valid=counts=={'capabilities':1,'context_packet':1,'hosted_evidence':1} and unexpected==0 and all((r['method'],r['path'].split('?')[0]) in {('GET','/api/v1/agent-context/capabilities'),('POST','/api/v1/agent-context/context-packets')} or (r['method']=='GET' and r['path'].startswith('/api/v1/agent-context/evidence/')) for r in rows)
verdict='pass' if not leaks and shape_valid and authorization==3 and non_authorization_matches==0 else 'reject'
if scenario=='forced-invariant-failure': verdict='reject'
safe={'schema_version':'context_fabric_privacy_receipt.v1','task':'CHAOS-3007 Task 8','mode':'local-index','scenario':scenario,'verdict':verdict,'source_revision':__import__('os').environ['SOURCE_REVISION'],'source_worktree_clean':__import__('os').environ['SOURCE_CLEAN']=='true','source_identity_unchanged':__import__('os').environ['SOURCE_IDENTITY_UNCHANGED']=='true','harness_sha256':__import__('os').environ['HARNESS_SHA256'],'binary_sha256':__import__('os').environ['BINARY_SHA256'],'tls_verified':True,'request_counts':{**counts,'unexpected':unexpected},'request_shape_valid':shape_valid,'local_expansion_hosted_request_count':after_local-before_local,'zero_match_counts':{k:(0 if k not in leaks else 1) for k in sentinels},'credential':{'authorization_header_count':authorization,'non_authorization_match_count':non_authorization_matches},'rejection_code':(codes.get(scenario,'sentinel_'+leaks[0] if leaks else 'verification_failed') if verdict=='reject' else '')}
if verdict!='pass':
 with open(ledger,'a',encoding='utf-8') as out: out.write(json.dumps({'task':safe['task'],'scenario':scenario,'result':'reject','actual_exit':1},sort_keys=True,separators=(',',':'))+'\n')
 print('privacy verifier failed')
 sys.exit(1)
import os,subprocess,tempfile
root=os.environ['SOURCE_ROOT']
fd,temporary=tempfile.mkstemp(prefix='.context-fabric-08-',dir=os.path.dirname(receipt))
try:
 os.fchmod(fd,0o600)
 with os.fdopen(fd,'w',encoding='utf-8') as out: json.dump(safe,out,sort_keys=True,separators=(',',':')); out.write('\n'); out.flush(); os.fsync(out.fileno())
 hook=os.environ.get('ACR_E2E_RECEIPT_POST_FSYNC_HOOK')
 if hook: subprocess.run([hook],check=True)
 if subprocess.check_output(['git','-C',root,'rev-parse','HEAD'],text=True).strip()!=safe['source_revision'] or subprocess.check_output(['git','-C',root,'status','--porcelain'],text=True): raise SystemExit('source changed before receipt publication')
 os.replace(temporary,receipt)
finally:
 if os.path.exists(temporary): os.unlink(temporary)
print('privacy receipt: pass')
PY
