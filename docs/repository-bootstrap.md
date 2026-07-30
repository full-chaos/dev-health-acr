# Repository bootstrap record

**Status:** Historical bootstrap complete. The repository is now publicly
visible to support unrestricted CI execution. That visibility is not an
open-source license grant; see [`../LICENSE-POLICY.md`](../LICENSE-POLICY.md).

Target repository:

```text
full-chaos/dev-health-acr
```

Original SVS visibility: **private**. Current visibility: **public for CI**.

## Historical import

From an unpacked source archive or the prepared local checkout:

```bash
gh repo create full-chaos/dev-health-acr \
  --private \
  --description "Private Go API and MCP sidecar for Dev Health Agent Context Runtime" \
  --source . \
  --remote origin \
  --push
```

If the repository is created in the GitHub UI first:

```bash
git remote add origin git@github.com:full-chaos/dev-health-acr.git
git push -u origin main
```

The supplied Git bundle can also recreate the committed history:

```bash
git clone dev-health-acr.bundle dev-health-acr
cd dev-health-acr
git remote add origin git@github.com:full-chaos/dev-health-acr.git
git push -u origin main
```

## Repository settings

Current repository settings should:

1. Require pull requests for `main`.
2. Require the `verify` CI job.
3. Disable force pushes and branch deletion on `main`.
4. Enable secret scanning and dependency alerts.
5. Keep write access restricted while allowing public read and CI execution.
6. Publish packages and releases only through the reviewed signing and release policy.

## First verification

```bash
make contract-test
make verify
go build ./cmd/acr-api ./cmd/acr-mcp ./cmd/contractcheck
```

The contract gate is Go-only and requires no Python setup. The bootstrap is
complete; `acr-api`, `acr-mcp serve`, release publication, and deployment
artifacts are implemented and verified by their current repository gates.

## Go-only contract tooling

The repository has no Python contract-checking dependency. `cmd/contractcheck` validates the local JSON Schema profile, golden examples, OpenAPI references and generated YAML mirror, and MCP manifest entirely in Go.

```bash
make contract-write
make contract-test
make verify
```
