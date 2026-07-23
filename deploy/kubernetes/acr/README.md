# Private ACR Kubernetes overlays

These manifests deploy only ACR API resources into a caller-owned namespace.
They reference existing runtime, migration, entitlement, and registry-pull
Secrets. They do not create Secrets, a Gateway, a Gateway controller, a
database, or an MCP workload.

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
