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

## Provider and tool policy

- Provider and model selection are server-owned configuration.
- Genkit plugins may provide model access, but public Context Fabric contracts remain provider-neutral.
- No unrestricted tools are exposed to the model in Reset 0 or Reset 1.
- Any future tool must be an ACR-owned typed capability with independent authorization and validation.
- No autonomous multi-agent loop is required or authorized for the investigation core.

## Reliability and fallback

- Each model call has a bounded deadline and a maximum of three attempts.
- Only cancellation-safe transient failures are retryable.
- Invalid, truncated, extra-field, schema-incompatible, or ungrounded output fails closed.
- A deterministic `ModelRuntime` fallback may be configured. Fallback output is subject to the same domain validation and evidence closure.
- Model unavailability never authorizes a fabricated answer.

## Receipts and observability

Every operation records a content-safe receipt containing:

- operation, provider, model, and model version;
- prompt, schema, and evaluator versions;
- start and completion time;
- attempts and token usage;
- SHA-256 input and output digests;
- success, fallback, invalid-output, or unavailable outcome.

Receipts never contain raw prompts, questions, credentials, unrestricted source bodies, or model chain-of-thought.

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
