# ADR-0005: CodeGraph 1.2.0 local-index CLI contract for CHAOS-3007

**Status:** Accepted
**Date:** 2026-07-19

## Numbering note

The approved plan (`.omo/plans/context-fabric-clients.md`, Task 1) named this
document `docs/adr/0004-codegraph-local-index-provider.md`. By the time this
task ran, `docs/adr/0004-deployment-ownership.md` had already merged to
`main` and claimed that number. This ADR is filed as **0005** instead; every
plan and task cross-reference to `0004-codegraph-local-index-provider.md`
refers to this file.

## Context

CHAOS-3007 (Tasks 2-11) will add a `LocalIndexProvider` that federates
bounded local CodeGraph evidence into ACR's hosted context packet. Before any
of that code exists, this task pins the exact, narrow CodeGraph CLI surface
that implementation is allowed to depend on, so:

- CodeGraph remains the sole owner of repository parsing, graph/index
  creation, refresh, and storage (see the plan's Boundaries section);
  ACR only ever consumes an existing index read-only.
- Every later task decodes against fixtures captured here instead of
  re-deriving the contract from CodeGraph's CLI a second time.
- A regression in CodeGraph's CLI output shape is caught by
  `scripts/codegraph/verify-contract.sh` instead of by a downstream adapter
  silently misbehaving.

All fields, commands, and caps below were observed directly against the
installed `codegraph` binary (`codegraph --version` → `1.2.0`) run read-only
against this monorepo's indexed checkout at commit
`439acd2e79f1833bd403d5b0a7ad8288c505dbab`. `.codegraph/codegraph.db`'s
sha256 (`6a3009d317cec2fb4bad21a6343b50f8f95749d8b4abb5ef2c7d66686dca369c`)
was identical before and after every observation command in this task,
proving each one was read-only.

## Decision

### Supported version range

`>=1.2.0,<2.0.0`. Observed installed version: `1.2.0`.

### Permitted production argv (exactly these, always with `--json`)

1. `codegraph status --json`
2. `codegraph query --json <search> [--limit N]`
3. `codegraph callers --json <symbol> [--limit N]`
4. `codegraph callees --json <symbol> [--limit N]`
5. `codegraph impact --json <symbol> [--depth N]`
6. `codegraph affected --json [files...] [--stdin] [--depth N] [--filter GLOB]`
7. `codegraph files --json [--filter DIR] [--pattern GLOB] [--max-depth N] [--no-metadata]`

No other CodeGraph subcommand, flag, or output mode may be invoked, or
documented as a production data source, anywhere in this repository.

### Forbidden commands and behaviors

- `init`, `index`, `sync`, `uninit`, `explore`, `node`, `daemon`/`daemons`,
  `unlock`, `install`, `uninstall`, `telemetry`, `upgrade` — never invoked by
  ACR in any mode. `init`/`index`/`sync` build or mutate the index;
  `explore`/`node` emit mixed human/JSON text never suitable for parsing;
  the rest manage installation, daemons, or telemetry unrelated to reading
  an existing index.
- Non-JSON output modes (omitting `--json`, or `--format text|table|tree` on
  `files`) — never parsed as a data source.
- Direct reads of `.codegraph/*.db`, `.codegraph/*.db-wal`,
  `.codegraph/*.db-shm`, or any other CodeGraph-internal file — the CLI's
  JSON output is the only supported interface; ACR never touches
  CodeGraph's SQLite storage.
- Parsing `codegraph explore` or `codegraph node` output as a data source.
- Claiming or inferring an indexed Git commit or ref. CodeGraph 1.2.0's
  `status --json` exposes no commit/ref field. Every consumer that would
  otherwise report an indexed commit MUST instead emit the literal sentinel
  `indexed_commit_unknown` and MUST NOT substitute the working tree's
  current `HEAD`, infer it from `.codegraph/codegraph.db`, or infer it from
  `lastIndexed`/`pendingChanges` timestamps.

### Production caps

- At most **8** CodeGraph commands per ACR task/request.
- At most traversal depth **2** for any command accepting `--depth`/
  `--max-depth`.
- `affected`'s own CLI default depth is `5`; production argv MUST pass
  `--depth 2` explicitly — never rely on the tool default.
- `impact`'s CLI default depth is already `2`; production argv may pass
  `--depth 2` explicitly for clarity but must never exceed it.
- `files --max-depth` is capped at `2`.

### Required JSON fields per command

| Command | Shape | Required fields |
| --- | --- | --- |
| `status` | object | `initialized`, `version`, `projectPath`, `indexPath`, `lastIndexed`, `fileCount`, `nodeCount`, `edgeCount`, `dbSizeBytes`, `backend`, `journalMode`, `nodesByKind`, `languages`, `pendingChanges` (`added`/`modified`/`removed`), `worktreeMismatch` (nullable), `index` (`builtWithVersion`/`builtWithExtractionVersion`/`currentExtractionVersion`/`reindexRecommended`) |
| `query` | array | each item requires `node` + `score`; `node` requires `id`/`kind`/`name`/`qualifiedName`/`filePath`/`language`/`startLine`/`endLine`/`startColumn`/`endColumn`/`signature`/`visibility`/`isExported`/`isAsync`/`isStatic`/`isAbstract`/`updatedAt` |
| `callers` | object | `symbol`, `callers` (array); each entry requires `name`/`kind`/`filePath`/`startLine`; empty array is valid |
| `callees` | object | `symbol`, `callees` (array); each entry requires `name`/`kind`/`filePath`/`startLine`; empty array is valid |
| `impact` | object | `symbol`, `depth`, `nodeCount`, `edgeCount`, `affected` (array); each entry requires `name`/`kind`/`filePath`/`startLine` |
| `affected` | object | `changedFiles`, `affectedTests`, `totalDependentsTraversed` |
| `files` | array | each entry requires `path`/`language`/`nodeCount`/`size` |

### Additive-field tolerance / missing-field rejection

Any JSON object above may gain additional fields in a future 1.x CodeGraph
release. Consumers MUST tolerate and ignore unrecognized fields. Absence of
any field listed as required above is a hard rejection; the local provider
degrades to hosted-only behavior rather than parsing a partial result
(implemented by Task 5, not this task).

### Local-only fields

`status --json`'s `projectPath` and `indexPath` are absolute local
filesystem paths. They MUST NOT be persisted, sent to the hosted API, or
preserved verbatim in fixtures or evidence checked into version control.
This ADR's canonical fixture replaces them with the literal placeholder
strings `"<local-only:absolute-project-path>"` and
`"<local-only:absolute-index-path>"`.

## Verification

`scripts/codegraph/verify-contract.sh` statically validates
`testdata/codegraph/v1.2.0/` against this contract. It never executes the
`codegraph` binary — it only parses the checked-in JSON fixtures — so it
cannot mutate a CodeGraph index by construction.

- `--scenario happy` (default): every canonical fixture (`status`, `query`,
  `callers`, `callees`, `impact`, `affected`, `files`) satisfies its
  required-field declaration and carries no forbidden indexed-commit field;
  the additive fixture is tolerated; a deliberately incomplete fixture is
  confirmed rejected. Exit 0.
- `--scenario forbidden-command`: rejects a captured attempt to invoke
  `codegraph explore`. Exit 1.
- `--scenario inferred-indexed-commit`: rejects a fixture that wrongly
  substitutes the working tree `HEAD` for an indexed commit. Exit 1.
- `--scenario unsupported-version`: rejects a fixture reporting version
  `1.1.9`, which predates the supported range. Exit 1.
- `--scenario non-json-mode`: rejects a captured attempt to invoke
  `codegraph query` without `--json`. Exit 1.
- `--scenario missing-field`: rejects a `status` fixture missing `fileCount`.
  Exit 1.
- `--scenario additive-field`: accepts a `status` fixture carrying an
  unknown `futureDiagnostic` field. Exit 0.
- `--scenario sqlite-access`: rejects a captured attempt to read
  `.codegraph/codegraph.db` directly. Exit 1.

## Consequences

- Tasks 2-11 decode against `testdata/codegraph/v1.2.0/` instead of
  capturing their own ad hoc CodeGraph output, keeping the adapter's fixture
  corpus and this contract in lockstep.
- Any future CodeGraph major version bump (`>=2.0.0`) requires a new ADR and
  a new `testdata/codegraph/v2.x.y/` fixture set; this ADR does not attempt
  to anticipate breaking changes.
- Because CodeGraph 1.2.0 never exposes an indexed commit, every downstream
  freshness/provenance surface (Task 5 onward) must carry the literal
  `indexed_commit_unknown` sentinel rather than inventing one.
