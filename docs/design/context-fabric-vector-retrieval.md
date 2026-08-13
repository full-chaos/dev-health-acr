# Context Fabric vector and semantic retrieval (CHAOS-3778)

Status: DESIGN — not implemented. Awaiting orchestrator GO.

Scope: TRD §19.4. Acceptance bars AC-3778-0 through AC-3778-7.
Precondition AC-3778-0 (lexical score-ladder normalization) is MERGED on `main`
(`falkorgraph/queries.go`, `graphrank/types.go`). This design composes with that
ladder; it does not re-open it.

---

## 1. Live probe results

Everything below was verified against a live server before this design was
written. No claim here is inferred from documentation.

### 1.1 Embedder — LM Studio

`GET http://localhost:1234/v1/models` serves six models. Five are generation
models (`qwen/qwen3.5-35b-a3b`, `google/gemma-4-e4b`, `google/gemma-4-31b`,
`google/gemma-4-26b-a4b-qat`, `gemma-4-26b-a4b-it@q4_k_m`). One is an embedding
model:

| Property | Value |
| --- | --- |
| Model id | `text-embedding-nomic-embed-text-v1.5` |
| Dimension | 768 |
| Vector norm | 1.0000 (already L2-normalized) |
| Batch input | Accepted (`input` as an array) |
| Cold latency | 9.3 s (first call — model load) |
| Warm latency | 10–17 ms, single input |

Because vectors arrive L2-normalized, cosine similarity equals the dot product.
No client-side normalization is needed.

The two generation models Chris named are not embedding models. The lane does
not need `/api/v1/models/load`: an embedding model is already loaded.

### 1.2 FalkorDB vector index

Server: module `graph` version 42002.

Creation (Cypher form, works):

```
CREATE VECTOR INDEX FOR (n:Subject) ON (n.embedding)
  OPTIONS {dimension:768, similarityFunction:'cosine'}
```

Query (works on `GRAPH.RO_QUERY`):

```
CALL db.idx.vector.queryNodes('Subject','embedding', $k, vecf32($vec))
  YIELD node, score
```

Verified behaviors:

| Behavior | Observed |
| --- | --- |
| **`score` is a cosine DISTANCE** | Identical vector → `0`. Unrelated vector → `0.699398`, which is exactly `1 − cos` (`cos = 0.3007`). Range `[0,2]`. |
| `WHERE node.org_id = $org` | Applies as a POST-filter on the k-NN result, not as a constraint on the search. |
| Dimension mismatch | Hard server error: `Vector dimension mismatch, expected 4 but got 2`. |
| Node without the property | Silently absent from results. No error. |
| Repeat index creation | `Attribute 'embedding' is already indexed` — not idempotent. |
| Coexistence with the fulltext index | Vector index on `Subject.embedding` and fulltext index on `Subject.search_text` coexist. Both keep working. |
| `db.indexes()` status | Reports `OPERATIONAL` and echoes `{dimension, similarityFunction, M, efConstruction, efRuntime}`. |

**The distance finding is the single most important one in this document.**
`graphrank.ResultConfidence` has a `score >= 0 && score <= 1 → return score` arm.
Feeding FalkorDB's vector score into it gives the BEST possible match a
confidence of `0.0` and a poor match `0.699`. That is D11 inverted a second
time, in a new place. `graphrank/types.go:88-95` already warns about exactly
this case for a future vector backend. This adapter therefore normalizes into
`Relevance` and never lets a vector score reach that arm.

---

## 2. Where embeddings live

**In FalkorDB, as a property on the existing `:Subject` node, with a per-org
vector index.** Not an external store.

- One graph key per organization already enforces tenancy. An external vector
  store would fork that boundary and add a second thing to authorize.
- Embeddings become projection artifacts of the same node that already carries
  `search_text`, `org_id`, and the `authorization_*` properties. Authorization
  keeps applying before ranking (§12) with no new code path.
- The rebuild/epoch machinery already covers node properties. See §6.

New node properties:

| Property | Type | Purpose |
| --- | --- | --- |
| `embedding` | vector | The vector itself, written via `vecf32(...)`. |
| `embedder_identity` | string | `provider/model` — records what produced the vector. |
| `embedder_dimension` | int64 | Records the dimension. Detects a model change. |

`falkorgraph.safeParams` (client.go:289) already admits `[]interface{}` of
`float64`, so a vector parameter passes the codec allowlist unchanged. The
pinned `falkordb-go` v2.1.0 codec is not touched, and the raw `Conn.Do`
discipline stays as-is: this is one more Cypher string through `a.api.query`.

Index bootstrap follows the existing pattern in `identity.go`
(`createFulltextIndex` + `pollConstraintsOperational`): create, treat
`already indexed` as success via `errAlreadyExists`, then poll `db.indexes()`
to `OPERATIONAL` under a strict allowlist. Zero rows is an error.

---

## 3. What gets embedded, and when

**Projection time, in `acr-projector`.** Never lazily at query time. This fits
the rebuild/epoch machinery and keeps the read path free of a model call for
the corpus side.

One vector per `:Subject` node, over the **same `search_text` property
projection already writes** (`projection.go:234, 318, 360`):

| Subject kind | `search_text` content today | Embedded |
| --- | --- | --- |
| entity | label + aliases + previous names | yes |
| content | title + body | yes, truncated |
| episode | summary (goal/outcome) | yes, truncated |

Reusing `search_text` verbatim means the lexical and vector paths search the
same text. A difference in outcome is then a difference in *mechanism*, not a
difference in corpus — which is what AC-3778-2's measurement needs to be
meaningful.

Text is truncated to a bounded character budget before embedding (config,
default 2000) so one large document cannot blow the projection batch budget.

**One model call per batch, not per node.** The embedder port takes a slice.

Untrusted content keeps its untrusted flag: embedding reads the text, it never
executes it, and no embedded text is echoed into any prompt by this change.

**Not embedded, by rule:** edges. §19.4.4 forbids a model in the write path of
an edge. Embedding a label is not creating an edge. `queryRelationships` is out
of scope for this pass (§7).

---

## 4. How vector scores merge into the normalized ladder

This is D11 territory. The design is *structural* — it makes the unwanted
outcomes arithmetically impossible rather than forbidding them by policy.

### 4.1 Distance to similarity

```
cos       := clamp(1 - score, 0, 1)      // score is a DISTANCE, see §1.2
```

### 4.2 An absolute similarity floor — the AC-3778-4 guard

A configured floor `τ` (default 0.55, tuned per embedder, never hardcoded per
model in code). If `cos < τ`, the candidate is **dropped, not scored**. It never
becomes a `CandidateNode`.

This is the honest-no-match guard. A k-NN query always returns k rows if k rows
exist — a no-match question would otherwise get k confident-looking neighbors.
AC-3778-4 is the highest-severity failure in this issue, so the guard sits at
the adapter boundary, before graphrank ever sees the row.

### 4.3 The vector band — [0.50, 0.70]

```
relevance := 0.50 + 0.20 * (cos - τ) / (1 - τ)      // absolute, per-candidate
```

The band ceiling **0.70 is strictly below the 0.72 lone-candidate commit gate**
in `ResolveFromMergedCandidates` (resolution.go:104). A vector-only candidate
therefore *cannot* auto-commit — not by a rule, but by arithmetic. AC-3778-3's
"a vector hit alone never commits a subject" holds with no special case in
graphrank, and the shipped thresholds stay untouched.

The function is absolute and per-candidate, exactly like
`fulltextRelevanceFromMatchedTerms`: it depends only on this candidate's own
`cos`, never on the result set. Two different queries' vector confidences are
directly comparable. This is the property Codex round 2 forced onto the lexical
arm, and it is honored here from the start.

Band layout, ordered and reasoned:

```
0.00 ─────────────────────────────────────────────────────── dropped (cos < τ)
0.50 ── vector-only ──────────── 0.70 │ 0.72 lone-commit gate
0.50 ── lexical-only ────────────────── 0.75  (merged, AC-3778-0)
0.72 ── corroborated ───────────────────────── 0.86 │ 0.88 top-of-two gate
1.00 ── exact canonical / alias / provider key (unchanged)
```

### 4.4 Corroboration — how the 25pp lift is actually earned

AC-3778-2 wants a 25pp rise in the **correct-commit** rate. AC-3778-3 says a
vector hit alone never commits. Both hold only if a vector hit can be
*corroborated* by a second mechanism and commit on the strength of the pair.

Today `ResolveSubjects` merges two findings of the same subject by keeping the
**higher confidence** (resolve.go:138). That discards the fact that two
independent mechanisms agreed — which is the single most useful signal available
here.

Proposal: `SubjectCandidate` records its **match mechanisms** (also required by
AC-3778-6), and the merge unions mechanisms instead of taking a max. A subject
proposed by two or more distinct mechanisms — from `{exact, alias, provider_key,
lexical, vector, traversal_parent}` — is scored into the **corroborated band
[0.72, 0.86]**, a bounded monotone function of the contributing mechanisms' own
bands.

Consequences, all intended:

- A corroborated candidate reaches the 0.72 lone gate, so it can auto-commit
  when unopposed. This is where the lift comes from.
- It never reaches 0.88, so **two** corroborated candidates still clarify rather
  than guess. That preserves the ruling recorded on the issue (2026-08-13):
  falling to clarification under genuine ambiguity is intended behavior.
- A vector hit alone stays at ≤ 0.70 and commits nothing. AC-3778-3 literal.
- `searchTruncated` still short-circuits to ambiguous before any threshold
  (resolution.go:80). Truncation authority is unchanged.

**This is the one change that reaches into `graphrank`'s merge rule, and the one
point where I want an explicit GO.** Without it, AC-3778-2 has no mechanical
path while AC-3778-3 holds. The alternative — letting a vector-only candidate
commit — is forbidden by the AC.

### 4.5 Order-soundness obligation

A test extends the AC-3778-0 monotonicity proof across the vector arm: for a
fixed mechanism set, a strictly higher `cos` never yields a lower confidence
than a lower `cos`, and no single-mechanism candidate ever outranks a
corroborated one. Bands are disjoint where they must be ordered and overlap only
where two single-mechanism proposals are genuinely comparable.

---

## 5. Transport

New package `internal/contextfabric/embedprovider`, mirroring `modelprovider`'s
shape and posture exactly.

- Reuses `modelprovider`'s `newClientOptions` pattern: an explicit base URL and
  explicit credential on every construction, so an ambient `OPENAI_*` variable
  can neither redirect traffic nor supply a credential the configuration did not
  choose.
- Reuses the `sanitizeProviderErrorBody` middleware discipline (provider.go:147)
  — a provider response body is never logged or surfaced, at any status class.
- Uses the `openai-go` SDK's `Embeddings` service directly, **not** Genkit's
  `compat_oai`. That wrapper eagerly defines embedders and hard-codes model ids
  (documented at modelprovider/config.go:15-23); the embedding call needs none
  of it. Same SDK, same option plumbing, no new transport code.

New ACR-owned port in `contextfabric/ports.go` — §19.4.7 calls for one; the
"no port change expected" line refers to `GraphReader`, which is unchanged:

```go
type Embedder interface {
    Embed(ctx context.Context, texts []string) ([][]float32, EmbedderIdentity, error)
}
type EmbedderIdentity struct { Provider, Model string; Dimension int }
```

Configuration — the LM Studio endpoint is a **dev config value only**, never a
constant in code:

| Variable | Default | Note |
| --- | --- | --- |
| `ACR_CONTEXT_FABRIC_EMBED_PROVIDER` | — | Recorded verbatim in the identity. |
| `ACR_CONTEXT_FABRIC_EMBED_BASE_URL` | — | Unset ⇒ vector retrieval disabled. |
| `ACR_CONTEXT_FABRIC_EMBED_MODEL` | — | e.g. `text-embedding-nomic-embed-text-v1.5`. |
| `ACR_CONTEXT_FABRIC_EMBED_DIMENSION` | — | Must match the index. |
| `ACR_CONTEXT_FABRIC_EMBED_API_KEY` / `_FILE` | empty | LM Studio needs none; the shape accommodates one for a hosted embedder. |
| `ACR_CONTEXT_FABRIC_EMBED_SIMILARITY_FLOOR` | 0.55 | τ, §4.2. |
| `ACR_CONTEXT_FABRIC_EMBED_TIMEOUT` | 250ms | §6.2. |
| `ACR_CONTEXT_FABRIC_EMBED_ALLOW_INSECURE_BASE_URL` | false | Required for a loopback `http://` endpoint. |

Unset base URL ⇒ the feature is off and the lexical path is unchanged. Same
opt-in posture as answer reuse.

From inside a container the dev value is `http://host.docker.internal:1234/v1/`;
on the host, `http://localhost:1234/v1/`.

---

## 6. Measurement, cost, and the rebuild interaction

### 6.1 AC-3778-2 — the 25pp lift

AC-3778-1 requires the corpus to be **authored after implementation and withheld
from this lane**. So this lane delivers the **harness**, not the corpus:

- A live-tagged benchmark runner that reads a corpus path from the environment,
  runs each question through `ResolveSubjects` twice — vector disabled, then
  enabled — against live dev data, and emits correct-commit / wrong-commit /
  no-match counts plus the delta.
- The runner records the lexical-only baseline first (AC-3778-1 ordering) and
  fails if the wrong-commit rate rises at all (AC-3778-3) or if a control
  no-match question resolves (AC-3778-4).
- This lane authors only a small non-authoritative smoke corpus, for wiring.
  The held-out 50 comes from the orchestrator or Chris.

### 6.2 AC-3778-5 — 150 ms p95

Measured budget for the retrieval stage, from the probes:

| Step | Measured |
| --- | --- |
| Embed the question (warm, 1 input) | 10–17 ms |
| `db.idx.vector.queryNodes` | < 1 ms |
| Normalize + merge | negligible |

Headroom is large. The real risk is a **cold embedder: 9.3 s**. Mitigation: a
bounded per-request embed timeout (default 250 ms) and **fail-open to
lexical-only** — a vector step that times out or errors degrades the request to
the existing lexical path and marks coverage partial. It never blocks or slows
the answer past the budget. That is the honest reading of "adds at most 150 ms".

Projection-side cost is one batched embed call per projection batch, which does
not raise projection lag beyond its current service level — measured and
reported as implementation evidence.

### 6.3 AC-3778-7 — rebuild and reuse

Embeddings are **projection artifacts**, so the existing epoch/invalidation
machinery already covers them. Stating it explicitly:

- Embedder identity and dimension are written onto each node and into the
  projection receipt.
- Changing either is detected at bootstrap by comparing the configured identity
  against what `db.indexes()` reports and what nodes carry. On mismatch the
  adapter **refuses to query the vector index** and degrades to lexical-only
  until a rebuild runs. A stale-dimension vector is never queried.
- The operator-prescribed recovery is the existing
  `acr-projector rebuild --org` path. It already resets every source checkpoint
  and bumps the rebuild epoch, which invalidates answer reuse (CHAOS-3782,
  condition 3 and 4). No new invalidation concept is introduced.
- FalkorDB's own hard dimension-mismatch error is a free second fail-closed
  layer underneath the adapter check.

---

## 7. Out of scope

- Relationship/edge vector index (`db.idx.vector.queryRelationships`). Subjects
  only this pass.
- Any model or embedder in the write path of an edge (§19.4.4).
- Retrieval-augmented generation from raw text. Approved source text stays
  untrusted data.
- Exposing a vector index, a distance value, or a native query to any consumer.
- Cross-organization embedding, a shared index, or a shared vector namespace.
- Changing the lexical band [0.50, 0.75] or the shipped commit thresholds
  (0.72 / 0.88 / gap 0.12). Both stay exactly as merged.
- Similarity as an authorization mechanism. Authorization stays ahead of ranking.
- Choosing the production embedder. `gpt-5-nano` fallback is a later decision.
- Routes, MCP surface, and answer projection (lane-3746 owns those).
