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
deploy/local/trial-data.sh dsn      # prints the ACR_TEST_TRIAL_* DSN recipe
deploy/local/trial-data.sh wipe     # destroys the namespace INCL. PVCs
```

### Contract

| Item | Value |
| --- | --- |
| Namespace | `acr-trial-data` (override: `ACR_TRIAL_DATA_NAMESPACE`) |
| Postgres endpoint | NodePort 30500; role `devhealth` (matches the seed dump, NOT a dedicated `acr` role -- see trap below) |
| ClickHouse endpoint | NodePort 30501 (HTTP), 30502 (native); user `ch` |
| FalkorDB endpoint | NodePort 30503; no auth |
| Persistence | PVCs for postgres/clickhouse/falkordb data (`ACR_TRIAL_PG_STORAGE`/`ACR_TRIAL_CH_STORAGE`/`ACR_TRIAL_FALKOR_STORAGE`, defaults 20Gi/30Gi/5Gi) -- survives `apply`/pod restarts; only `wipe` destroys it |
| Images | postgres:18-alpine (same digest as `shard.yaml`), falkordb (same digest), clickhouse pinned to the digest resolved from the live compose container at build time (`ACR_TRIAL_CH_IMAGE` override) -- parity with whatever the compose baseline was measured under, not an arbitrary tag |

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
8. **KNOWN ISSUE, unresolved as of 2026-08-24**: both a lexical-only and an
   embedded `serve` run hit an identical, reproducible infinite loop starting
   ~1-2s after the LAST projection source reaches `cursor_exhausted` --
   repeating `checkpoint-store divergence detected (CHAOS-3882)` ERROR ->
   `lifecycle CAS conflict, observed_status=grace` -> `build-aside epoch
   already open; not restarting` -> a self-contradicting WARN claiming
   recovery opened an epoch anyway -- every ~2s, no forward progress. Real
   data and (in the embedded run) real embeddings DO get written before the
   loop takes over (confirmed live: 82.84% Subject-node embedding coverage,
   KNN vector index present and OPERATIONAL on the resulting epoch key), but
   the epoch never gets a chance to converge/flip cleanly, so that coverage
   number is a mid-loop snapshot, not a settled state. `go run`'s wrapper
   process does not reliably signal its compiled child on `kill` -- match by
   full command (`procs --json 'acr-projector'`) and kill the actual binary,
   not just the wrapper PID. Under investigation; do not treat an epoch flip
   as reliable until this is understood.

## Constraints

- Apple Silicon/macOS only; kind/k3d remain the portable fallback.
- Never touches the Docker daemon or any compose stack.
- Nothing here deploys ACR application workloads by itself; the helm chart
  (`deploy/helm/acr`) remains the release authority.
