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
  for case in direct-api codegraph-mutation preplan-default bare-registration credential-project cursor-claim absolute-path status-mutation; do
    cp -R "$tmp/root" "$tmp/$case"
    case "$case" in
      direct-api) printf 'call the hosted API directly\n' >>"$tmp/$case/docs/safe.md" ;;
      codegraph-mutation) printf 'CodeGraph init\n' >>"$tmp/$case/docs/safe.md" ;;
      preplan-default) printf 'pre-plan enabled by default\n' >>"$tmp/$case/docs/safe.md" ;;
      bare-registration) printf 'command: "acr-mcp"\n' >>"$tmp/$case/docs/safe.md" ;;
      credential-project) printf 'copy credentials into project config\n' >>"$tmp/$case/docs/safe.md" ;;
      cursor-claim) printf 'Cursor native smoke passed\n' >>"$tmp/$case/docs/safe.md" ;;
      absolute-path) printf '/Users/private-user/private\n' >>"$tmp/$case/docs/safe.md" ;;
      status-mutation) printf 'status transition applied\n' >>"$tmp/$case/docs/safe.md" ;;
    esac
    if bash "$0" --root "$tmp/$case" >/dev/null 2>&1; then printf 'self-test failed: %s\n' "$case" >&2; exit 1; fi
  done
  git_root="$tmp/git-root"
  mkdir -p "$git_root/docs"
  git -C "$git_root" init -q
  git -C "$git_root" config user.name scope-self-test
  git -C "$git_root" config user.email scope-self-test@example.invalid
  printf 'safe text\n' >"$git_root/docs/safe.md"
  git -C "$git_root" add docs/safe.md
  git -C "$git_root" commit -qm baseline
  git_base="$(git -C "$git_root" rev-parse HEAD)"
  printf 'still safe\n' >>"$git_root/docs/safe.md"
  CONTEXT_FABRIC_SCOPE_SELFTEST=1 CONTEXT_FABRIC_SCOPE_SELFTEST_BASE="$git_base" bash "$0" --root "$git_root" >/dev/null
  printf 'call the hosted API directly\n' >>"$git_root/docs/safe.md"
  if CONTEXT_FABRIC_SCOPE_SELFTEST=1 CONTEXT_FABRIC_SCOPE_SELFTEST_BASE="$git_base" bash "$0" --root "$git_root" >/dev/null 2>&1; then
    printf '%s\n' 'self-test failed: git-range' >&2
    exit 1
  fi
  rm -rf "$tmp"
  printf '%s\n' 'CONTEXT_FABRIC_SCOPE_SELF_TEST_OK cases=direct-api,codegraph-mutation,preplan,bare-registration,credential-project,cursor,absolute-path,status-mutation,git-range'
  exit 0
fi

scope_base='9a9626305dcbeffea9d08fad8ac6230147ae8724'
if [[ -n "${CONTEXT_FABRIC_SCOPE_SELFTEST_BASE:-}" ]]; then
  [[ "${CONTEXT_FABRIC_SCOPE_SELFTEST:-}" == 1 ]] || { printf '%s\n' 'FAIL: scope base override is self-test only' >&2; exit 1; }
  scope_base="$CONTEXT_FABRIC_SCOPE_SELFTEST_BASE"
fi
python3 - "$root" "$evidence" "$release_dir" "$scope_base" <<'PY'
import json,os,re,subprocess,sys
root,evidence,release,scope_base=sys.argv[1:]
def fail(x): raise SystemExit('FAIL: '+x)
patterns={
 'parser/index/SQLite implementation':r'(?i)(sql\.open|create table|modernc\.org/sqlite|sqlite3|local graph implementation)',
 'CodeGraph mutation command':r'(?im)^\s*(?:\$\s*)?codegraph\s+(init|index|sync)\b',
 'direct client API or CodeGraph call':r'(?i)(call the (hosted )?API directly|direct(ly)? call CodeGraph|direct client API)',
 'credentials or raw local upload':r'(?i)(\b(copy|store|write|put)\s+(the\s+)?credentials?\s+(into|in|to)\s+(a\s+|the\s+)?project (file|config(?:uration)?)\b|raw local upload)',
 'default pre-plan/writeback':r'(?i)(pre-plan|writeback).{0,50}(enabled by default|default enabled)',
 'unsupported Cursor claim':r'(?i)(Cursor (native |real )?(smoke|execution|validation).{0,30}(passed|complete|supported))',
 'absolute path disclosure':r'/(Users|home)/(?!example(?:/|$)|you(?:/|$)|user(?:/|$)|<user>(?:/|$))[A-Za-z0-9._-]+/',
 'status mutation':r'(?i)(status transition applied|mark(ed)? (CHAOS-3007|CHAOS-3010) done|reopen(ed)? CHAOS-(2946|2941))',
}
files=[]
def scan_candidate(path):
 normalized=path.replace(os.sep,'/')
 name=os.path.basename(normalized)
 if normalized.startswith(('scripts/','testdata/','clients/conformance/fixtures/')): return False
 if name.endswith('_test.go') or name.startswith('test-'): return False
 return True
if subprocess.run(['git','-C',root,'rev-parse','--is-inside-work-tree'],capture_output=True).returncode == 0:
 changed=subprocess.check_output(['git','-C',root,'diff','--name-only',scope_base,'--'],text=True).splitlines()
 changed += subprocess.check_output(['git','-C',root,'ls-files','--others','--exclude-standard'],text=True).splitlines()
 files=[os.path.join(root,path) for path in changed if os.path.isfile(os.path.join(root,path)) and scan_candidate(path)]
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
bare_command=re.compile(r'(?m)^\s*["\']?command["\']?\s*[:=]\s*["\']acr-mcp["\']\s*,?\s*$')
serve_args=re.compile(r'(?m)^\s*["\']?args["\']?\s*[:=]\s*\[\s*["\']serve["\']\s*\]\s*,?\s*$')
for path in files:
 text=open(path,encoding='utf-8',errors='ignore').read()
 for match in bare_command.finditer(text):
  if not serve_args.search(text[match.end():match.end()+256]): fail(f'bare acr-mcp registration: {os.path.relpath(path,root)}')
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
