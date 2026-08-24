# Local Kubernetes via kiac (CHAOS-4055)

Apple-Silicon-native local cluster path for ACR. Every node is a lightweight
VM on Apple's open-source `container` runtime -- entirely outside any Docker
daemon, so cluster work never contends with Docker-hosted compose stacks or
Testcontainers suites. Additive: the kind/k3d scripts under `scripts/e2e/`
are untouched and remain the portable (Linux/CI) fallback.

## Pinned tool matrix

| Tool | Version | Checked by |
| --- | --- | --- |
| kiac | v0.5.1 | `kiac.sh doctor` (fail-closed; `ACR_KIAC_ALLOW_VERSION_DRIFT=1` downgrades to a warning) |
| apple/container | 1.2.2 | `kiac.sh doctor` |
| kubectl | any recent | presence only |

First run of apple/container needs its one-time interactive initialization
(`container system start`). `kiac.sh doctor` fails early with that
instruction rather than entering a partial cluster create (kiac issue #35).

## Cluster lifecycle

```bash
deploy/local/kiac.sh doctor        # preflight: binaries, pins, runtime state
deploy/local/kiac.sh up            # k3s, single node by default (ACR_KIAC_WORKERS=n for more)
export KUBECONFIG="$(deploy/local/kiac.sh kubeconfig)"
# ... use the cluster ...
deploy/local/kiac.sh down          # delete cluster + isolated kubeconfig
```

The kubeconfig is isolated under `.tmp/kiac/<cluster>/kubeconfig`; the user's
`~/.kube/config` is never touched. k3s mode bundles a default local-path
StorageClass and metrics-server, which satisfies the helm chart's PVC needs
(e.g. the optional `contextFabric.falkordb` workload) with no extra addons.

## Image path (no registry, no Docker)

```bash
deploy/local/kiac.sh build-image acr-api:dev . [Dockerfile]  # container build
deploy/local/kiac.sh load-image acr-api:dev                  # -> every node's containerd
```

Reference loaded images with `imagePullPolicy: Never` (or `IfNotPresent`).
The helm chart accepts a local tag only for `config.environment`
development/test renders; everything else requires an immutable digest.

## Per-shard Postgres + FalkorDB pairs (CHAOS-4033 consumer)

`shard.sh` stands up one isolated Postgres+FalkorDB pair per namespace,
parameterized by shard index. This is the deploy-side contract for the
two-turn confirmation harness's parallel trials; the harness code lives
elsewhere and consumes only the contract below.

```bash
deploy/local/shard.sh apply 0 && deploy/local/shard.sh wait 0
deploy/local/shard.sh dsn 0     # prints the DSN + falkor addr for shard 0
deploy/local/shard.sh delete 0  # teardown = namespace delete
```

### Contract

| Item | Value |
| --- | --- |
| Namespace | `acr-shard-<i>` (all shard state lives inside it) |
| Postgres endpoint | NodePort `31000 + 2*i` on any node IP; user `acr`, db `acr` |
| Postgres DSN | `postgres://acr:<password>@<node-ip>:<31000+2i>/acr?sslmode=disable` (printed by `shard.sh dsn <i>`) |
| FalkorDB endpoint | NodePort `31001 + 2*i` on any node IP; no auth |
| Password | `ACR_SHARD_PG_PASSWORD` (default `acr-local-dev`, dev-only; restricted to `[A-Za-z0-9._~-]`, fail-closed) |
| Readiness | in-cluster probes: `pg_isready` (postgres) and `GRAPH.QUERY` (falkordb, proves the graph module executes Cypher -- PING alone is insufficient); `shard.sh wait <i>` returns only when both Deployments are fully rolled out. Client-side liveness over the declared endpoints: `pg_isready -d <dsn>` / `redis-cli -h <node-ip> -p <port> PING` |
| Teardown | `shard.sh delete <i>` = `kubectl delete namespace acr-shard-<i>`; nothing leaks outside the namespace |
| Persistence | none (ephemeral by design; the harness reseeds per trial) |
| Images | exact compose digests: `postgres:18-alpine@sha256:a1d02e...`, `falkordb/falkordb@sha256:ad09d5...` (FalkorDB 4.20.2 / Redis 8.6.3) |
| Shard budget | `i <= 883` (NodePort range) |

Migrations: run `acr-migrate` from the host against the printed DSN (the
`postgres` superuser-equivalent here is the `acr` user itself; per-shard
databases are single-tenant and ephemeral, so the compose role split does not
apply).

## Standing trial data plane (CHAOS-4186)

`trial-data.sh` stands up a PERSISTENT single instance of postgres +
clickhouse + falkordb -- unlike `shard.sh` (ephemeral, no PVCs, reseeded per
trial), this is seeded ONCE from a `dev-health/backups/` dump and reused
across runs, ending the trial harness's dependence on the shared Docker
compose stack. Reuses whatever cluster `KUBECONFIG` points at (normally the
same `acr-local` kiac cluster the helm chart pilot already runs in, in its
own `acr-trial-data` namespace) -- it does not create a second cluster.

```bash
export KUBECONFIG="$(deploy/local/kiac.sh kubeconfig)"
deploy/local/trial-data.sh apply && deploy/local/trial-data.sh wait
deploy/local/trial-data.sh restore-postgres backups/postgres-all-<ts>.sql.gz
deploy/local/trial-data.sh restore-clickhouse backups/clickhouse-*-<ts>.zip
deploy/local/trial-data.sh dsn        # prints the ACR_TEST_TRIAL_* DSN recipe
deploy/local/trial-data.sh dsn --env  # same, as raw KEY=value lines (scripting; the consumer
                                       # never evals this -- see common.sh's kiac branch)
deploy/local/trial-data.sh wipe       # namespace `acr-trial-data` ONLY -- ignores
                                       # ACR_TRIAL_DATA_NAMESPACE; a differently-named
                                       # instance's resources are the caller's to remove
```

### Contract

| Item | Value |
| --- | --- |
| Namespace | `acr-trial-data` (`apply`/`render`/`dsn`/`restore-*` honor `ACR_TRIAL_DATA_NAMESPACE`; `wipe` does NOT -- it operates on the hardcoded default only, see traps) |
| Postgres endpoint | NodePort 30500; role `devhealth` (matches the seed dump, NOT a dedicated `acr` role -- see trap below) |
| ClickHouse endpoint | NodePort 30501 (HTTP), 30502 (native); user `ch` |
| FalkorDB endpoint | NodePort 30503; no auth |
| Persistence | PVCs for postgres/clickhouse/falkordb data (`ACR_TRIAL_PG_STORAGE`/`ACR_TRIAL_CH_STORAGE`/`ACR_TRIAL_FALKOR_STORAGE`, defaults 20Gi/30Gi/5Gi) -- survives `apply`/pod restarts; only `wipe` destroys it |
| Images | postgres:18-alpine (same digest as `shard.yaml`), falkordb (same digest), clickhouse pinned to the digest resolved from the live compose container at build time (`ACR_TRIAL_CH_IMAGE` override) -- parity with whatever the compose baseline was measured under, not an arbitrary tag |

**Launcher coverage**: every trial script (`run-two-turn.sh`, the sharded
`run-two-turn-parallel.sh`, replay/W0/D2B/generative/frontier -- everything
sharing `scripts/trial/common.sh`) reads ONE switch,
`ACR_TRIAL_DATA_PLANE=kiac|compose` (default **`kiac`** -- chris's standing
order: kiac is THE trial stack for every run, no comparability exception;
`compose` is the legacy fallback for anyone still standing that up locally).
`kiac` resolves postgres, clickhouse, AND
falkordb together from `trial-data.sh dsn` in one call -- there is no
per-store partial state to land in, so a launcher can never end up
measuring against a hybrid of the two stacks. An `ACR_TRIAL_{PG,CH,FALKOR}_
{HOST,PORT}` escape hatch exists for one-off diagnostics (e.g. a relay
bypassing a misbehaving host port-forward); setting any one of those six
vars requires setting all six, enforced at `common.sh` source time.
Not yet exercised against a real parallel/sharded run on this data plane --
CHAOS-4186's own smoke was sequential-subset only, per the ratified plan.
One harness remains compose-bound regardless of this switch:
`run-frontier-arm.sh` talks to the compose clickhouse container directly
via `docker exec` (its own frontier baseline, a separate measurement family)
-- tracked as CHAOS-4220 (Low), not fixed here.
DSN component assembly (`common.sh`'s `ch_dsn`, `run-two-turn-parallel.sh`'s
per-shard Postgres DSN) does not bracket an IPv6 host -- unreachable via the
kiac or compose planes themselves (both always IPv4), only via the six-var
escape hatch pointed at an IPv6 endpoint -- tracked as CHAOS-4228 (Low), not
fixed here.

### Traps hit standing this up (read before repeating)

1. **Seed source**: `dev-health/backups/` accumulates timestamped snapshots
   over time from the periodic backup script -- only the ORIGINAL dataset is
   the sanctioned trial seed (chris ruling, banked in the repo's ops memory:
   "@backups = the ORIGINAL pg+ch dataset"). A newer-looking timestamped
   subdirectory is a snapshot of the LIVE (possibly drifted) compose stack,
   not the seed. Check with whoever owns the trial measurement plan before
   assuming "newest" is "correct" -- seeding from the wrong one silently
   breaks baseline comparability.
2. **Postgres bootstrap role collision**: do NOT bootstrap the postgres
   container with `POSTGRES_USER=devhealth` before restoring a plain (no
   `--clean`) `pg_dumpall` that itself does `CREATE ROLE devhealth`. Bootstrap
   as the default `postgres` superuser/db instead; restore as that role.
   `restore-postgres` pins the `devhealth` role's password afterward
   (the dump's own `ALTER ROLE ... PASSWORD` overwrites whatever
   `POSTGRES_PASSWORD` the container started with, to whatever the SOURCE
   stack's password was, which is unknown here).
3. **ClickHouse has no `Zip` backup engine compiled in on this image** --
   `RESTORE ... FROM Zip(...)` fails `BACKUP_ENGINE_NOT_FOUND`. `trial-data.sh`
   unzips client-side and restores via `FROM Disk('trial_backups', <dir>)`
   instead (see the `backup-disk.xml` ConfigMap in the manifest).
4. **`kubectl cp` does not reliably carry local file permissions into the
   container** -- ClickHouse's own `.backup` metadata file lands unreadable
   by the server's UID regardless of local `chmod`. Fixed server-side
   (`kubectl exec ... chmod -R a+rwX` after the copy, not trusted to the
   transport).
5. **Schema migrations**: a seed dump captures whatever migration state the
   SOURCE stack was at when it was taken -- almost certainly behind the
   worktree's current tip. Run `acr-migrate up` (env `ACR_POSTGRES_MIGRATION_DSN`
   = the same postgres DSN) before any code that assumes current schema
   (e.g. `acr-projector rebuild`, which needs `acr.context_fabric_graph_lifecycle`
   from a migration well after 08-17). This is schema DDL (deterministic
   code), not measurement data -- doesn't conflict with a "no re-baseline"
   data discipline.
6. **Graph state is NOT restorable from a dump**: FalkorDB only gets
   populated via a live `acr-projector rebuild --org <id>` + `serve` run
   against the seeded postgres/clickhouse (build-aside-and-swap). Needs
   `ACR_CONTEXT_FABRIC_GRAPH_LIFECYCLE_ENABLED=true`,
   `ACR_CONTEXT_FABRIC_PROJECTOR_ORG_IDS=<id>`, and (for a real, non-lexical-only
   graph) `ACR_CONTEXT_FABRIC_EMBED_*` pointed at a working embed credential --
   an unconfigured or blank-key embedder is either skipped cleanly (unset) or
   refused at startup (configured-but-blank, CHAOS-4192 guard); never silently
   degrades.
7. **A rebuild opens a 24h-graced epoch** (default
   `ACR_CONTEXT_FABRIC_GRAPH_LIFECYCLE_GRACE_WINDOW`) -- running `rebuild`
   again (e.g. because the first attempt lacked embed credentials) hits
   `lifecycle CAS conflict ... observed_status=grace` and refuses to open a
   fresh epoch. `acr-projector rollback --org <id>` reverts to the previous
   epoch while still inside the grace window and unblocks a clean re-`rebuild`
   -- costs nothing (the org's own dataset is unaffected), but you have to
   know to do it rather than wait out the 24h.
8. **CHAOS-4208 post-flip divergence loop -- FIXED (#252 c53042d0), this
   section is now HISTORY only**: a `serve` run against a freshly-seeded
   trial cluster used to be able to hit a reproducible infinite loop
   starting ~1-2s after the LAST projection source reaches
   `cursor_exhausted` -- repeating `checkpoint-store divergence detected
   (CHAOS-3882)` ERROR -> `lifecycle CAS conflict, observed_status=grace`
   -> `build-aside epoch already open; not restarting` -> a
   self-contradicting WARN claiming recovery opened an epoch anyway --
   every ~2s, no forward progress. Root cause (lane-3882-loop): unwired
   epoch-resolver cache invalidation -- if a `serve` process (or any
   process with a warm resolver cache) was already running when `rebuild`
   fired `BeginBuild`, writes could misroute to the stale cached epoch key,
   deadlocking the recovery path in `grace`. Severity correction from that
   investigation: the loop was always a bounded, self-healing false-alarm
   storm -- data written before it starts was never corrupted (confirmed
   live twice: 82.84% Subject-node embedding coverage + an OPERATIONAL KNN
   vector index landed cleanly both times) -- it just never converged/
   flipped on its own. **Fix**: `CachedResolver.Invalidate` now wired into
   BeginBuild/Flip/Rollback (#252). The workaround that unblocked this
   before the fix -- rollback the stuck epoch, use a fresh cold process,
   run `rebuild` to completion BEFORE starting `serve` (never the reverse),
   set `ACR_CONTEXT_FABRIC_GRAPH_LIFECYCLE_LEASE` low -- should no longer
   be necessary on any worktree built from #252 or later; kept here as a
   fallback recipe only, not a step to follow by default.
9. **`ACR_TEST_TRIAL_LIMIT` is a ROW budget, not a case count.** It caps
   the harness's own (member x arm) result rows, not distinct corpus case
   indices -- `LIMIT=16` actually ran only 6 distinct corpus cases (indices
   0-5), and the LAST case in that set was truncated mid-case (some
   members/arms never ran, because the row budget was exhausted first).
   For a genuine parity/flip-rate comparison against another run, either
   drop the last (possibly-partial) case from the comparison, or use
   `ACR_TEST_TRIAL_INDICES` (exact corpus indices) instead of `LIMIT` when
   you need every selected case to be complete.

## Resizing the control-plane VM (CHAOS-4186)

**WARNING: recreate is destructive.** `acr-local` is single-node --
`kiac-acr-local-control-plane` (untainted) runs every workload, including
both `acr-trial-data` and any other namespace on the cluster (e.g.
`acr-pilot`). Neither `kiac` nor the underlying `container` CLI expose a
live-resize path: `--cpus`/`--cp-memory` are `kiac create cluster`-only
flags, and hand-editing the node's on-disk state files while stopped
(`~/Library/Application Support/com.apple.container/containers/<id>/
config.json` and `runtime-configuration.json`) does NOT work either --
confirmed empirically on a disposable throwaway cluster: both edits
persisted on disk but were silently ignored on the next `container
start`, which still reported the original create-time cpus/memory. The
node's PVC storage (local-path-provisioner, hostPath-backed) lives
entirely inside that same directory's `rootfs.ext4`, with no separate
volume object `container` could reattach -- so the ONLY way to change
CPU/memory is `kiac.sh down` + `kiac.sh up`, which provisions a fresh
`rootfs.ext4` and **destroys all PVC-backed data on the node**.

**Step 0, before any of the below: kill every process holding connections to
the OLD cluster.** The recreated VM reuses the same NodePort IP:port pairs
(container networking assigns them fresh, but kiac's fixed NodePorts mean a
leftover `acr-projector serve`/`rebuild` from an earlier session can end up
pointed at the NEW cluster's endpoints without anyone restarting it -- CHAOS-
4186 hit this live: a 7-hour-old orphaned `acr-projector serve` from an
unrelated earlier investigation raced the fresh rebuild's epoch lifecycle
over the SAME org, producing a `checkpoint held for replay`/`failure_class:
canceled` tick-cancellation storm indistinguishable at first glance from a
genuine infra problem). Pre-flight before `rebuild`/`serve`:
`procs --json 'acr-projector'` must return empty (besides the one you are
about to launch). Kill anything it finds via the scoped-kill discipline
(confirm ownership via cwd first) before proceeding.

Recipe (run only when no lane is mid-run on kiac; coordinate first):

```bash
deploy/local/kiac.sh down                                    # destroys acr-trial-data + acr-pilot data
ACR_KIAC_CPUS=4 ACR_KIAC_CP_MEMORY=16G deploy/local/kiac.sh up
export KUBECONFIG="$(deploy/local/kiac.sh kubeconfig)"        # kiac.sh up only LOGS this; every
                                                               # trial-data.sh call below needs it set
deploy/local/trial-data.sh apply && deploy/local/trial-data.sh wait
deploy/local/trial-data.sh restore-postgres backups/postgres-all-<ORIGINAL Aug-17 ts>.sql.gz
deploy/local/trial-data.sh restore-clickhouse backups/clickhouse-*-<ORIGINAL Aug-17 ts>.zip
# apply acr DB migrations up to the ratified schema version (NOT the dump's
# own version -- the dump predates several migrations)
procs --json 'acr-projector'                                  # MUST be empty before the next step -- see
                                                                # Step 0 above; kill any survivor first
# rebuild the falkordb graph via acr-projector (CHAOS-3898 build-aside-and-
# swap: rollback if a stale epoch exists, rebuild to completion, THEN serve
# -- CHAOS-4208 is fixed upstream now, so no special unblock recipe needed).
# `deploy/local/trial-data.sh dsn --env` includes
# ACR_CONTEXT_FABRIC_FALKOR_TLS=false and
# ACR_CONTEXT_FABRIC_FALKOR_ALLOW_INSECURE=true -- BOTH required together
# (TLS=false alone is refused by acr-projector's own config validation,
# ALLOW_INSECURE=true alone does nothing since it only permits skipping
# cert verification when TLS IS used). Without them acr-projector
# defaults to TLS=true, sends a TLS ClientHello at the trial FalkorDB's
# plaintext port, and every projection tick hangs until
# ACR_CONTEXT_FABRIC_FALKOR_REQUEST_TIMEOUT (default 30s) -- a real
# incident hit live during this exact recipe, root-caused via `deja
# recall` on the "failed to dial ... context deadline exceeded" line.
# re-verify embedding coverage / KNN before calling it done
#
# acr-pilot's image: build with DOCKER, not `kiac.sh build-image` (the
# apple/container builder-shim has real bugs -- "changes out of order"
# on context transfer and "unsupported offset" on a multi-stage COPY
# --from, hit live during this exact recipe; deja found no prior fix,
# do not fight the shim). The Dockerfile has MULTIPLE final stages
# (acr-api, acr-mcp, ...) -- a bare `docker build` with no --target
# defaults to the LAST one in the file (acr-mcp, missing acr-migrate/
# acr-api/acr-projector entirely), a real incident this recipe also
# hit live. Always pass --target explicitly:
docker build --target acr-api -t dev-health-acr:dev .
docker save dev-health-acr:dev -o /tmp/dev-health-acr-dev.tar
container image load -i /tmp/dev-health-acr-dev.tar
container image tag docker.io/library/dev-health-acr:dev dev-health-acr:dev
kiac load image dev-health-acr:dev --name acr-local
#
# acr-pilot's runtime Secret (acr-runtime-credentials/acr-model-credentials):
# GENERATE fresh at redeploy time, NEVER restore a Secret backup from a
# previous install (a real incident hit live: a restored backup carried
# both a pre-migration host AND a password that predated this recipe's
# own restore-postgres step, so it silently no longer matched the live
# role's actual password -- surfaced as "FATAL: password authentication
# failed", not a connectivity error, which is why DNS/TCP checks alone
# didn't catch it).
#
# `deploy/local/trial-data.sh dsn --env` is NOT the source for this --
# it emits the node's EXTERNAL access point (InternalIP + NodePorts,
# e.g. 192.168.64.14:30500), meant for trial scripts running on the
# HOST Mac outside the cluster. acr-pilot's pod runs INSIDE the
# cluster, where the correct host is the in-cluster Service DNS name
# on the store's own cluster-internal port (NOT the NodePort):
#   <service>.<namespace>.svc.cluster.local:<port>
#   trial-postgres.acr-trial-data.svc.cluster.local:5432
#   trial-clickhouse.acr-trial-data.svc.cluster.local:9000
#   trial-falkordb.acr-trial-data.svc.cluster.local:6379
# (confirm the exact names/ports with `kubectl -n acr-trial-data get svc`
# -- they come from templates/trial-data.yaml, not from this script.)
#
# The credential itself: read directly from the live `trial-postgres`
# Secret in acr-trial-data (the SAME cluster-secret-is-source-of-truth
# rule `dsn`'s own password lookup follows) -- never `trial_secret`
# (that reads ops/.env, a completely different, host-side credential
# store unrelated to what the trial data plane pods were actually
# seeded with):
kubectl -n acr-trial-data get secret trial-postgres \
  -o jsonpath='{.data.POSTGRES_PASSWORD}' | base64 -d
# Build each DSN as postgres://devhealth:<that password>@trial-postgres.
# acr-trial-data.svc.cluster.local:5432/acr?sslmode=disable (clickhouse
# analogous, user `ch`, same password -- see trial-data.sh's own Secret
# comment for why postgres/clickhouse share one password), base64-encode
# into acr-runtime-credentials' ACR_POSTGRES_DSN/ACR_POSTGRES_MIGRATION_DSN/
# ACR_CLICKHOUSE_DSN keys, and `kubectl -n acr-pilot apply -f` the result.
# Never hand-edit an EXISTING Secret's password to "whatever looks
# right" -- always re-derive it from the live trial-postgres Secret above.
# redeploy acr-pilot via its own owner's normal deploy path (stateless, no
# PVC -- no data loss there, just needs a fresh rollout after the recreate)
```

No parity re-run needed (chris ruling: seed data and schema are identical
to what was already ratified -- only the VM's resource ceiling changes).
Verify: `container inspect kiac-acr-local-control-plane` shows the new
cpus/memoryInBytes, all pods Ready in both namespaces, DSN reachable,
falkordb node count unchanged from the pre-recreate graph.

## Parity verification (CHAOS-4186, 2026-08-24)

One-time sequential-subset smoke against the kiac trial data plane, per the
ratified plan: seed from `@backups` (no re-baseline), rebuild the graph with
real embeddings (see trap 6/8), then compare a small live run against the
established compose-stack baseline. **Historical note**: this smoke ran
before the `ACR_TRIAL_DATA_PLANE` switch (Launcher coverage, above) existed
-- it manually exported the three `ACR_TEST_TRIAL_*` endpoint vars ahead of
calling `trial_wire_common_env`, which worked at the time but is no longer
how a kiac run should be driven; use `ACR_TRIAL_DATA_PLANE=kiac` instead.
`ACR_TRIAL_CORPUS` was explicitly pointed at `.remember/acr-3778-corpus-ext65.json`
-- at the time of this run, `scripts/trial/common.sh`'s own default pointed
at a STALE corpus file (the CHAOS-3860 eval-only holdout, never meant as a
live-trial default) that fails the CHAOS-4157 v2-scheme preflight against
the current oracle annex; fixed upstream since (#251, common.sh's default
now matches). Still worth the habit: verify `provenance.corpus_sha256` on
any artifact matches the annex's expected corpus sha before trusting a run.

**Methodology**: row-level and cell-level flip rate over the regime/offer/pool/outcome
field set (18 fields: `turn1_regime`; `offer_kind`, `turn1_offer_kind`,
`candidate_offer_count`, `turn1_candidate_offer_count`,
`kind_offer_distinct_kind_count`, `turn1_kind_offer_distinct_kind_count`,
`kind_offer_explicit_hint_count`, `turn1_kind_offer_explicit_hint_count`,
`kind_offer_suppressed_by_cardinality`, `turn1_kind_offer_suppressed_by_cardinality`,
`offer_miss`; `expected_in_pool`, `expected_kind_at_offer_boundary`,
`expected_kind_at_offer_boundary_before_repair`; `turn1_status`,
`turn2_status`, `applied`, `committed_count`, `inferred_classification`),
computed over result rows keyed by (index, member, arm, mutation_probe) --
the same key shape `acr-trial-merge-two-turn` uses -- restricted to corpus
indices 0-4 (index 5 excluded: truncated by the LIMIT-is-a-row-budget trap
above, not comparable). Compared against the two full-65 compose-stack
replicates from the CHAOS-4183 validation
(`gen-trial-chaos3742_twoturn-parallel-20260824T013649Z-38193-merged.json`,
`...-20260824T024828Z-21131-merged.json`), same corpus sha (`b981ac40...`),
same schema v26.

| comparison | cell-level flip | row-level flip (>=1 field differs) |
| --- | --- | --- |
| compose rep1 vs rep2 (noise floor) | 7.56% (68/900) | 35.56% (16/45) |
| kiac vs rep1 | 7.11% (64/900) | 40.00% (18/45) |
| kiac vs rep2 | 11.00% (99/900) | 51.11% (23/45) |

Commit-safety fields (`wrong_commit`, `pair_invalid`): ZERO mismatches across
every pairwise comparison, every row. Aggregate gates (positive_applied_count,
wrong_commit_count, false_no_match_count, gate_reachable_count, all
inferred_* counts) identical between kiac and the compose baseline.

**VERDICT (team-lead ruling, chris-carried, 2026-08-24): PARITY HOLDS --
baselines carry over, no re-baseline.** Rationale: triangle distances (floor
35.6 / kiac-rep1 40.0 / kiac-rep2 51.1) point at rep2 as the outlier draw,
not kiac -- a systematically different kiac population would exceed the
floor on BOTH pairings, and one pairing sits at/below floor. Row-level
23/45 vs 16/45 is not statistically significant (two-proportion z~1.5,
p~0.14). Commit-safety and aggregate agreement hold everywhere. The
Aug-17 seed vs drifted-compose data-vintage difference makes some
divergence expected, and it still landed in-band.
**CAVEAT**: n=45 rows is small; one pairing (kiac-vs-rep2) ran elevated
above the floor. A tie-breaker replicate may be ordered later -- this
verdict is not a claim that kiac is proven identical to compose, only that
the evidence available does not disqualify it.

**Timing head-to-head** (per-responder-call latency, isolating model
round-trip time from case-selection noise -- both environments hit the same
external codex-subscription backend for the actual model call):

| arm | kiac / call | compose / call | delta |
| --- | --- | --- | --- |
| turn1 | 18064ms | 15657ms | +15.4% |
| positive | 16251ms | 14501ms | +12.1% |
| inferred_tier | 17002ms | 15034ms | +13.1% |
| confirmed_wrong | 17026ms | 14922ms | +14.1% |
| mutation | 17503ms | 16376ms | +6.9% |

Uniform ~7-15% higher per-call latency on kiac across every arm -- since the
model call itself hits the same external backend either way, a uniform gap
across all arms (not concentrated in one) reads as DB/graph-side overhead
baked into the measured call window (NodePort routing to the kiac node vs
compose's more direct container-network path), not model variance.
Informational; did not affect the parity verdict.

## Constraints

- Apple Silicon/macOS only; kind/k3d remain the portable fallback.
- Never touches the Docker daemon or any compose stack.
- Nothing here deploys ACR application workloads by itself; the helm chart
  (`deploy/helm/acr`) remains the release authority.
