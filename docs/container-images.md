# Container images

`Dockerfile` has exactly two production targets: `acr-api` (plus the separate
`acr-migrate` and `acr-projector` commands) and `acr-mcp`, a local STDIO
sidecar—not a daemon or a Compose/Kubernetes service.

All binaries are static cross-builds (`CGO_ENABLED=0`, `-trimpath`, cleared
Go build ID, `-buildvcs=false`, `SOURCE_DATE_EPOCH`) carrying the actual clean
commit's metadata. They run as numeric UID/GID `65532:65532`, accept a
read-only root filesystem, and receive no credentials or runtime configuration
at build time. The API target is Distroless, containing only CA certificates,
`acr-api`, `acr-migrate`, and `acr-projector`; it has no shell or package
manager. `acr-projector` (Context Fabric's projection worker, CHAOS-3753) is
deployed as its own Compose service / Kubernetes Deployment from this same
image, entrypoint overridden to `acr-projector serve` — independent
lifecycle and scaling, not a separate build, exactly like `acr-migrate`'s
`up` command today.

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
| Go builder | `golang:1.27.0-alpine3.23@sha256:3747dcba41c8b0db3211fda4db61638b980e17ac5bb3c94460a975a9cfe19395` |
| API runtime | `gcr.io/distroless/static-debian12:nonroot@sha256:1b7b9f0f0e0a1d2155f531db587cc48ec26aaf97ab64364225f5bf18a054e66a` |
| MCP runtime | `cgr.dev/chainguard/git:latest@sha256:1d0957e6ec5f9586d91ded20999b1c029d4b24107d20b409fbb0992ed164d8f6` |
| QEMU binfmt | `docker.io/tonistiigi/binfmt:qemu-v10.2.3@sha256:400a4873b838d1b89194d982c45e5fb3cda4593fbfd7e08a02e76b03b21166f0` |
| Buildx CLI | `v0.35.0` linux-amd64 asset, SHA-256 `d41ece72044243b4f58b343441ae37446d9c29a7d6b5e11c61847bbcf8f7dfda` |
| BuildKit driver | `moby/buildkit:v0.31.0@sha256:a095b3d11ce1a9a05b6064ef515dfca0291ec5bcf2ea8178da8f6461924294e1` |
| Scanner | `aquasec/trivy:0.69.3@sha256:bcc376de8d77cfe086a917230e818dc9f8528e3c852f7b1aff648949b6258d1c` |
| SBOM generator | `anchore/syft:v1.46.0@sha256:473a60e3a58e29aca3aedb3e99e787bb4ef273917e44d10fcbea4330a07320bb` |
| Migration smoke database | `postgres:18-alpine@sha256:a1d02e4bd40c94d3bf2bdd3678c137388e76d9efcd23c285e9429d336a834b44` |
| Compose E2E PostgreSQL helper | `postgres:18-alpine@sha256:a1d02e4bd40c94d3bf2bdd3678c137388e76d9efcd23c285e9429d336a834b44` |
| Compose E2E ClickHouse helper | `clickhouse/clickhouse-server:latest@sha256:f90a77560f72b10802106ee49e9870e41668cbc496e280c3911f6e3b216657f3` |
| Compose E2E PgBouncer helper | `edoburu/pgbouncer:latest@sha256:4c1ca296ef525f108f5d3552cc337c0c09587cf8dae7f0067fd93349e47dc1cd` |
| Compose E2E Valkey helper | `valkey/valkey:9-alpine@sha256:ee91f7a174ac4d6a6b0685b3a60e321f0a9dbbb691f9b0e285be2ba1d1be8328` |
| Compose E2E Mailpit helper | `axllent/mailpit:latest@sha256:d5ecbb067db3705fa953d79e1b7f81ef84038df67aba6c52825d8c02a1ea748a` |
| Compose E2E TLS proxy | `nginx:1.27-alpine@sha256:65645c7bb6a0661892a8b03b89d0743208a18dd2f3f17a54ef4b76fb8e2f2a10` |
| Context Fabric graph backend (ADR 0009, profile-gated) | `falkordb/falkordb@sha256:ad09d5051bbda1cfee8cef9d7f41ffe1bcb1c5327b82c442c989e84ab8cc33d3` (FalkorDB 4.20.2, module ver 42002, Redis 8.6.3) |

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
and reports tag/digest drift. It is an on-demand command, deliberately not a CI
gate: every image is pinned by digest in the Dockerfile and the compose files,
so a moved tag cannot change what is built, and nine of the reviewed references
sit on tags that move by design (`:latest`, `nonroot`, rolling series such as
`postgres:18-alpine`). Gating on it produced red builds on upstream's release
cadence rather than on anything under review. Run it when you want to know
whether a newer image exists. What still gates, offline and without flaking, is
the pin list itself: `scripts/e2e/test-compose.sh` requires every compose image
to appear in it, so an unreviewed image cannot enter the stack. The Trivy
**scanner** image above is pinned the same way as every other tool image.
The Trivy **vulnerability database** is the deliberate exception: it carries
no pin anywhere in source. See "Vulnerability scanning" below for why, and
for how a scan failure is triaged.

## The ghcr.io/full-chaos/dev-health-acr mirror (CHAOS-4855)

Every image above except the two already-non-Docker-Hub runtimes
(`gcr.io/distroless`, `cgr.dev/chainguard`) is pulled from Docker Hub, and
until CHAOS-4855, CI did so **anonymously** -- acr has never held Docker Hub
credentials, so every pull drew on the shared per-runner-IP anonymous quota.
That quota ran out on 2026-09-02 and failed a `container-contract-smoke` run
mid-pull. dev-health-ops hit the same class of failure against its own
(authenticated) account and fixed it by mirroring every Docker Hub pull to
`ghcr.io/full-chaos` (#2111); `.github/workflows/mirror-images.yml` copies
that pattern here, adjusted for where acr's own pulls are -- and for one
thing #2111 didn't have to deal with (next paragraph).

The destination is `ghcr.io/full-chaos/dev-health-acr/<repo>`, i.e.
`ghcr.io/<owner>/<repo>` (acr's own), **not** the flat `ghcr.io/full-chaos`
ops's #2111 mirror uses, and that is load-bearing rather than a style choice:
`ghcr.io/full-chaos` looks like a shared org-level namespace (ops's mirror
already publishes `postgres`, `clickhouse/clickhouse-server`,
`testcontainers/ryuk`, and `edoburu/pgbouncer` there), but GHCR package
**write** access is scoped to whichever repo's token created the package, not
shared org-wide -- confirmed the hard way: the first version of this mirror
tried to publish acr's postgres digest under ops's existing
`ghcr.io/full-chaos/postgres` package (a different tag, same flat path) and
got `403 Forbidden, denied: permission_denied: write_package`. Reads of those
PUBLIC packages still work from any repo -- so `testcontainers/ryuk` and
`edoburu/pgbouncer` remain readable via ops's copies wherever a pull happens
to reach them -- but this workflow cannot ADD anything to a package it did
not create. So every image this workflow **writes** lands under acr's own
path, including its own copy of the reaper (`testcontainers/ryuk`):
`TESTCONTAINERS_HUB_IMAGE_NAME_PREFIX` is one prefix applied uniformly to
every testcontainers pull, so it cannot resolve the reaper from a different
namespace than postgres/clickhouse/falkordb.

Every pull is redirected through one of two mechanisms, chosen entirely by
what does the pulling -- neither touches a pinned digest:

- **testcontainers-go's own `TESTCONTAINERS_HUB_IMAGE_NAME_PREFIX`** (set to
  `ghcr.io/full-chaos/dev-health-acr` in the `unit`, `race`, and `build` CI
  jobs): covers every `postgres`/`clickhouse`/`falkordb` testcontainers
  fixture and the library's own hardcoded reaper (`testcontainers/ryuk`),
  with zero Go source changes -- the library prepends the registry to
  whatever ref the code already declares.
- **`ACR_IMAGE_MIRROR_PREFIX`** (set to `ghcr.io/full-chaos/dev-health-acr/`,
  trailing slash included, in the `container-contract-smoke`,
  `container-reproducible`, `container-oci-scan`, and release.yml
  `container` jobs): covers everything that mechanism can't reach -- the
  Dockerfile's `golang` base image (via a build ARG), the QEMU binfmt and
  BuildKit driver images `docker/setup-qemu-action`/`docker/setup-buildx-
  action` pull, and the `aquasec/trivy` / `anchore/syft` / migration-smoke
  `postgres` pulls in `scripts/container/scan.sh` and `verify.sh`. Empty by
  default, so a local build or script run still pulls straight from Docker
  Hub, unchanged.

Every digest-pinned image is mirrored under a synthetic
`mirror-<first-12-of-the-sha256>` tag rather than the upstream tag: every
consumer above requests the ref by full digest, which Docker resolves
regardless of what tag(s) exist, so the tag is purely cosmetic bookkeeping
for this now-acr-owned path. The one exception is `clickhouse/clickhouse-
server`: its pin (`internal/chfixture.Image`, CHAOS-4549) is a bare tag with
no digest, by design, so `TESTCONTAINERS_HUB_IMAGE_NAME_PREFIX` requests it
by that literal tag and the mirror must publish under the same literal tag --
there is no digest to derive a synthetic one from. `testcontainers/ryuk` is
the other tag-only entry, restated in the workflow rather than read from any
file in this repo (testcontainers-go's own hardcoded reaper default). See
`mirror-images.yml`'s own header comment for the full reasoning.

Compose-only pulls (the root `compose.yml`'s ClickHouse/PgBouncer/Valkey/
Mailpit/nginx helpers, and this repo's own `deploy/compose/acr.compose.yml`
overlay) are out of scope: neither runs in GitHub Actions CI, and per the
Context Fabric lane brief, Compose is not the supported way to run acr or
Ask Dev locally any more.

### Adding or re-pinning a mirrored image: the bootstrap race

`.github/workflows/mirror-images.yml` and `ci.yml` are independent
workflows triggered by the same push, with no cross-workflow `needs` (GitHub
Actions has none). If a change adds a new image to `scripts/ci/resolve-
mirrored-images.sh`'s list or re-pins an existing one to a digest the mirror
has never published, the FIRST push carrying that change races: `ci.yml`'s
jobs can reach their pull before `mirror-images.yml`'s push lands, and every
one of them fails `manifest unknown` at once (this happened on CHAOS-4855's
own introducing PR -- six jobs, one root cause).

`ci.yml`'s `mirror-preflight` job (which every image-pulling job `needs`)
turns that race into ONE named failure instead of six: it resolves every
entry from `resolve-mirrored-images.sh` against ghcr before anything else
runs, and fails with `MISSING <image> -- run "Mirror images" (workflow_dispatch)
and wait for it to complete, then re-run this job` naming exactly what is not
there yet. It does not eliminate the race, only makes it legible. **The
fix, when `mirror-preflight` fails this way:** manually dispatch `Mirror
images` (Actions tab or `gh workflow run mirror-images.yml`), wait for it to
complete, then re-run the failed `ci` jobs on the same commit -- do not
push again, and do not treat the failure as a defect in the change itself
unless `Mirror images` also fails (in which case see that workflow's own
run log; a `403 permission_denied: write_package` there means something
tried to write into a package acr's token cannot write to -- see this
document's own mirror-namespace note above for why that happens and how it
was fixed for CHAOS-4855's own introducing set).

## Build context and verification

Local builds that select the canonical `Dockerfile` use
`Dockerfile.dockerignore`, whose reviewed allowlist admits exactly `Dockerfile`,
`go.mod`, `go.sum`, Go sources under `cmd/**`, Go and embedded JSON under
`internal/**`, and Go plus SQL migration files under `migrations/**`. This makes
`docker build .` and Docker Compose useful for local development without sending
files outside those reviewed paths and extensions to BuildKit. Local builds may
include ignored files whose paths and extensions match the allowlist; keep local
credentials outside source directories. Builders without Dockerfile-specific
ignore support fall back to `.dockerignore` and fail closed.

Release-capable builds use the stricter wrapper path. The wrapper invokes
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
directly inspects the generated release context, verifies required Go, embedded
JSON, and migration sources are present in the local build while the sentinel
files are absent, verifies ignored source files stop the wrapper before BuildKit
runs, and finally checks the sentinel is absent from both runtime filesystems.
Concurrent smoke invocations therefore never mutate or race on the shared
worktree.

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

`container-oci` produces local OCI archives for `linux/amd64` and
`linux/arm64`; verification requires Linux platform labels and extracts each
target executable to inspect its ELF header (`x86-64` or `AArch64`). A tagged
Release workflow renames the verified outputs to
`acr-api_VERSION_linux_multiarch.oci.tar` and
`acr-mcp_VERSION_linux_multiarch.oci.tar`, records their archive and OCI index
digests in `container-release-manifest.json`, and includes them in the final
Actions artifact and GitHub Release.

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

`container-scan` writes four Trivy reports and four SPDX JSON SBOMs:

- `acr-api-amd64`, `acr-api-arm64`
- `acr-mcp-amd64`, `acr-mcp-arm64`

The ordinary local target builds independent layouts. The tagged Release
workflow sets `CONTAINER_SCAN_OCI_ROOT=.tmp/container-oci`, so Trivy and Syft
select both platforms directly from the exact multi-platform archives that
are later attached and copied to GHCR; it performs no release-scan rebuild.

Each invocation uses unique OCI layout, scanner cache, and report staging
roots. It pulls the digest-pinned Trivy and Syft images, resolves the
`trivy-db` mirror tag to one immutable digest, downloads that exact
snapshot, validates its `metadata.json` freshness, and performs all four
scans with `--skip-db-update`, `--network none`, and the same recorded
cache. Only after every scanner command and report validator succeeds does a
bounded publication lock atomically point `.tmp/container-reports` at the
new immutable report generation; failed or concurrent runs cannot replace
the last known-good reports, expose a missing stable path, or leave
work/lock state behind. It fails on every HIGH/CRITICAL finding, including
unfixed findings, and currently has no exceptions. A future exception must
be a reviewed, tracked ignore file entry with CVE, narrow package/image
scope, rationale, owner, and expiration date — then be removed at expiry or
the gate must fail again.

### Vulnerability scanning: DB pinning removed, not the check (CHAOS-3772)

Trivy previously scanned these images against a `trivy-db` snapshot pinned
by immutable digest **in source**, refreshed on a manual weekly cadence and
gated by `TRIVY_DB_MAX_AGE_HOURS=168` against that pin's own age. An
immutable digest's `UpdatedAt` never advances once pinned, so that gate was
guaranteed to go red exactly 168 hours after each manual refresh — by
construction, not by accident. It expired and failed the containers CI
check repo-wide twice within roughly two weeks — #74 fixed the first
expiry by hand; the second, on 2026-08-12, blocked all pushes until #82
removed the scan outright rather than leave it as a recurring time bomb.
CHAOS-3772 tracks this reintroduction.

The fix is not a static pin refreshed faster or by a bot: any value
committed to source can still go stale while code is untouched. Instead,
`scan.sh` resolves the `trivy-db` mirror tag (`ghcr.io/aquasecurity/trivy-db:2`)
to an immutable digest **at scan time**, on every run, and downloads exactly
that resolved snapshot. No trivy-db value ever sits in source, so wall-clock
passage against a fixed pin cannot happen again — there is no fixed pin.
The resolved digest, the resolution timestamp, and the downloaded
`metadata.json` are written to the run's reports (`trivy-db-snapshot.txt`,
`trivy-db-metadata.json`) so exactly which DB snapshot scanned a given run
stays auditable, without needing that value to stay fixed.

The Trivy **scanner binary** stays pinned by immutable digest like every
other tool image in this pipeline — it is code, and code is pinned. The
vulnerability **database** is not, because a DB whose entire purpose is
reflecting current CVE knowledge defeats that purpose the moment it is
frozen. Pin code; let necessarily-changing threat-intel data float; record
what was actually used.

The freshness check on the downloaded snapshot's `UpdatedAt` is kept
(`TRIVY_DB_MAX_AGE_HOURS`, default 168h), but its meaning changed: since the
digest is freshly resolved every run, a large gap now means the upstream
`trivy-db` feed itself looks stalled — a real signal about the mirror, not
about our own inaction. `scripts/container/lib/trivy-db-freshness.sh`
implements this as a standalone, sourceable function so it stays unit
testable without Docker; `scripts/container/test-trivy-db-freshness.sh`
proves it still rejects a stale snapshot (the same shape of staleness the
old pin-expiry bug produced) and runs as part of `container-contract`.

**A scan failure has two distinct, differently worded shapes; do not confuse
them:**

- `trivy-db mirror unreachable` / `trivy-db download failed` / `trivy
  scanner image unreachable`: a transient registry or mirror outage,
  exactly like a pull failure for any other digest-pinned tool image in
  this pipeline (Syft, BuildKit, QEMU). Self-heals on retry. Not a
  vulnerability finding, not a stale pin.
- `HIGH/CRITICAL vulnerabilities in <target>`, followed by the actual CVE
  ID, package, installed/fixed versions, and severity: a real, newly
  published finding against an image whose code did not change. **This is
  the intended, correct behavior of a vulnerability scanner, not a
  regression of the CHAOS-3772 class.** The old defect was the gate going
  red while *both* code and the DB pin were frozen. Here the DB is
  deliberately never frozen, so a new red means new externally published
  vulnerability information, not the passage of time against our own
  static value. Triage it as a real finding: confirm the CVE against the
  reported package, and update the affected dependency or base image. There
  is currently no suppression mechanism (see the "no exceptions" note
  above) — a HIGH/CRITICAL finding must be fixed, not waited out. If a
  narrow, time-boxed exception is ever genuinely needed, add reviewed
  `.trivyignore` support as its own change, with the CVE, package/image
  scope, rationale, owner, and expiration date all in the PR, not silently.

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

Ordinary `.tmp/container-*` archives, reports, images, and temporary containers
remain disposable. Release builds are the exception: the release workflow
copies the exact verified OCI archives, without rebuilding, to GHCR. Every
successful current-tip `main` build publishes both the full commit SHA and
`latest` for `acr-api` and `acr-mcp`; canonical version tags publish their own
immutable `vX.Y.Z[-dev.N|-beta.N]` references. The workflow verifies every
published tag and digest against the OCI index digest recorded in
`container-release-manifest.json`, then keylessly signs that immutable digest.
Before moving `latest`, it rechecks the current GitHub `main` ref so an older
run finishing out of order cannot roll the channel backward. The full commit SHA and `latest` therefore identify the same image only for the current tip of
`main`. Deployment and rollback references should still use the approved
`@sha256:` digest. The same OCI archives and container SBOMs are attached to
the `main-<full-sha>` or version-tagged GitHub Release for offline verification. See
[`release-policy.md`](release-policy.md).
