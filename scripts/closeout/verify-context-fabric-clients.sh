#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
root="$(cd "$script_dir/../.." && pwd -P)"
plan=""
evidence=""
reviews=""
check_only=0
self_test=0

usage() {
  printf '%s\n' 'Usage: verify-context-fabric-clients.sh --plan FILE --evidence DIR --check-only [--reviews DIR] [--root DIR] | --self-test'
}

while (($#)); do
  case "$1" in
    --plan) plan="${2:?missing value}"; shift 2 ;;
    --evidence) evidence="${2:?missing value}"; shift 2 ;;
    --reviews) reviews="${2:?missing value}"; shift 2 ;;
    --root) root="${2:?missing value}"; shift 2 ;;
    --check-only) check_only=1; shift ;;
    --self-test) self_test=1; shift ;;
    --help) usage; exit 0 ;;
    *) usage >&2; exit 2 ;;
  esac
done

snapshot() {
  printf '%s\n%s\n%s\n' \
    "$(git -C "$root" rev-parse HEAD)" \
    "$(git -C "$root" status --porcelain=v1)" \
    "$(git -C "$root" diff --no-ext-diff | shasum -a 256 | awk '{print $1}')"
}

before="$(snapshot)"
finish() {
  local status=$?
  local after
  after="$(snapshot)"
  if [[ "$before" != "$after" ]]; then
    printf '%s\n' 'FAIL: verifier changed HEAD, tree, or status' >&2
    exit 1
  fi
  exit "$status"
}
trap finish EXIT

if ((self_test)); then
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"; finish' EXIT
  cp -R "$root/.omo/evidence" "$tmp/evidence"
  export CONTEXT_FABRIC_CLOSEOUT_SELFTEST=1
  bash "$0" --root "$root" --plan "$root/.omo/plans/context-fabric-clients.md" --evidence "$tmp/evidence" --check-only >/dev/null
  rm "$tmp/evidence/context-fabric-08-no-upload.json"
  if bash "$0" --root "$root" --plan "$root/.omo/plans/context-fabric-clients.md" --evidence "$tmp/evidence" --check-only >/dev/null 2>&1; then exit 1; fi
  cp "$root/.omo/evidence/context-fabric-08-no-upload.json" "$tmp/evidence/"
  printf 'tamper\n' >>"$tmp/evidence/context-fabric-08-no-upload.json"
  if bash "$0" --root "$root" --plan "$root/.omo/plans/context-fabric-clients.md" --evidence "$tmp/evidence" --check-only >/dev/null 2>&1; then exit 1; fi
  cp "$root/.omo/evidence/context-fabric-08-no-upload.json" "$tmp/evidence/"
  python3 - "$tmp/evidence/context-fabric-17-conformance.json" <<'PY'
import json,sys
p=sys.argv[1]; value=json.load(open(p)); value['results']['cursor']='installed'; open(p,'w').write(json.dumps(value))
PY
  if bash "$0" --root "$root" --plan "$root/.omo/plans/context-fabric-clients.md" --evidence "$tmp/evidence" --check-only >/dev/null 2>&1; then exit 1; fi
  printf '%s\n' 'CONTEXT_FABRIC_CLIENT_CLOSEOUT_SELF_TEST_OK cases=missing,hash_tamper,unsupported_cursor'
  exit 0
fi

[[ -n "$plan" && -n "$evidence" && "$check_only" == 1 ]] || { usage >&2; exit 2; }
[[ -f "$plan" && -d "$evidence" ]] || { printf '%s\n' 'FAIL: plan or evidence is missing' >&2; exit 1; }
[[ -z "$reviews" || -d "$reviews" ]] || { printf '%s\n' 'FAIL: reviews directory is missing' >&2; exit 1; }
[[ -n "${CONTEXT_FABRIC_CLOSEOUT_SELFTEST:-}" || -z "$(git -C "$root" status --porcelain=v1)" ]] || { printf '%s\n' 'FAIL: dirty source worktree' >&2; exit 1; }

python3 - "$root" "$plan" "$evidence" <<'PY'
import hashlib,json,re,subprocess,sys
root,plan,evidence=sys.argv[1:]
expected={
 '1':('context-fabric-01-codegraph-contract.md','73abfdc16d53253c52c046e067e6eb083a1293eb2e714d26c2a54c1787a491a9'),
 '2':('context-fabric-02-provider-contract.txt','f43b0d83191c55e9f60c164700b72e2cc1b0410b55576a930b09797cd15fa9c8'),
 '3':('context-fabric-03-local-config-runner.txt','ef17e636e0791e4ef6f18f798114a72c594258f7f7102140a22b1eaa8507ce93'),
 '4':('context-fabric-04-codegraph-adapter.txt','a5cbfa7e4394f51ef00051c1c6636961551a8512f66711cbeca513f6d575f25b'),
 '5':('context-fabric-05-freshness-errors.json','138e2a62806f399e33a64b27c72a5f73f87c8eefa7c5d873d4af02a2a68e21d9'),
 '6':('context-fabric-06-mcp-contracts.txt','49e74a3fc624b08666c48f76efdf4755e89e93f8a8ae1a4c77e72dc0220fa33e'),
 '7':('context-fabric-07-federation-routing.json','e45b56e89d50e6674691d6aea3545ffbb34facebde370862a4511cea87ee1299'),
 '8':('context-fabric-08-no-upload.json','301842d9e6d317a3fdd7a2c97134e3b7620212cac9f59180bcc05528db065bbd'),
 '9':('context-fabric-09-mixed-mcp.json','3f9bfb7817a49f28ede4f13d26e68926fff8de570cc227cee47ea75afc7f7173'),
 '10':('context-fabric-10-doctor-diagnostics.json','d0e552d29d516c7a3870b5a20964f47ce14c8b0f39b1c759dea148b314f2489c'),
 '11':('context-fabric-11-chaos-3007-closeout.md','0439ced1a585457ad10192d32431b68473466721994b67b41f7eed8bee63273d'),
 '12':('context-fabric-12-client-contract.json','ecf3c894075066b423c249f08ca8038b44d2be58a82a4a83b4fc4fc752e9ab3c'),
 '13':('context-fabric-13-opencode.json','7eed49e126125faf3b49bf3cd52ff7f1061e1a29cf31a6a1df35f375707cc930'),
 '14':('task-14-acr-project-completion.json','be43630dd600927b252b2da63d8cdc75002835b0a7465787f03d5c869517bc36'),
 '17':('context-fabric-17-conformance.json','715d8c193ae437c97815f4dd03dba25787381cccd7d7ca18be30739d210a6301'),
 '18':('context-fabric-18-release-bundle.txt','ee5b17f5b1c78b0ded58c9739a061a0a380dc145748091c4f6e184932cf564df'),
 '19':('context-fabric-19-client-docs.md','d81ecdc18e992436348ae2e5bd0abdb9328a4ef3c25afde19397917681a7cb73'),
}
def fail(message): raise SystemExit('FAIL: '+message)
for task,(name,want) in expected.items():
 p=f'{evidence}/{name}'
 try: got=hashlib.sha256(open(p,'rb').read()).hexdigest()
 except FileNotFoundError: fail(f'Task {task} receipt missing: {name}')
 if got != want: fail(f'Task {task} receipt hash mismatch: {name}')
for client in ('claude-code','codex','cursor'):
 p=f'{root}/clients/{client}/package.v1.json'
 if not __import__('os').path.isfile(p): fail(f'Task { {"claude-code":"14","codex":"15","cursor":"16"}[client] } package missing')
 package=json.load(open(p))
 if package.get('command')!='acr-mcp' or package.get('args')!=['serve']: fail(f'Task package is not exact acr-mcp serve: {client}')
privacy=json.load(open(f'{evidence}/context-fabric-08-no-upload.json'))
mixed=json.load(open(f'{evidence}/context-fabric-09-mixed-mcp.json'))
conformance=json.load(open(f'{evidence}/context-fabric-17-conformance.json'))
if privacy.get('verdict')!='pass' or privacy.get('local_expansion_hosted_request_count') != 0: fail('Task 8 privacy receipt failed')
if mixed.get('verdict')!='pass' or mixed.get('scenario')!='mixed' or mixed.get('mcp',{}).get('record_episode_rejected') is not True: fail('Task 9 mixed MCP receipt failed')
if conformance.get('results',{}).get('cursor') != 'not_installed' or conformance.get('results',{}).get('windows') != 'deferred': fail('Task 17 Cursor conditional-state receipt failed')
release=open(f'{evidence}/context-fabric-18-release-bundle.txt',encoding='utf-8').read()
if 'two_builds=byte_identical' not in release or 'release_verify_build_1=pass' not in release or 'client_tamper_negative=pass' not in release: fail('Task 18 release receipt failed')
docs=open(f'{evidence}/context-fabric-19-client-docs.md',encoding='utf-8').read()
if 'Cursor native client: `cursor_client=not_installed`' not in docs or 'Documentation anchors and Windows credential guidance self-tests: PASS.' not in docs: fail('Task 19 documentation receipt failed')
head=subprocess.check_output(['git','-C',root,'rev-parse','HEAD'],text=True).strip()
for name,_ in expected.values():
 text=open(f'{evidence}/{name}',encoding='utf-8',errors='ignore').read()
 for commit in set(re.findall(r'(?im)(?:source_revision|source_head|source revision|final full sha|^head)\s*[":=` ]+([0-9a-f]{7,40})',text)):
  if subprocess.run(['git','-C',root,'cat-file','-e',commit+'^{commit}'],capture_output=True).returncode == 0 and subprocess.run(['git','-C',root,'merge-base','--is-ancestor',commit,head],capture_output=True).returncode != 0: fail(f'stale/non-ancestor source: {commit}')
print('CONTEXT_FABRIC_CLIENT_CLOSEOUT_OK tasks=1-19 mode=pre-final external_status_mutated=false')
PY
