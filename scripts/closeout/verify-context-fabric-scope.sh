#!/usr/bin/env bash
set -euo pipefail
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
root="$(cd "$script_dir/../.." && pwd -P)"
evidence=""
release_dir=""
self_test=0
while (($#)); do
  case "$1" in
    --root) root="${2:?missing value}"; shift 2 ;;
    --evidence) evidence="${2:?missing value}"; shift 2 ;;
    --release-dir) release_dir="${2:?missing value}"; shift 2 ;;
    --self-test) self_test=1; shift ;;
    --help) printf '%s\n' 'Usage: verify-context-fabric-scope.sh [--root DIR] [--evidence DIR --release-dir DIR] | --self-test'; exit 0 ;;
    *) exit 2 ;;
  esac
done
snapshot() {
  if git -C "$root" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    printf '%s\n%s\n' "$(git -C "$root" rev-parse HEAD)" "$(git -C "$root" status --porcelain=v1)"
  else
    printf '%s\n' fixture-root
  fi
}
before="$(snapshot)"
status=0
trap 'status=$?; [[ "$before" == "$(snapshot)" ]] || { printf "%s\\n" "FAIL: verifier changed HEAD or status" >&2; exit 1; }; exit "$status"' EXIT

if ((self_test)); then
  tmp="$(mktemp -d)"
  mkdir -p "$tmp/root/docs" "$tmp/evidence"
  printf 'safe text\n' >"$tmp/root/docs/safe.md"
  git -C "$root" worktree list >/dev/null
  for case in direct-api codegraph-mutation preplan-default cursor-claim absolute-path status-mutation; do
    cp -R "$tmp/root" "$tmp/$case"
    case "$case" in
      direct-api) printf 'call the hosted API directly\n' >>"$tmp/$case/docs/safe.md" ;;
      codegraph-mutation) printf 'CodeGraph init\n' >>"$tmp/$case/docs/safe.md" ;;
      preplan-default) printf 'pre-plan enabled by default\n' >>"$tmp/$case/docs/safe.md" ;;
      cursor-claim) printf 'Cursor native smoke passed\n' >>"$tmp/$case/docs/safe.md" ;;
      absolute-path) printf '/Users/example/private\n' >>"$tmp/$case/docs/safe.md" ;;
      status-mutation) printf 'status transition applied\n' >>"$tmp/$case/docs/safe.md" ;;
    esac
    if "$0" --root "$tmp/$case" >/dev/null 2>&1; then printf 'self-test failed: %s\n' "$case" >&2; exit 1; fi
  done
  rm -rf "$tmp"
  printf '%s\n' 'CONTEXT_FABRIC_SCOPE_SELF_TEST_OK cases=direct-api,codegraph-mutation,preplan,cursor,absolute-path,status-mutation'
  exit 0
fi

python3 - "$root" "$evidence" "$release_dir" <<'PY'
import json,os,re,subprocess,sys
root,evidence,release=sys.argv[1:]
def fail(x): raise SystemExit('FAIL: '+x)
patterns={
 'parser/index/SQLite implementation':r'(?i)(sql\.open|create table|modernc\.org/sqlite|sqlite3|local graph implementation)',
 'direct client API or CodeGraph call':r'(?i)(call the (hosted )?API directly|direct(ly)? call CodeGraph|direct client API)',
 'bare acr-mcp registration':r'(?m)^\s*(command|args)\s*[:=]\s*["\']?acr-mcp["\']?\s*$',
 'credentials or raw local upload':r'(?i)(copy credentials? into|credential.*project (file|config)|raw local upload|upload.*(source|index))',
 'default pre-plan/writeback':r'(?i)(pre-plan|writeback).{0,50}(enabled by default|default enabled)',
 'unsupported Cursor claim':r'(?i)(Cursor (native |real )?(smoke|execution|validation).{0,30}(passed|complete|supported))',
 'absolute path disclosure':r'/(Users|home|tmp|var|opt|etc)/',
 'status mutation':r'(?i)(status transition applied|mark(ed)? (CHAOS-3007|CHAOS-3010) done|reopen(ed)? CHAOS-(2946|2941))',
}
files=[]
if subprocess.run(['git','-C',root,'rev-parse','--is-inside-work-tree'],capture_output=True).returncode == 0:
 changed=subprocess.check_output(['git','-C',root,'diff','--name-only','1e1c267e50432647c2c59fe7cf5bf38c1f565caa','--'],text=True).splitlines()
 changed += subprocess.check_output(['git','-C',root,'ls-files','--others','--exclude-standard'],text=True).splitlines()
 files=[os.path.join(root,path) for path in changed if os.path.isfile(os.path.join(root,path)) and not path.startswith('scripts/closeout/')]
else:
 for base in ('cmd','internal','clients','contracts','docs','scripts'):
  p=os.path.join(root,base)
  if os.path.isdir(p):
   for dp,_,names in os.walk(p):
    if '/closeout' in dp or (base=='scripts' and any(part in dp for part in ('/test','/verify','/docs'))): continue
    for name in names:
     if name.endswith(('.go','.md','.json','.sh','.ts','.yaml','.yml')) and not name.endswith('_test.go') and not name.startswith('test-'): files.append(os.path.join(dp,name))
for label,pattern in patterns.items():
 for path in files:
  text=open(path,encoding='utf-8',errors='ignore').read()
  if re.search(pattern,text): fail(f'{label}: {os.path.relpath(path,root)}')
if not evidence:
 print('CONTEXT_FABRIC_SCOPE_OK mode=pre-final f1_f5=pending external_status_mutated=false')
 raise SystemExit(0)
if not release: fail('final mode requires --release-dir')
required=('context-fabric-final-f1-review.md','context-fabric-final-f2-live-codegraph.json','context-fabric-final-f3-clients.json','context-fabric-final-f4-parity.txt')
for name in required:
 p=os.path.join(evidence,name)
 if not os.path.isfile(p) or not os.path.getsize(p): fail('missing final receipt: '+name)
 text=open(p,encoding='utf-8',errors='ignore').read().lower()
 if not re.search(r'\b(verdict|result|status)\s*[":= ]+\s*(pass|approved?)\b',text): fail('failed final receipt: '+name)
 if 'source_revision' not in text: fail('stale final receipt: '+name)
f2=json.load(open(os.path.join(evidence,'context-fabric-final-f2-live-codegraph.json')))
if f2.get('verdict') != 'pass' or f2.get('index_before_sha256') != f2.get('index_after_sha256'): fail('invalid F2 live-CodeGraph receipt')
if not os.path.isdir(release): fail('missing final release directory')
print('CONTEXT_FABRIC_SCOPE_OK mode=final f1_f4=validated')
PY
