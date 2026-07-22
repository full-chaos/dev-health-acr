# Container images

`Dockerfile` has exactly two production targets: `acr-api` (plus the separate
`acr-migrate` command) and `acr-mcp`, a local STDIO sidecar—not a daemon or a
Compose/Kubernetes service.

Both binaries are static cross-builds (`CGO_ENABLED=0`, `-trimpath`, cleared
Go build ID, `-buildvcs=false`, `SOURCE_DATE_EPOCH`) carrying the actual clean
commit's metadata. They run as numeric UID/GID `65532:65532`, accept a
read-only root filesystem, and receive no credentials or runtime configuration
at build time. The API target is Distroless, containing only CA certificates,
`acr-api`, and `acr-migrate`; it has no shell or package manager.

Every runtime probe (in `container-test`'s `verify.sh` and the fixture
commands in this document) additionally runs with `--cap-drop ALL` and
`--security-opt no-new-privileges`, on top of the existing read-only root
and non-root numeric user, so a probe can gain no Linux capability and no
new privilege even though neither image has a bind-mounted write target.

The MCP target assembles its runtime from the pinned Chainguard `git` image
because workspace discovery must run Git against the caller's read-only mounted
repository. The build-only base stage removes its `dash` and `sh` entries before
copying the filesystem into the final scratch target. The resulting runtime has
Git but no shell, BusyBox, or package manager. The smoke gate exports and
inspects both runtime filesystems, then proves Git can read a real read-only
mounted Git workspace as UID `65532` and `acr-mcp doctor --offline` succeeds.
The image also carries a protected, non-wildcard
`safe.directory=/workspace` system Git config so that UID `65532` operating
on a real bind-mounted repository owned by a different host UID (the normal
case on a Linux CI runner) does not trip Git's "detected dubious ownership"
refusal; verification exercises `acr-mcp workspace --path /workspace` (the
sidecar's own read-only workspace-discovery command, the same code path
`context_for_task` uses) against a real mounted fixture repository, not only
a direct `git` invocation, and asserts the reported commit SHA matches.

## Immutable inputs

Every executable build input is tag plus immutable OCI index digest (or a
GitHub action commit SHA):

| Purpose | Pinned input |
| --- | --- |
| Dockerfile frontend | `docker/dockerfile:1.20@sha256:26147acbda4f14c5add9946e2fd2ed543fc402884fd75146bd342a7f6271dc1d` |
| Go builder | `golang:1.26.5-alpine3.23@sha256:622e56dbc11a8cfe87cafa2331e9a201877271cbff918af53d3be315f3da88cc` |
| API runtime | `gcr.io/distroless/static-debian12:nonroot@sha256:f5b485ea962d9bd1186b2f6b3a061191539b905b82ec395de78cbfae51f20e35` |
| MCP runtime | `cgr.dev/chainguard/git:latest@sha256:7671e64c37b99739fd52eb5ae4299e957c5095e083d6ee5dcd1845ce850a7614` |
| QEMU binfmt | `docker.io/tonistiigi/binfmt:qemu-v10.2.3@sha256:400a4873b838d1b89194d982c45e5fb3cda4593fbfd7e08a02e76b03b21166f0` |
| Buildx CLI | `v0.35.0` linux-amd64 asset, SHA-256 `d41ece72044243b4f58b343441ae37446d9c29a7d6b5e11c61847bbcf8f7dfda` |
| BuildKit driver | `moby/buildkit:v0.31.0@sha256:a095b3d11ce1a9a05b6064ef515dfca0291ec5bcf2ea8178da8f6461924294e1` |
| Scanner | `aquasec/trivy:0.69.3@sha256:bcc376de8d77cfe086a917230e818dc9f8528e3c852f7b1aff648949b6258d1c` |
| Scanner DB snapshot | `ghcr.io/aquasecurity/trivy-db@sha256:d1f9baeef9aa5fc4c2c631ee8813033e7ec3442950e2b06d33aa1bc84618bc81` |
| SBOM generator | `anchore/syft:v1.46.0@sha256:473a60e3a58e29aca3aedb3e99e787bb4ef273917e44d10fcbea4330a07320bb` |
| Migration smoke database | `postgres:17-alpine@sha256:742f40ea20b9ff2ff31db5458d127452988a2164df9e17441e191f3b72252193` |
| Compose E2E PostgreSQL helper | `postgres:18-alpine@sha256:9a8afca54e7861fd90fab5fdf4c42477a6b1cb7d293595148e674e0a3181de15` |
| Compose E2E ClickHouse helper | `clickhouse/clickhouse-server:latest@sha256:1d1f6508eba2dccce2cee9913907c5f7766327debc57a6b1991f2c9e3176c163` |
| Compose E2E PgBouncer helper | `edoburu/pgbouncer:latest@sha256:4c1ca296ef525f108f5d3552cc337c0c09587cf8dae7f0067fd93349e47dc1cd` |
| Compose E2E Valkey helper | `valkey/valkey:9-alpine@sha256:c9b77919daeba2c02ad954d0c844cc4e7142069d177b89c5fd771f405daf9e02` |
| Compose E2E Mailpit helper | `axllent/mailpit:latest@sha256:5a49a77c5bdbe7c5474450b4f46348d09949df3695257729c93a30369382d4f6` |
| Compose E2E TLS proxy | `nginx:1.27-alpine@sha256:65645c7bb6a0661892a8b03b89d0743208a18dd2f3f17a54ef4b76fb8e2f2a10` |

CI action refs are commit-SHA pinned: `actions/checkout@9c091bb21b7c1c1d1991bb908d89e4e9dddfe3e0`,
`actions/setup-go@924ae3a1cded613372ab5595356fb5720e22ba16`,
`docker/setup-qemu-action@29109295f81e9208d7d86ff1c6c12d2833863392`,
`docker/setup-buildx-action@e468171a9de216ec08956ac3ada2f0791b6bd435`,
and `actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a`.
QEMU image caching and Buildx binary caching are disabled. CI downloads the
Buildx release asset itself and verifies its recorded SHA-256 before the pinned
setup action creates a builder with the digest-pinned BuildKit driver.

To refresh a container input, inspect the selected tag's OCI **index** digest
with `docker buildx imagetools inspect <image:tag>`, verify its amd64 and arm64
manifests, change the tag and digest together, and record upstream review in
the PR. For actions, resolve a reviewed release to its full commit SHA. Then
run all gates below. Never substitute a digest from memory or retain an old
digest under a new tag. `make container-pins` resolves every reviewed OCI tag
and rejects tag/digest drift. The Trivy database is a deliberately immutable
snapshot rather than a stable release tag: refresh its manifest and layer
digests together at least weekly, confirm `metadata.json` age and chronology,
and retain the snapshot identity beside the scan reports.

## Build context and verification

Raw `docker build .` is intentionally unsupported and fails closed:
`.dockerignore` sends only `Dockerfile`, so repository sources and unrelated
files never enter that BuildKit context. The supported build wrapper invokes
`create-context.sh` to construct a fresh context containing exactly
`Dockerfile`, `go.mod`, `go.sum`, Go sources under `cmd/**`, Go and embedded
JSON under `internal/**`, and Go plus SQL migration files under
`migrations/**`. It rejects symlinks for required root files. Consequently,
unrelated fixtures and ignored `.env*` files cannot enter BuildKit or an
exported build cache even when they are nested beneath an approved source
directory. The build wrapper additionally fails closed if Git status cannot be
inspected or an ignored file exists below one of those source roots.

The smoke test creates an invocation-owned Git snapshot that includes the
current tracked and untracked source state, then creates arbitrary top-level
files and an ignored nested `.env*` file inside that disposable snapshot. It
directly inspects the generated context, proves a raw repository build cannot
receive product sources, verifies ignored source files stop the wrapper before
BuildKit runs, and finally checks the sentinel is absent from both runtime
filesystems. Concurrent smoke invocations therefore never mutate or race on the
shared worktree.

Runtime verification starts the digest-pinned Postgres fixture on a unique
Docker network with tmpfs-backed storage and generated per-run credentials. It
runs `acr-migrate up` successfully from the API image, reruns it to prove the
no-op path is idempotent, and inspects migration status. Fixture credentials and
DSNs remain process-local and are never printed or written to reports.

```bash
make container-contract
make container-pins
make container-test
make container-oci
make container-scan
```

`container-oci` produces unpushed OCI archives for `linux/amd64` and
`linux/arm64`; verification requires Linux platform labels and extracts each
target executable to inspect its ELF header (`x86-64` or `AArch64`).

`scripts/container/build.sh` writes each OCI export to a unique temporary path
and renames it into place only after `validate-oci.sh` confirms every
recursively referenced descriptor's presence, size, digest, and layer
readability, so a reader never observes a partial or internally inconsistent
archive. A build killed by its own `CONTAINER_BUILD_TIMEOUT`-bounded process
timeout (default 900s; portable across macOS and Linux without depending on GNU
coreutils' `timeout`) never leaves truncated bytes at the final path. Timeout,
interrupt, and termination handling recursively terminates retained
descendants, waits a bounded grace period, and uses SIGKILL before reaping a
TERM-ignoring Buildx process.

`container-oci` gives every invocation a unique work root and serializes only
the final publication step with a bounded lock. It validates the complete
candidate set, moves it to an immutable generation, and atomically replaces the
`.tmp/container-oci` symlink only after that generation is ready. Failed
publications leave the previous pointer untouched, and work and lock state are
removed on every exit; a dead publisher's PID-marked lock is recovered by the
next invocation. The current and immediately previous immutable generations
are retained, bounding disk use while concurrent runs build independently and
unlocked readers continue to resolve one complete archive generation.
`verify-oci.sh` recursively validates every descriptor (index, manifest,
config, and each layer) by re-extracting its blob and comparing the real
byte size and sha256 against what the descriptor claims, validates the
image config (`os`/`architecture`, numeric non-root `User`, `Entrypoint`, and a
string-array `Env` containing no secret-shaped entries), and merges layers with
OCI whiteout semantics (a later layer's `.wh.<name>` marker discards an
earlier layer's same-named entry) so the extracted binary reflects the
actual final merged filesystem rather than the first layer that happens to
contain a same-named entry.

`container-scan` builds four independent OCI layouts and writes four Trivy
reports and four SPDX JSON SBOMs:

- `acr-api-amd64`, `acr-api-arm64`
- `acr-mcp-amd64`, `acr-mcp-arm64`

Each invocation uses unique OCI layout, scanner cache, and report staging roots.
It pulls the digest-pinned Trivy and Syft images, downloads the pinned Trivy DB
snapshot once, validates the expected DB layer plus `metadata.json` freshness,
and performs all four scans with `--skip-db-update`, `--network none`, and the
same recorded cache. Only after every scanner command and report validator
succeeds does a bounded publication lock atomically point `.tmp/container-reports`
at the new immutable report generation; failed or concurrent runs cannot replace
the last known-good reports, expose a missing stable path, or leave work/lock
state behind. It fails on every
HIGH/CRITICAL finding, including unfixed findings, and currently has no
exceptions. A future exception
must be a reviewed, tracked ignore file entry with CVE, narrow package/image
scope, rationale, owner, and expiration date—then be removed at expiry or the
gate must fail again.

`container-reproducible` refuses a dirty product worktree, derives identity
from `HEAD`, creates a `git archive HEAD` snapshot, and performs two clean
no-cache builds from that snapshot. It compares the final application layer
and extracted binary hashes. **After the feature commit is created, rerun this
command from that clean committed checkout before delivery**; an uncommitted
worktree is intentionally rejected. Each no-cache build also receives a fresh
Go module/build cache ID, so persistent BuildKit cache mounts cannot satisfy a
reproducibility build.

`scripts/container/build.sh` itself (used by every target above) also
refuses a dirty source tree by default, for the same reason: an artifact
built from uncommitted changes but labeled with the last committed HEAD's
identity would misrepresent its own provenance. `CONTAINER_ALLOW_DIRTY=1` is
the explicit, narrow local precommit-QA opt-in; it does not silently pass
the dirty tree off as clean -- the resulting `VERSION` and `COMMIT` build
args are both suffixed `-dirty` so the image is never confused for one
built from the labeled commit alone.

All `.tmp/container-*` archives, reports, images, and temporary containers are
disposable. The CI upload is short-lived reports only; no target pushes or
publishes an image.
