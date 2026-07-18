# Deterministic evaluation demo

`bash scripts/e2e/demo.sh` compares a no-context cold plan with a plan supplied
by a real ACR context packet. It is an offline fixture demonstration: it makes
no live LLM call and does not claim a real-agent outcome.

The command verifies `testdata/evaluation/v1/` with `internal/evalfixture`,
then assembles packets through `contextpacket.EvaluationStore` and
`contextpacket.Assembler`. Each result therefore records real fixture packet
IDs and resolved evidence citations. Repeats use the same serialized request
input and fail if their task output differs.

Default runs retain assembled packet candidates even when they diverge from the
fixture's expected evidence labels, so the metrics expose drift rather than
turning it into a passing result. Only named corruption scenarios invalidate.
Branch-only tasks omit `task_ref`, so the fixture exercises branch resolution
rather than task-reference lookup.

## Metrics

All metrics are deterministic functions of the verified corpus and assembled
packet:

- **Factual-error rate**: unsupported observed packet claims divided by
  observed packet claims.
- **File/test recall**: matched file or test identifiers divided by fixture
  file or test identifier ground truth. The v1 public corpus intentionally has
  no such identifiers, so this metric is reported as `null`, not as a made-up
  score.
- **Citation precision**: packet citations that match expected fixture evidence
  divided by packet citations.
- **Irrelevant-item rate**: packet items outside the task's expected evidence
  divided by packet items.
- **Plan latency**: deterministic logical steps, not wall-clock time. A cold
  plan is one step per task; a context plan adds one inspection step per cited
  packet item.
- **Token cost**: the real ACR packet budget's estimated context tokens.

The cold surface emits no observed claims, citations, or packet items; metrics
without a denominator are explicitly `null`. This avoids treating absence of
context as an unsupported performance claim.

## Cross-surface sample

`contracts/examples/v1/evaluation_demo.v1.json` is the canonical metrics
object for the companion web demo. This repository does not contain the web
checkout, so it cannot render that surface here. Instead,
`TestRun_matches_committed_cross_surface_sample` byte-compares the CLI's metric
JSON with the committed sample, while `evaluation_demo.v1.schema.json` makes
`contractcheck` reject schema drift and unknown fields. The web demo must render
this object verbatim; changing either surface requires updating the canonical
sample, schema, and parity test together.

## Commands

```bash
bash scripts/e2e/demo.sh --repeat 2 --out .omo/evidence/task-22-acr-project-completion.json
bash scripts/e2e/demo.sh --scenario corrupt-hash
bash scripts/e2e/demo.sh --scenario empty-evidence
bash scripts/e2e/demo.sh --scenario mismatched-task
```

The default command exits zero. Each named failure scenario exits one, emits an
`invalidated` result, and omits metrics.
