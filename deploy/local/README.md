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
| Password | `ACR_SHARD_PG_PASSWORD` (default `acr-local-dev`, dev-only) |
| Readiness | in-cluster probes: `pg_isready` (postgres) and `GRAPH.QUERY` (falkordb, proves the graph module executes Cypher -- PING alone is insufficient); `shard.sh wait <i>` returns only when both Deployments are fully rolled out. Client-side liveness over the declared endpoints: `pg_isready -d <dsn>` / `redis-cli -h <node-ip> -p <port> PING` |
| Teardown | `shard.sh delete <i>` = `kubectl delete namespace acr-shard-<i>`; nothing leaks outside the namespace |
| Persistence | none (ephemeral by design; the harness reseeds per trial) |
| Images | exact compose digests: `postgres:18-alpine@sha256:a1d02e...`, `falkordb/falkordb@sha256:ad09d5...` (FalkorDB 4.20.2 / Redis 8.6.3) |
| Shard budget | `i <= 883` (NodePort range) |

Migrations: run `acr-migrate` from the host against the printed DSN (the
`postgres` superuser-equivalent here is the `acr` user itself; per-shard
databases are single-tenant and ephemeral, so the compose role split does not
apply).

## Constraints

- Apple Silicon/macOS only; kind/k3d remain the portable fallback.
- Never touches the Docker daemon or any compose stack.
- Nothing here deploys ACR application workloads by itself; the helm chart
  (`deploy/helm/acr`) remains the release authority.
