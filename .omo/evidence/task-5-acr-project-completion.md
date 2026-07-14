# Task 5 evidence: private ACR deployment ownership architecture

**Plan:** `acr-project-completion`, Todo 5 (lines 126-132)
**Worktree:** `/Users/chris/projects/full-chaos/dev-health/worktrees/acr/acr-project-task-5` (branch `docs/acr-project-task-5-deployment-ownership`)
**Commit:** `docs(architecture): keep private ACR packaging isolated`

## Deliverables

1. `docs/adr/0004-deployment-ownership.md` — the deployment ownership ADR.
   Defines:
   - Ownership table: exact ACR-owned deploy paths (`deploy/helm/acr`,
     `deploy/kubernetes/acr/base` + overlays, `deploy/compose/acr.compose.yml`
     + `deploy/compose/root-compose.patch`) versus the operator-owned root
     `../compose.yml`, versus the read-only Ops chart pattern
     (`deploy/helm/dev-health`) and Ops's own internal ACR entitlement API
     code (`src/dev_health_ops/api/internal/acr.py`, application code, not a
     deployment artifact).
   - External Postgres/ClickHouse/Ops dependency declaration.
   - Existing-Secret-only credential contract.
   - Immutable private image requirement.
   - No-MCP-workload boundary.
   - Superseded-paths section naming the old Ops-owned direction
     (`acr-developer-deployment` plan, Todos 9-11, which proposed
     `../ops/deploy/helm/acr/` and `../ops/deploy/kubernetes/acr/`) and its
     replacement.
2. `scripts/deploy/verify-boundaries.sh` — offline, read-only boundary
   verifier (`--acr`, `--ops`, `--compose`).

## Sources read

- `AGENTS.md` (root + this repo)
- `docs/adr/0001-go-service-boundary.md`, `0002-auth-entitlements.md`,
  `0003-storage-and-evidence.md`
- `docs/prd-v2.1.md`
- `docs/threat-model.md`
- `docs/container-images.md`, `docs/repository-bootstrap.md`,
  `docs/release-policy.md`, `docs/service-shell.md`
- `.omo/plans/acr-project-completion.md` (Todos 2-11, this repo's main
  checkout; not present in this worktree — untracked/gitignored operator
  artifact)
- `.omo/plans/acr-developer-deployment.md` (superseded plan; located in
  the main ACR checkout, not this worktree) — confirmed superseded header:
  > **Superseded for remaining work:** `.omo/plans/acr-project-completion.md`
  > is the authoritative execution plan. Completed runtime/container
  > evidence remains valid; the old Ops-owned ACR packaging paths must not
  > be executed.
  Todos 9-11 of that plan proposed `../ops/deploy/helm/acr/`,
  `../ops/deploy/kubernetes/acr/`, and an Ops-owned `acr-db-init` path
  inside `dev-health-ops` — this ADR formally supersedes that direction.
- `../ops` (read-only inspection; not modified): `deploy/docker-compose/`,
  `deploy/docker-swarm/`, `deploy/helm/dev-health/`, `deploy/kubernetes/`,
  `docker/`. No path under `deploy/` or `docker/` currently names or
  packages ACR; the only Ops-side ACR references are application code
  (`src/dev_health_ops/api/internal/acr.py`,
  `src/dev_health_ops/service_credentials.py`, related tests/migrations/docs),
  never a deployment artifact.
- `../compose.yml` (read-only inspection; not modified): root operator
  Compose file with services `postgres`, `pgbouncer`, `clickhouse`,
  `valkey`, `mailpit`, `traefik`, `migrate`, `api`, `billing-edge`,
  `worker`, `worker-wi`, `worker-ingest`, `worker-heavy`, `beat`, `web`,
  `bugsink`. No ACR service, image, database, or route currently defined
  (expected — Todo 7 owns adding the ACR overlay).

## Baseline / fail-first proof

Verifier was implemented first and run against the real repos before the
ADR existed:

```text
$ bash scripts/deploy/verify-boundaries.sh --acr . --ops /Users/chris/projects/full-chaos/dev-health/ops --compose /Users/chris/projects/full-chaos/dev-health/compose.yml
BOUNDARY VIOLATION: missing deployment ownership ADR: docs/adr/0004-deployment-ownership.md
EXIT=1
```

After adding `docs/adr/0004-deployment-ownership.md`, the same command
passes (see "Happy path" below).

## Happy path

```text
$ bash scripts/deploy/verify-boundaries.sh --acr . --ops /Users/chris/projects/full-chaos/dev-health/ops --compose /Users/chris/projects/full-chaos/dev-health/compose.yml
ok: private ACR deployment ownership boundary intact
  ADR:     docs/adr/0004-deployment-ownership.md
  ACR:     /Users/chris/projects/full-chaos/dev-health/worktrees/acr/acr-project-task-5
  Ops:     /Users/chris/projects/full-chaos/dev-health/ops (no ACR-named artifact under deploy/ or docker/)
  Compose: /Users/chris/projects/full-chaos/dev-health/compose.yml (operator-owned, outside both repositories)
EXIT=0
```

Real Ops (`/Users/chris/projects/full-chaos/dev-health/ops`) and the real
root Compose file (`/Users/chris/projects/full-chaos/dev-health/compose.yml`)
were used directly; both were opened read-only and neither was modified
(`git status`/`git diff` in `../ops` unchecked here since it was never
touched — only read via `find`).

## Failure fixture: violation naming (required by plan Todo 5)

A disposable Ops fixture directory (outside any repository, under a
`mktemp -d` root, never inside `../ops`) was created containing exactly the
two example violation shapes named in the plan:

```text
$ FIXTURE=$(mktemp -d)/ops-fixture
$ mkdir -p "$FIXTURE/deploy/helm/acr" "$FIXTURE/docker"
$ echo dummy > "$FIXTURE/deploy/helm/acr/Chart.yaml"
$ touch "$FIXTURE/docker/acr-db-init.sh"

$ bash scripts/deploy/verify-boundaries.sh --acr . --ops "$FIXTURE" --compose /Users/chris/projects/full-chaos/dev-health/compose.yml
BOUNDARY VIOLATION: ACR-named deployment artifact found under dev-health-ops: deploy/helm/acr (private ACR packaging must live only under dev-health-acr, never dev-health-ops)
EXIT=1

$ rm -rf "$FIXTURE/deploy"
$ bash scripts/deploy/verify-boundaries.sh --acr . --ops "$FIXTURE" --compose /Users/chris/projects/full-chaos/dev-health/compose.yml
BOUNDARY VIOLATION: ACR-named deployment artifact found under dev-health-ops: docker/acr-db-init.sh (private ACR packaging must live only under dev-health-acr, never dev-health-ops)
EXIT=1
```

The disposable fixture tree was removed immediately after (`rm -rf`); the
real `../ops` checkout was never touched by this test.

## Adversarial coverage

| Case | Result |
| --- | --- |
| Missing all flags | `exit 2`, usage printed |
| `--acr` given with no value (malformed) | `exit 2`, `missing value for --acr` |
| `--ops` pointed at a nonexistent path | `exit 2`, `invalid --ops path: not a directory` |
| Unknown flag (`--bogus`) | `exit 2`, `unknown argument: --bogus`, usage printed |
| Stale ownership docs (fixture ADR missing every required section) | `exit 1`, names the first missing section (`external Postgres/ClickHouse/Ops dependency declaration`) |
| Dirty ACR worktree (untracked sentinel file added at repo root) | No effect — result and exit code identical to a clean worktree, because the verifier only reads fixed known paths (`docs/adr/0004-*.md`, `--ops` deploy/docker trees, `--compose` file) and never depends on `git status` |
| Broken relative markdown link in a fixture ADR | `exit 1`, names the missing link target and resolved path |
| Misleading output | Every failure line is prefixed `BOUNDARY VIOLATION:` and names the exact violating path or missing section; usage errors (`exit 2`) are textually and numerically distinct from boundary violations (`exit 1`) and from success (`exit 0`) |
| Malicious/absolute paths, symlink traversal in `--ops` | N/A for this task — the verifier only reads (`find`, `grep`, `cat`); it never writes into `--ops` or `--compose`, so a hostile Ops tree can at most cause a false pass/fail of a read-only scan, not data loss. Out of scope: Todo 5 is a documentation/verification deliverable, not a security-hardening deliverable for a hostile filesystem |
| Concurrent/parallel invocations | N/A — the script has no shared mutable state, lock file, or write target; every invocation is independent |

## Cleanup

- All disposable fixtures (`$(mktemp -d)/ops-fixture`, `$(mktemp -d)/acr-fixture*`)
  were removed with `rm -rf` immediately after each test.
- `untracked-sentinel-for-qa.tmp` created for the dirty-worktree test was
  removed with `rm -f`.
- `../ops` and `../compose.yml` were never modified (read-only `find`/`cat`
  inspection only).
- Final `git status --porcelain=v1` in this worktree shows only the two
  intended new files: `docs/adr/0004-deployment-ownership.md` and
  `scripts/deploy/verify-boundaries.sh`.

## Verification commands (reproduce)

```bash
bash scripts/deploy/verify-boundaries.sh \
  --acr . \
  --ops /Users/chris/projects/full-chaos/dev-health/ops \
  --compose /Users/chris/projects/full-chaos/dev-health/compose.yml
```
