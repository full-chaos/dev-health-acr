# ADR 0008: Use Genkit Go as the bounded Context Fabric model runtime

- Status: Accepted
- Date: 2026-08-11
- Decision owners: Context Fabric / ACR
- Implements: CHAOS-3756

## Context

Context Fabric must interpret unrestricted natural-language engineering questions and synthesize direct answers from authorized graph context and canonical Dev Health facts. It must not return to exact-question routing, a finite intent registry, or an autonomous model that can invent facts or directly query infrastructure.

ACR already owns the investigation state machine, authorization, graph access, canonical fact planning, evidence closure, persistence, API, and MCP boundary. The model SDK therefore needs to provide bounded structured generation rather than become a second orchestration framework.

## Decision

ACR will use Genkit Go:

```text
github.com/firebase/genkit/go v1.11.0
```

Genkit is confined behind the ACR-owned `ModelRuntime` interface and performs exactly two versioned operations:

1. structured question interpretation;
2. structured answer and driver synthesis.

The ACR engine remains the orchestration authority.

## Interpretation boundary

The interpretation operation receives a bounded representation of:

- the open-ended question;
- ordered conversation turns;
- requested scope and subject hints;
- prior answer-bound subject receipts;
- time context.

It may propose:

- investigation shape;
- requested judgment;
- subject and comparison terms;
- time interpretation;
- canonical fact families;
- a focused clarification when proceeding would be materially unreliable.

It cannot mint canonical entity IDs, authorization, facts, relationships, evidence, staffing claims, SQL, GraphQL, Cypher, credentials, or caller-defined tools.

## Synthesis boundary

The synthesis operation receives only the ACR-produced investigation state:

- committed subject resolution and cohort;
- bounded relationship and temporal paths;
- graph driver candidates;
- typed canonical facts;
- evidence references;
- source coverage and degradation.

The model returns an internal synthesis draft, not a public result. ACR validates that every subject, path, and evidence reference already exists in the input, then ACR constructs the public `InvestigationResult`.

A model cannot bypass:

- organization and scope authorization;
- canonical fact authority;
- evidence closure;
- source-state disclosure;
- result schema validation;
- serialized-output budgets.

### Value-level evidence closure (CHAOS-3755 amendment)

The evidence closure above, as originally built, is structural only: it
proves a driver or finding cites something real, not that the cited value
agrees with what was actually observed. A synthesis draft claiming
"release-ready" against a canonical `release_ready=false` fact passed every
Reset 0 validator unchanged -- recorded as a must-do from the Reset 0
adversarial review.

CHAOS-3755 closes that gap deterministically: `ContextFabricClaimedFact`
entries restate a canonical fact field verbatim, and
`SynthesisDraft.ValidateAgainst` deep-equals every claim against the actual
`CanonicalFactBundle` the synthesizer received before a result can ever be
built. `DeterministicAnswer` is server-composed from the validated result
rather than model-authored, so it cannot itself reintroduce an unchecked
claim. See [docs/design/context-fabric-result-semantics.md](../design/context-fabric-result-semantics.md)
for the full mechanism and its honest residual limitation: free-text prose
fields (`direct_judgment`, `current_state`, `limitations`) are not
provably closed by code -- that is an entailment problem, not a
deterministic one, and remains open for a future evaluator pass (below)
rather than a synchronous request-path check.

## Provider and tool policy

- Provider and model selection are server-owned configuration.
- Genkit plugins may provide model access, but public Context Fabric contracts remain provider-neutral.
- No unrestricted tools are exposed to the model in Reset 0 or Reset 1.
- Any future tool must be an ACR-owned typed capability with independent authorization and validation.
- No autonomous multi-agent loop is required or authorized for the investigation core.

## Operational configuration

`genkitruntime.Config` is the whole server-owned configuration surface; nothing in the wire contract lets a caller influence it:

- `Provider`, `Model` — required, non-empty, ≤256 bytes.
- `ModelVersion` — defaults to `Model` when unset.
- `InterpretationPromptVersion`, `SynthesisPromptVersion`, `SchemaVersion`, `EvaluatorVersion` — default to the `context-fabric-*` constants in `genkitruntime`. All four were `.v1` at Reset 0; `InterpretationPromptVersion` moved to `.v2` in CHAOS-3754 when the interpretation system prompt gained conversational-reference resolution, alias/acronym/previous-name subject-term guidance, and subjectless team/project cohort framing. `SynthesisPromptVersion`, `SchemaVersion`, and `EvaluatorVersion` remain `.v1`.
- `Timeout` — per-attempt deadline, must be in `[1s, 2m]`; defaults to 45s.
- `MaxAttempts` — must be in `[1, 3]`; defaults to 2.
- `MaxInputBytes` — bounded-input budget for the encoded interpretation/synthesis payload, must be in `[8KiB, 1MiB]`; defaults to 512KiB.
- `Fallback` — optional deterministic `ModelRuntime`; see Reliability and fallback.
- `MaxSynthesisResynthesisAttempts` (CHAOS-5655) — bounds `SynthesizeAnswer`'s own re-sampling of a validator-rejected draft; must be in `[1, 5]`; a zero value defaults to `1` (no re-sampling, the pre-CHAOS-5655 behavior) rather than the operator-facing default of `3`, which hosted composition applies explicitly. See "Bounded re-synthesis on invalid output" below.

`New()`/`newWithGenerator()` reject construction outright when any bound is violated; there is no partially-valid `Runtime`.

### Provider portability

Swapping `Provider`/`Model` to a different genkit-registered model requires no ACR code change, but the replacement model must declare `ai.ModelSupports{SystemRole: true, Multiturn: true}`. `sdkGenerator.Interpret`/`Synthesize` always send a system message plus a user message (two messages); genkit's `model_middleware` rejects a multi-message request from a model that has not declared `Multiturn` support (`INVALID_ARGUMENT`), which `classifyModelError` surfaces as `ErrModelOutput`. This bit the fixture in `TestSDKGeneratorUsesGenkitStructuredOutputAndUsage` before the fake model declared `Multiturn: true` and is the first thing to check when onboarding a new provider/model.

### Rollback boundary

Reverting from the Genkit runtime to a fully deterministic runtime is a configuration change, not a code change: construct `RuntimeQuestionInterpreter{Runtime: <deterministic ModelRuntime>}` / `RuntimeAnswerSynthesizer{Runtime: <deterministic ModelRuntime>}` directly instead of `genkitruntime.Runtime`, or point `genkitruntime.Config.Fallback` at one so a Genkit outage degrades to it automatically per call. Either path goes through the same `ModelRuntime` interface and the same outer domain validation, so rollback cannot itself become a source of invalid or ungrounded results.

## Reliability and fallback

- Each model call has a bounded deadline and a maximum of three attempts.
- Only cancellation-safe transient failures are retryable. `genkitruntime` prefers the structured status genkit (and well-behaved plugins) attach to a generation error (`core.GenkitError.Status`) over string sniffing; rate-limit and availability statuses are retried, `INVALID_ARGUMENT` and genkit's own schema-mismatch failures are not, per the fail-closed rule below.
- Invalid, truncated, extra-field, schema-incompatible, or ungrounded output fails closed -- but a single rejected sample is not the verdict on synthesis: `SynthesizeAnswer` re-samples the identical prompt up to `MaxSynthesisResynthesisAttempts` times before the fail-closed 422 applies (CHAOS-5655; see "Bounded re-synthesis on invalid output" below). Genkit's inferred output schema sets `additionalProperties: false`, so an extra field is itself a schema-incompatible-output failure, not a silently dropped field.
- A generation failure is classified into one of four ACR sentinels so callers can apply distinct policy: `contextfabric.ErrModelRateLimited` (provider status `RESOURCE_EXHAUSTED`; or, when the error isn't a `core.GenkitError`, the HTTP status extracted from ACR's own sanitized-error token — never from a bare digit scan of the raw error text, which can collide with an unrelated number such as an ephemeral port), `contextfabric.ErrModelOutput` (provider status `INVALID_ARGUMENT`, or genkit's fixed "model failed to generate output matching expected schema" `INTERNAL` message), `contextfabric.ErrModelUnavailable` (`UNAVAILABLE`/`DEADLINE_EXCEEDED`/`ABORTED`/unmapped statuses, and the default when no structured status and no sanitized-status token are present), and `contextfabric.ErrModelCancelled` (CHAOS-5577: a pre-call cancellation on the operation's first attempt only — see "Receipts and observability" below). Only the classification and the provider status name are preserved in the wrapped error; the original error, which may quote provider response fragments, is dropped rather than propagated.
- A deterministic `ModelRuntime` fallback may be configured on `genkitruntime.Runtime` (`Config.Fallback`). `genkitruntime.Runtime` itself does not re-validate fallback output before returning it — the domain validation and evidence-closure guarantee comes from the outer `RuntimeQuestionInterpreter`/`RuntimeAnswerSynthesizer` adapters in `internal/contextfabric/model_runtime.go`, which call `InterpretedQuestion.Validate()` / `SynthesisDraft.ValidateAgainst()` unconditionally on whatever any configured `ModelRuntime` returns (primary or fallback). Callers must wire `genkitruntime.Runtime` through those adapters, not consume it directly as a `ports.QuestionInterpreter`/`AnswerSynthesizer`; their differing method signatures (receipt-returning vs. not) make wiring `genkitruntime.Runtime` in bare unrepresentable without an adapter, so this is enforced by the type system, not just convention.
- Model unavailability never authorizes a fabricated answer.

## Receipts and observability

Every operation records a content-safe receipt containing:

- operation, provider, model, and model version;
- prompt, schema, and evaluator versions;
- start and completion time;
- attempts and token usage;
- SHA-256 input and output digests;
- outcome: `pending_validation` (generation succeeded, domain validation not yet applied by the caller), `success`, `fallback`, `invalid_output`, `rate_limited`, `unavailable`, or `cancelled`. The rate-limited/invalid-output/unavailable/cancelled members mirror the error classification above one-for-one, so a receipt reader and a Go caller agree on what happened without re-deriving it from the error string.

Receipts never contain raw prompts, questions, credentials, unrestricted source bodies, or model chain-of-thought.

`ModelExecutionReceipt` itself still describes exactly one leg's one reported attempt (the durable row is unchanged by CHAOS-5655); the per-draw sequence a bounded re-synthesis loop produces is Info-level decision-log telemetry only (`draws_total`, `draws_retried`, `draw_outcomes`, `draw_output_digests`, `draw_rejected_clauses` on the `synthesize` operation's own decision line) -- see "Bounded re-synthesis on invalid output" below.

### `cancelled` outcome for pre-call context cancellation (2026-09-11, CHAOS-5577)

CHAOS-5380 (2026-09-10) first gave a context cancellation its own `cancelled` class, but scoped to the *per-attempt* `attempt_outcomes` decision-log field only (`genkitruntime.attemptOutcomeClass`) -- the terminal `receipt.Outcome` documented above was deliberately left unchanged, both moments still folding into `unavailable`, because widening the ADR-governed terminal vocabulary was ruled a contract-token decision outside that lane's authority.

CHAOS-5577 (chris, dictation 969, "Recommends accepted", D2 = A) resolves that open item for the terminal field too, but narrower than the per-attempt class: it distinguishes two different moments a call can stop, where the per-attempt class does not --

- **pre-call, first attempt**: the caller's own context was already canceled or past its deadline *before* `withRetry` placed the operation's FIRST attempt -- the provider never saw a request for this operation at all;
- **in-call, or pre-call on a later retry**: either the call was actually in flight -- already sent to the provider -- when its context was canceled or the per-attempt timeout elapsed, OR a prior attempt in the same operation already contacted the provider (and got a retryable failure) before the caller's context stopped a later retry from ever being placed. Either way, the provider WAS contacted for this operation, so `unavailable` -- not `cancelled` -- is the accurate terminal read (round 2 finding, P1: a pre-call cancellation on attempt 2+ was originally tagged `cancelled` too, contradicting this distinction, since attempt N>1 is reachable only after an earlier attempt genuinely reached the provider).

Only a pre-call cancellation on the operation's FIRST attempt now reports the terminal outcome `cancelled`. `withRetry` is the one place that can tell these apart (its own `ctx.Err()` check, in `internal/contextfabric/genkitruntime/runtime.go`, runs strictly before each attempt's call is placed, and only tags the sentinel when that check fires on attempt 1), so it tags a first-attempt pre-call cancellation with the new `contextfabric.ErrModelCancelled` sentinel; `classifyModelError`/`receiptOutcomeForError` map that sentinel to `cancelled`. Every other case -- in-call, or pre-call after attempt 1 -- keeps producing a bare `context.Canceled`/`context.DeadlineExceeded` (or a genkit-classified error), which still maps to `unavailable`, exactly as before this change. The per-attempt `attempt_outcomes` class from CHAOS-5380 is unaffected either way -- it still reads `cancelled` for any attempt whose own ctx.Err() fired, regardless of attempt number, coarser than the terminal field by design.

**`cancelled` is an operation-wide question, not a per-leg one (round 3 finding, P1, and chris's D15 = A ruling).** `InterpretQuestion` and `SynthesizeAnswer` each have a `Fallback` leg (`internal/contextfabric/genkitruntime/runtime.go`, four call sites: two per operation, one on a generation failure and one on a domain-validation failure), and when the primary fails, the existing "both legs failed -> report the fallback's own outcome" composition rule (`receipt.Outcome = fallbackReceipt.Outcome`) lets the FALLBACK leg's own classification become the operation's terminal read. This is correct for every failure class except one: a primary that genuinely contacted a provider and was then canceled *in flight* correctly reads `unavailable` for its own leg (the paragraph above), but a configured fallback receives the SAME already-canceled context, sees it on its OWN first attempt, and correctly tags ITS OWN leg `cancelled` too -- and the composition rule then let that leg's tag overwrite an operation where a provider genuinely was reached, misreporting the whole operation as `cancelled`. The per-leg `attempt==1` rule above answers "did a PRIOR ATTEMPT IN THIS LEG contact a provider" -- it cannot answer "did ANY leg of this operation" ever contact one, which is what `cancelled` actually means at the operation level.

The fix (`fallbackWouldSeeADeadContext`, `internal/contextfabric/genkitruntime/runtime.go`) closes that gap by never placing the doomed fallback call in the first place: at all four fallback call sites, when the caller's context is already done AND the primary's own already-computed classification shows a provider was genuinely contacted (`receipt.Outcome != "cancelled"`), the fallback is skipped entirely and the call falls through to the exact code path it already runs when no fallback is configured at all -- the primary's own truthful classification (`unavailable` or `invalid_output`) stands as the operation's terminal outcome, and no fallback receipt is fabricated for a call that was never placed. A primary that itself never contacted a provider (its own outcome already `cancelled`) still invokes the fallback exactly as before this fix: nothing in the operation has reached a provider yet, so letting the fallback's own attempt decide the outcome -- including composing a genuine operation-wide `cancelled` when the fallback also never reaches a provider -- remains correct and unchanged. Both receipts stay truthful either way: the primary's own `attempt_outcomes`/classification are always recorded before this check runs, independent of whether the fallback is subsequently invoked.

This is telemetry-only: no wire contract, schema, OpenAPI, or MCP surface is touched -- `ModelExecutionReceipt.Outcome` stays a plain `string` field, and this section only documents which literal values a well-behaved writer emits.

### Bounded re-synthesis on invalid output (2026-09-12, CHAOS-5655)

The fail-closed rule above ("invalid ... output fails closed") was, before this ticket, a same-attempt rule: `SynthesizeAnswer` sent the synthesis prompt once, and a single `ValidateAgainst` rejection went straight to the fallback leg (if configured) and then the 422. CHAOS-5650's own measurement (`internal/contextfabric/model_runtime.go`'s recorded evidence) established that a rejected synthesis draft is usually model-output *nondeterminism*, not a deterministic gap: an identical client request produced heterogeneous rejection reasons across independent whole-pipeline re-draws, and the corpus record's retried turns cleared on resample 9 times out of 9. Failing an entire investigation closed on ONE such sample was treating a single unlucky draw as though it were the model's considered answer.

This amendment adds a bounded, same-prompt re-sampling step ahead of the existing fail-closed behavior, without changing what "fails closed" means:

- **Mechanism.** On a validator rejection (`draft.ValidateAgainst` or `toDomain`'s own required-field check), `SynthesizeAnswer` re-sends the byte-identical encoded synthesis prompt (no prompt-assembly change, no new input) up to `Config.MaxSynthesisResynthesisAttempts` times TOTAL -- the rejecting draw counts toward the bound. The loop stops the moment a draw validates or a generation call fails in transport (a transport failure is not a rejection to re-sample; `withRetry`'s own `MaxAttempts` already owns that axis).
- **Ordering (chris/team-lead ruling).** The PRIMARY model re-samples first, exhausting the whole bound, before the fallback leg (if `Config.Fallback` is set) is ever tried -- and the fallback itself still gets exactly one attempt, never its own re-sampling loop. `runtimeConfigWithPhrasing` (the primary runtime's own builder in `internal/contextfabric/modelprovider`) is the only path that wires `MaxSynthesisResynthesisAttempts` onto a `genkitruntime.Config`; the fallback runtime is built through the plain `runtimeConfig`, which leaves the field at its zero value, and `newWithGenerator` defaults zero to `1`.
- **The invariant, restated.** "Invalid output fails closed" is unchanged: a rejected draft is *never* served, at any point in the loop. What changes is that a single rejected sample is no longer treated as the final word on whether the model CAN answer this question -- one model sample is not the verdict.
- **Reuse-key trap, avoided on purpose.** A resynthesis attempt is NOT a new prompt version and does not touch `SynthesisPromptVersion` or `contextfabric.ReuseKey` -- it is the same versioned prompt, re-sent, exactly as `withRetry`'s existing transport retries already are for a transport failure. Widening the prompt itself, or feeding a rejection's reason back into a later attempt's prompt, is explicitly out of scope for this ticket (no prompt change, no new claim semantics).
- **Configuration.** `MaxSynthesisResynthesisAttempts` (env `ACR_CONTEXT_FABRIC_SYNTHESIS_MAX_RESYNTHESIS_ATTEMPTS`, hosted composition in `internal/runtime/hosted`) defaults to `3` and is inherited by a per-organization BYO runtime the same way `Timeout`/`MaxAttempts`/`MaxTransportRetries` already are (§19.3.2: a deployment-surface tuning knob, not part of the per-organization contract). Unlike every `ACR_CONTEXT_FABRIC_MODEL_*` variable `modelprovider.ConfigFromEnv` owns (which fails composition outright on a malformed value), a malformed or out-of-range value for this knob logs a startup WARN naming the bad value and falls back to the default rather than blocking the whole model runtime from starting -- this is a re-sampling POLICY knob, not a provider-identity or credential value, and mis-setting it should never be able to take synthesis down entirely. The WARN fires once per composition path that resolves the value (the deployment default, and again for per-organization defaults when a per-organization store is configured) -- at most two identical lines naming the same variable and value, never a silent miss and never two different diagnoses.
- **Observability.** Each draw's `OutputDigest` (CHAOS-5650's own SHA-256 stamp, extended across every draw rather than only the reported one) and, where the rejection delegates to a model-minted struct's own `Validate()`, its `rejected_clause` are recorded per draw on the `synthesize` decision line (`draws_total`, `draws_retried`, `draw_outcomes`, `draw_output_digests`, `draw_rejected_clauses` -- see "Receipts and observability" above). Distinct digests across rejected draws are the mechanism that distinguishes genuine re-sampling from a server defect returning the same bad draft every time; the durable `ModelExecutionReceipt` still reports only the one leg the call ultimately serves or fails on, matching every other field on that type.

## Evaluator seam

Reset 0D defines, but does not wire, the evaluation extension point: `ModelExecutionReceipt.EvaluatorVersion` tags every receipt with the evaluation method/schema version in force, and `ModelReceiptSink.RecordModelExecution` is where every receipt (success, fallback, invalid-output, rate-limited, unavailable, or cancelled) durably lands. An evaluator consumes `EvaluatorVersion`-keyed receipts from that sink asynchronously and out of the synchronous investigation path — it does not call back into the model, sit in the request/response loop, or gain any authority the model itself lacks. This mirrors the repository's existing evaluation convention (`docs/evaluation-demo.md`, `internal/evalfixture`): deterministic, offline, fixture-driven measurement, not a live in-band hook. Reset 1+ may add a concrete async consumer; doing so does not require changing this seam.

## Evaluation

Evaluation measures open-ended interpretation and fact-grounded answer quality with:

- held-out and paraphrased questions;
- ambiguous and conversational references;
- compound and novel fact combinations;
- subjectless cohorts;
- negative controls and no-match cases;
- invented subject/path/evidence rejection;
- source degradation and uncertainty disclosure.

Representative default questions are smoke tests, not a supported-question allowlist.

## Alternatives

- Google ADK Go remains available for a later explicitly multi-agent or A2A use case. It is not needed for the ACR-owned Reset 0/1 state machine.
- Charmbracelet Fantasy is not selected as the permanent runtime because the required structured generation, evaluation, and production observability are better aligned with Genkit.
- Direct provider SDKs may be used inside a Genkit plugin, but may not bypass `ModelRuntime`.

## Consequences

- Questions remain open; schemas constrain output rather than user language.
- ACR retains deterministic control over data access and public assertions.
- Provider changes do not alter graph, fact, API, MCP, or Workbench contracts.
- Model receipts support replay and quality analysis without logging sensitive content.
