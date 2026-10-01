# ACR deployment topology coverage (CHAOS-4055)

Three deployment surfaces live here. Helm is the release authority for
Kubernetes; compose is the local/E2E overlay; the kustomize tree is the
script-driven overlay path.

| Compose service (`compose/acr.compose.yml`) | Helm chart (`helm/acr`) | Kustomize (`kubernetes/acr`) |
| --- | --- | --- |
| `acr-api` | `templates/deployment.yaml` | `base/deployment.yaml` |
| `acr-migrate` | `templates/migration-job.yaml` (pre-install/upgrade hook) | `base/migration-job.yaml` |
| `acr-db-init` (bootstrap roles) | **not covered -- documented gap** (see below) | not covered |
| `acr-db-acl` (runtime ACL grants) | **not covered -- documented gap** (see below) | not covered |
| `acr-projector` | `templates/projector-deployment.yaml` (values-gated; always `strategy: Recreate`, so two projector versions never run at once during a rollout) | **not covered -- documented gap** (see below) |
| `falkordb` (profile-gated) | `templates/falkordb-statefulset.yaml` + service (values-gated, CHAOS-4055) | not covered (use helm) |
| `acr-mcp` (hosted Streamable HTTP; **no compose service, Kubernetes only**) | `templates/acr-mcp-*.yaml` (`acrMcp.enabled`, off by default) | `components/acr-mcp` + `overlays/development-mcp` (opt-in Component) |
| `postgres` / `clickhouse` (root compose) | external by contract (ADR-0004) | external by contract |

## Hosted acr-mcp (Kubernetes only)

`acr-mcp` runs as its own stateless workload from its own image (the
`acr-mcp` Dockerfile target, published multi-arch `linux/amd64` +
`linux/arm64` by the release workflow). It is selected with `serve` plus
`ACR_MCP_TRANSPORT=http`; STDIO stays the image default. It has **no compose
service** and is never injected beside `acr-api` (`deployment.extraContainers`
still fails closed naming `injected-mcp`).

The pod holds **no credential**: no Secret is mounted or read into its
environment (the only Secret reference is the image pull secret, which is
registry auth, and it is absent when none is configured), no
ServiceAccount token is mounted, and `ACR_API_TOKEN` is never set. Each request
carries the caller's own ACR API bearer, which `acr-mcp` decides against and
forwards to `acr-api`; there is no service-to-service credential.

- Helm: set `acrMcp.enabled=true` and `acrMcp.image.reference` (an
  `@sha256` digest of the `acr-mcp` image). `acrMcp.apiUrl` defaults to the
  release's own `acr-api` Service. `acrMcp.gateway.*` adds an HTTPRoute for a
  caller-supplied Gateway that routes only `acrMcp.basePath` (`/mcp`). The
  workload ships a default-deny NetworkPolicy and admits itself on `acr-api`'s.
- Kustomize: an overlay lists `components: [../../components/acr-mcp]` and adds
  its own HTTPRoute; `overlays/development-mcp` is the reference composition.
  Plain `development`, `staging` and `production` never render it.
- Probes: `/healthz` (liveness), `/readyz` (readiness, requires `acr-api` live).

## Documented gaps (deliberate, not oversights)

- **`acr-db-init` / `acr-db-acl` have no Kubernetes equivalent.** The compose
  overlay bootstraps the runtime/migration role split and re-asserts ACL
  grants with the Postgres admin credential. Both Kubernetes paths instead
  declare Postgres externally provisioned: the operator creates the roles and
  grants out-of-band and hands the chart two pre-existing Secrets
  (`credentials.runtime` / `credentials.migration`). Closing this in-cluster
  would require the chart to hold an admin DSN, which contradicts its
  existing-Secret-only, least-privilege credential contract. If a k8s-native
  bootstrap is ever wanted, it needs its own ticket and credential design.
- **The kustomize tree carries only the API + migration.** It predates the
  projector and FalkorDB workloads; helm is the surface that grows. Use the
  chart for the complete topology.
- **Compose FalkorDB vs chart FalkorDB.** Same digest, same GRAPH.QUERY
  health vocabulary. The chart mounts the data volume at the image's real
  `FALKORDB_DATA_PATH` (`/var/lib/falkordb/data`); the compose mount was
  moved to match in CHAOS-4055 (the old `/data` mount point never captured
  the dump files).

## Complete local topology on Kubernetes

With CHAOS-4055 the chart alone can stand up every ACR-owned workload:
`acr-api`, the migration hook, `acr-projector`
(`contextFabric.projector.enabled=true`), and a single-instance FalkorDB
(`contextFabric.falkordb.enabled=true`, wired via
`contextFabric.falkor.addr=<fullname>-falkordb:6379`, `tls=false`,
`allowInsecure=true`). Postgres and ClickHouse remain externally provisioned
on every surface.

For an Apple-native local cluster (kiac k3s) and the per-shard
Postgres+FalkorDB harness pairs, see `local/README.md`.
