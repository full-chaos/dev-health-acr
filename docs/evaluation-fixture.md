# ACR evaluation fixture corpus

CHAOS-2918 delivers a deterministic, public-safe evaluation corpus that
unblocks CHAOS-2905 (the Go context assembler). This document explains what
the corpus is, how it was produced, and how to bootstrap or regenerate it in
a clean checkout.

## What this is

`testdata/evaluation/v1/` is a fixed, versioned, synthetic corpus:

```text
testdata/evaluation/v1/
  scenario.json     Fictional repository and scenario description
  tasks.json         >= 3 fixed evaluation tasks with expected outcomes
  manifest.json       SHA-256 manifest over every corpus file
  evidence/            Synthetic source-evidence records referenced by tasks
```

`internal/evalfixture` is the Go package that loads and verifies this
corpus. It never fabricates ACR packet output; it only proves the corpus
itself is well-formed, hash-stable, and internally consistent. CHAOS-2905
imports `internal/evalfixture` as a test helper and reads only
`testdata/evaluation/v1/**` — it never depends on customer or production
data.

## Clean-room provenance

Every identifier, commit SHA, PR number, and evidence excerpt in this corpus
was authored directly for ACR evaluation. None of it was copied, scraped, or
derived from a real repository, customer account, or production system:

- The repository (`example-org/widget-service`) and its remote URL are
  fictional and resolve to nothing.
- Commit SHAs are synthetic 40-character hex strings, not real Git objects.
- Evidence `safe_uri` values use the reserved `.invalid` TLD (RFC 2606) so
  they can never be dereferenced, consistent with the repository rule that
  outbound fetching of evidence URLs is disabled.
- No agent episode, transcript, or license artifact appears anywhere in the
  corpus.

## Task coverage

`tasks.json` defines three fixed tasks:

1. `task-001-checkout-flake-exact-commit` — exact-commit scope
   (`branch` + `commit_sha`), two evidence records, `expected_status:
   "complete"`.
2. `task-002-auth-refactor-branch` — branch-filtered scope (`branch` only,
   no commit pin), two evidence records, `expected_status: "complete"`.
3. `task-003-unindexed-branch-empty` — the controlled degraded/empty case.
   Its branch intentionally has zero indexed evidence, so
   `expected_evidence_ids` is empty and `expected_status: "empty"`.

Together these exercise exact-commit scope, branch-only scope, and one
degraded/empty outcome, as required by the fixture prerequisite.

## Verifying the corpus

```bash
go test ./internal/evalfixture -run TestVerifyCorpus -count=2
go test ./internal/evalfixture -count=1
```

`VerifyCorpus` re-hashes every manifest-listed file, confirms no file is
missing or unlisted, confirms every task's `expected_evidence_ids` resolve
to a real evidence record (except the controlled empty case), and confirms
the minimum task count and branch/commit-SHA scope coverage. Two runs over
the same directory produce byte-identical results because the corpus is
static, tracked, and never touched by the verifier itself.

## Regenerating `manifest.json` after editing fixtures

The verifier is read-only by design; it does not rewrite fixtures. If you
add or edit a corpus file, regenerate the manifest by hand with the
standard `shasum` tool from the corpus directory, then update
`manifest.json` with the new digests:

```bash
cd testdata/evaluation/v1
shasum -a 256 scenario.json tasks.json evidence/*.json
```

Copy each `<hash>  <path>` pair into the matching `files[].sha256` /
`files[].path` entry in `manifest.json`. Do not add or remove files without
updating both `tasks.json` references and `manifest.json` in the same
change, and re-run `go test ./internal/evalfixture` before committing.

## Bootstrapping in a clean checkout

No credentials, network access, or private data are required:

```bash
git clone <this repository>
cd dev-health-acr
go test ./internal/evalfixture -count=1
go test ./... -count=1
make verify
```

If any of these fail on a fresh clone, the corpus or verifier has drifted
and must be fixed before CHAOS-2905 can rely on it.
