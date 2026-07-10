# Private repository bootstrap

Target repository:

```text
full-chaos/dev-health-acr
```

Visibility for SVS: **private**.

## Preferred import

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

After creation:

1. Require pull requests for `main`.
2. Require the `verify` CI job.
3. Disable force pushes and branch deletion on `main`.
4. Enable secret scanning and dependency alerts.
5. Restrict repository access to the ACR implementation team and approved agents.
6. Do not publish packages or releases until the private distribution/signing policy lands.

## First verification

```bash
make contract-test
make verify
go build ./cmd/acr-api ./cmd/acr-mcp ./cmd/contractcheck
```

The contract gate is Go-only and requires no Python setup. The repository is a contract bootstrap, not a production service. `acr-mcp serve` remains intentionally unwired until the Phase 1 MCP issue is implemented.

## Go-only contract tooling

The repository has no Python contract-checking dependency. `cmd/contractcheck` validates the local JSON Schema profile, golden examples, OpenAPI references and generated YAML mirror, and MCP manifest entirely in Go.

```bash
make contract-write
make contract-test
make verify
```
