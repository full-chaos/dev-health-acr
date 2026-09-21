# Private ACR Kubernetes overlays

These manifests deploy only ACR API resources into a caller-owned namespace.
They reference existing runtime, migration, entitlement, and registry-pull
Secrets. The base uses plaintext private-service transports; an operator overlay
may select TLS DSNs/origins and add the corresponding CA projections. The base
does not create Secrets, a Gateway, a Gateway controller, a database, or an MCP
workload. The hosted `acr-mcp` workload is an opt-in Component
(`components/acr-mcp`) that an overlay composes; `overlays/development-mcp` is
the reference. It mounts no Secret and holds no credential (only the registry pull secret is referenced; each caller's bearer is
forwarded to `acr-api`), and `apply.sh`/`wait.sh`/`rollback.sh` handle it like
the API Deployment (rollback re-applies the overlay-pinned `acr-mcp` digest).

Each overlay pins `acr-api` to an immutable digest. The deployment script
applies supporting resources, creates and waits for `acr-migrate`, and applies
the API Deployment only after migration success.

```bash
bash deploy/kubernetes/acr/scripts/apply.sh --overlay staging \
  --image ghcr.io/full-chaos/dev-health-acr/acr-api@sha256:<64-hex-digest>
```

Wait for an existing rollout:

```bash
bash deploy/kubernetes/acr/scripts/wait.sh --overlay staging
```

Rollback renders an application-only Deployment by default. Use `--apply` only
after selecting the prior immutable application digest; rollback never runs a
schema migration or changes the migration Job.

```bash
bash deploy/kubernetes/acr/scripts/rollback.sh --overlay staging \
  --image ghcr.io/full-chaos/dev-health-acr/acr-api@sha256:<64-hex-digest> \
  --apply
```

Run offline policy validation with `scripts/deploy/test-kustomize.sh`.
