# Spec: fresh incumbent capture runner (plan I6)

Status: **revision 5, the implementation spec** (lane-capture, 2026-09-30; H3 and the prompt-variant control added 2026-10-01, section R7). Section R4 at the end records the gpt-6.1-sol code-review r1 fixes and overrides any earlier text it contradicts.
- Revision 2 applied astra design review r1 (`private/reviews/astra-capture-r1/`).
- Revision 3 applies astra round 2 (`private/reviews/astra-capture-r2/REPORT.md`: REWORK, 4 High provisional and 2 Medium) verbatim, as the coordinator directed. There is no round 3.
- **Timeouts:** the deployment-of-record's effective values are pinned in a deployment profile, with no automatic default (§4).

**Scope.** The runner captures the incumbent (`gpt-5.6-luna` behind the production genkit runtime) on one sealed held-out set: 3 production invocations per row.
- It writes write-once private artifacts, then derives INTERFACES §7 incumbent rows from them.
- Each request is compared with a complete expected descriptor **before** it is sent. The evaluator recomputes both descriptors from the saved bytes.

**Approval.** chris approved live incumbent calls for held-out rows (PLAN §8 item 2). The budget is **720 HTTP attempts in total** for approval `plan-2026-09-30-item2`, across H1, H2 and every run, unless chris raises it.

**Non-goals.**
- No live call during implementation.
- No train or valid capture.
- No indexed samples.
- No scoring in Go.

## 1. Prior art

The old captures came from one Go control command:
- `intent-hybrid-benefit/.../control/`, and the same code at `intent-decision-eval/.../adapters/currentgenkit/`.

Kept:
- the production constructor (`modelprovider.ConfigFromEnv` plus `NewGenkitRuntime`);
- the allowlist;
- a recording `http.DefaultTransport`;
- a flock'd, fsync'd ledger that reserves before it sends;
- 0700/0600 files.

Changed:
- the measured call (§4);
- the request proof (§6);
- the ledger scope and pair identity (§7);
- the publication primitive (§8).

## 2. Location, build and build identity

- **Code.**
  - `experiments/intent-model-training/gocapture/` (package `main`).
  - The shared decoder is `experiments/intent-model-training/internal/interpreq`: strict RequestJSON decoding, the helper `request_sha256`, and the payload render. It is extracted from `gohelper/request.go` and `gohelper/ordered.go`.
  - `gohelper` is unchanged until chris's review session ends; then it moves onto `interpreq`.
- **Build.** Only `gocapture/build.sh` builds the binary. It writes `bin/gocapture` and `bin/gocapture.build.json` with:
  - `schema: "gocapture.build.v1"`, `binary_sha256`, `go_version`, `built_at`;
  - `source_pin` (`git rev-parse HEAD`);
  - `production_tree_clean`: `git status --porcelain -- internal cmd go.mod go.sum` is empty, and `git diff --quiet HEAD -- internal cmd go.mod go.sum` succeeds;
  - `go_sum_sha256`;
  - `experiment_source_sha256`: over the sorted files of `gocapture/` and `internal/interpreq/`, path plus bytes;
  - `vcs_modified_paths`: all must be under `experiments/`.
- **Start check** (any failure exits 2 before any file write or connection):
  - `sha256(os.Executable())` equals `binary_sha256`;
  - the manifest `source_pin` equals `--source-pin` equals `debug.ReadBuildInfo()` `vcs.revision`;
  - the production-tree check is re-run.
- **Trust model:** procedural, the same as review spec R4.1.

## 3. Runtime construction and allowlist

- **Construction.** `modelprovider.ConfigFromEnv(os.LookupEnv)` supplies identity and credential. The deployment profile (§4) supplies all tuning. Then `NewGenkitRuntime`. Nothing else constructs a client.
- **Allowlist** (hard-coded; no flag or env changes it):
  - provider `openai`;
  - the **resolved** base URL (empty means `DefaultBaseURL`) equals `modelprovider.DefaultBaseURL`;
  - model `gpt-5.6-luna`;
  - fallback **unset**; phrasing unset;
  - `AllowInsecureBaseURL` false.
  - The profile must state the same values (§4).
- **Transport check, per request and before send:**
  - scheme `https`, host `api.openai.com`, path `/v1/chat/completions`, method POST;
  - redirects are refused (`CheckRedirect` returns an error), and a 3xx response is a stop.
- **Tests.** Tests reach a loopback allowlist only through an unexported `runConfig` field.

## 4. What is measured, and the deployment profile

- **Replicates.** For each sealed row: replicates r ∈ {0, 1, 2}. Each is one call of production `Runtime.InterpretQuestion(ctx, principal, request)`. The call starts at sample 0 and may redraw on a validator rejection, bounded by the profile's resynthesis limit and deadline (`runtime.go:928`, `:1075–1146`).
  - All replicates send the sample-0 seed, so their spread is provider non-determinism, which is what production serves.
  - Indexed samples are not captured.
- **Deployment profile.** `--deployment-profile <file>` is **required**, and there is **no default**: the runner refuses to capture until the file exists. chris supplies it; tests use a fixture profile. Schema `gocapture.deployment-profile.v1`, with **no credential value**:
  - identity: `deployment_id`, `deployed_source_revision`, `image_digest`, `config_snapshot_at` (RFC 3339);
  - `source_revision_relation`: `"equal_to_pin"`, or `"interpreter_tree_equal"` with `relation_evidence` (a citation). Anything else is refused.
  - tuning: `acr_request_timeout`, `model_timeout` (Go durations), `model_max_attempts`, `model_max_transport_retries`, `synthesis_max_resynthesis_attempts` (the total draw limit, initial draw included);
  - identity values: `provider`, `base_url_resolved`, `model`, `fallback_model`, `allow_insecure_base_url`, `phrasing_model`;
  - `org_model_resolution`: `{enabled, capture_org_override, config_generation, provider, base_url, model, fallback}`. With an override, its identity must equal the allowlist; tuning is inherited, as `org_model_config.go:170–191` resolves it. Note that an org-only deployment resolves tuning to 45 s / 2 / 2.
  - `credential_source_confirmed: true`;
  - `sources`: a citation per value.
- **Profile validation:**
  - Every field is present; durations and integers are in range. Attempts are 1–3 (`runtime.go:904`), transport retries are 0–10, and resynthesis is 1 to `MaxSynthesisResynthesisAttemptsCeiling`.
  - A malformed value is refused, never defaulted.
  - An env tuning variable that is set and differs from the profile: exit 2.
  - Identity values must equal the allowlist.
- **Deadline policy.** Each invocation runs under `context.WithTimeout(background, acr_request_timeout)`. This is the full request deadline of `api/app.go:224`. Interpretation is the first model step, so it is a slight upper bound on production's remaining interpretation budget (disclosed). With `acr_request_timeout < model_timeout`, the redraw gate (`runtime.go:1136–1142`) allows no validator redraw; SDK and runtime retries still happen inside the deadline. The capture reproduces whatever the profile implies.
- **`profile_sha256`** is the sha256 of the profile file bytes. It is recorded in `run.json` and in every artifact.

## 5. Input, seal, session and exposure (Python interface; the sealing lane owns the Python)

Paths are under `$IT = ~/projects/full-chaos/dev-health/intent-training`.

**Canonical JSON (CJ)**, used by every digest in this section:
- UTF-8, object keys sorted by code point, separators `,` and `:` with no spaces, integers as exact decimal, no floats.
- Strings are escaped as Python `json.dumps(sort_keys=True, separators=(",",":"), ensure_ascii=True)` escapes them: non-ASCII as lowercase `\uxxxx`, astral code points as surrogate pairs, and `\b \f \n \r \t \" \\` short escapes. Invalid UTF-8 is refused. Go (`gocapture/cj.go`) and Python are then byte-identical.
- `request` objects are never CJ-hashed (see `request_sha256`).

**Hash meanings** (fixed; both implementations use them):
- `request_sha256`: the **existing helper/`interpreq` value**. It is sha256 of Go `json.Marshal` of the strictly decoded typed `requestJSON` struct (field order and `omitempty` as in `gohelper/request.go:46–83`). Python stores the helper's value (`dataset.py:859`). No sorted-JSON request hash is defined.
- `input_sha256`: sha256 of the rendered user-payload bytes (helper `render`, `interpreq.Render`).
- `membership_digest`: sha256(CJ(`rows`)). `rows` is sorted by `row_id` ascending, and each is exactly `{example_id, input_sha256, request_sha256, row_id}`.
- `seal_digest`: the sealing lane's **full** seal digest, binding the requests, targets, alternatives, equivalents, acceptable fact sets, scoring-policy id and the frozen evaluation-manifest identity (HELDOUT §3 step 9, §7 item 13). The runner treats it as opaque but binds every record to it. The evaluator recomputes it.

**Read: the seal record** in `$IT/data/review/seals.jsonl` (append-only; the sealing lane writes it). The runner uses the one record with `kind: "seal"`, the set and `seal_digest` named by the session. Two seal records with the same `seal_digest` but different content: exit 2. The fields it reads:
```
{kind:"seal", schema:"intent-training.seal.v1", set:"H1"|"H2"|"H3", set_version, seal_digest, membership_digest,
 membership:[{row_id, example_id, request_sha256, input_sha256}],
 expected_system_message_sha256, evaluation_manifest_sha256, sealed_at}
```

**Read and append: `$IT/data/review/heldout-openings.jsonl`** (0600, flock, fsync per line). The session lifecycle:
- `{kind:"evaluation_session", session_id, set, seal_digest, membership_digest, evaluation_manifest_sha256, phase:"H1-decision"|"H2-final"|"H3-round3", authorized_by:"human:chris", allowed_readers:["capture-input","capture","predict","evaluate"], opened_at}`. Only chris opens a session, through the Python CLI.
- Each reader appends `{kind:"exposure", session_id, reader, event:"start"|"resume", run_id, seal_digest, binary_sha256|null, at}` **before** it reads sealed request text (`capture-input`) or sends a request (`capture`). The runner fsyncs its exposure line before its first reservation.
- `{kind:"session_closed", session_id, closed_by:"human:chris"|"evaluate", at}`. `evaluate --final` closes the session when it has scored. A closed session admits no reader.
- **Resume versus reopening.** Resuming a capture run inside the same open session appends `event:"resume"`. A new session for an already-exposed seal is a **reopening**. The runner refuses it unless the session record carries `reopen_reason` (chris), and it records the reopening in `run.json`.
- **Phase gate.** H1 needs `H1-decision`; H2 needs `H2-final`; H3 needs `H3-round3`. The runner computes the phase from the set name (`gocapture/input.go` `phaseFor`) and the Python side holds the same table (`heldout.PHASES`). No header or label is authority.

**Read: `$IT/data/heldout/<set>/capture-input.jsonl`** (0600 file, 0700 directory). `intent-train capture-input` writes it as reader `capture-input`, inside the session.
- Line 1: `{kind:"header", schema:"intent-training.capture-input.v1", set, seal_digest, membership_digest, session_id, expected_system_message_sha256, row_count}`.
- Then, in `row_id` order: `{kind:"row", row_id, example_id, request, request_sha256, input_sha256}`. Any other key is refused. Targets never appear.

**Checks before any write or call:**
1. The file types and modes are right, and no path is a symlink.
2. The session is open, for this set, with the right phase, and `capture` is an allowed reader.
3. The seal record is found for `session.seal_digest`, and `membership_digest` recomputes from `seal.membership`.
4. The header equals the seal on set, `seal_digest`, `membership_digest` and system-message sha, and `session_id` is the session.
5. **Exact membership.** For every row, `interpreq` recomputes `request_sha256` and `input_sha256` from `request`, and both equal the seal row. The input's `row_id` set equals the seal's: none missing, extra or duplicated. `example_id` equals. `membership_digest` recomputed from the input equals the seal's.

**Prerequisite.** The runner is not run until the sealing lane ships named-set loading, the seal and the openings ledger (HELDOUT §7).

## 6. The request descriptor and the wire proof

**Observed descriptor**, built from one request body (decoded with `UseNumber`):
```
{"messages":[ per message, in wire order:
     {"content_kind":"string"|"parts",
      "content_sha256":<sha256 of the text: the string, or the parts' text joined with no separator>,
      "keys":[sorted member names of the message object],
      "parts":[per part {"keys":[sorted member names], "type":<part.type>}] or null,
      "role":<role>} ],
 "seed":"<exact decimal string of the seed value, or null>",
 "top":{<every top-level key except messages and seed>: <its value, JSON with numbers as literal text>}}
```
- A message member other than `role` and `content` must be listed in the golden file; its value is kept in `keys` only, and the golden decides whether it may appear.
- `descriptor_sha256` = sha256(CJ(descriptor)). A non-ASCII string in `top` is refused.

**Expected descriptor**, derived independently per row and draw index d:
- `messages`: exactly 2, in order `system` then `user`. `content_sha256` is the seal's `expected_system_message_sha256` and the row's `input_sha256`. `content_kind`, `keys` and `parts` are copied from the golden file.
- `seed`: `FNV-1a-64("<contextfabric.QuestionHash(question)>:<d>")` with bit 63 cleared (`runtime.go:649–654`), as a decimal string.
- `top`: exactly the key set and literal values in `gocapture/testdata/expected_envelope.golden.json`. `model` equals the profile model. `response_format` is `{"type":"json_object"}`. `temperature` is absent.

**Golden file.** Test T16 generates `expected_envelope.golden.json` by running the production runtime over loopback at the pin, and pins it. It holds `top` without the model value, and per message its `content_kind`, `keys` and `parts`.

**How d advances.** The runner installs its own `slog.Handler` as the runtime logger. It keeps only messages from a closed list and never keeps content.
- The production rejection event (`"context fabric interpret draw rejected"`, fields `sample` and `attempt`, `runtime.go:2774–2794`) is emitted before the redraw is sent. When it is seen with `sample = s`, the handler **authorizes** d = s + 1.
- d starts at 0. **A changed seed never authorizes itself.** A request whose seed matches a draw that has not been authorized is a mismatch.
- The artifact records each authorization as `{after_attempt_seq, authorized_draw, rejection_reason}`.
- Retries are not classified. Only the authoritative global `attempt_seq`, the per-invocation `attempt_index` and d are recorded.

**Checked before send, with a latch.** For each attempt, the transport:
1. re-checks the allowlist;
2. builds the observed descriptor and compares it with the expected descriptor for the current authorized d;
3. records both descriptors and their hashes.
- On any difference it does **not** send. It sets the per-invocation latch and returns an error. Every later attempt in the invocation is refused unsent.
- The pair ends `contract_failure`, and the run stops.

**Credential scan (Medium 6).** Every response body (2xx and non-2xx) is scanned **transiently** before anything is persisted. The scan looks for the configured key in its raw, std-base64, url-base64, URL-query-escaped and JSON-escaped forms. On a match:
- set the latch, and the run stops;
- the pair ends `credential_echo`;
- **every content-derived output field is suppressed**, `raw_text` included;
- only the hashes, lengths, status and the classification are persisted.

**Persisted per attempt:**
- `attempt_seq`, `attempt_index`, `draw`;
- the request body (private, base64), its sha256, and both descriptors with their hashes;
- status, `response_sha256`, `response_bytes`, `system_fingerprint`, `sent_at`, `duration_ms`;
- the response body (private, base64) **only** if it is 2xx and the scan is clean. A non-2xx body is never persisted.

**No content logging.** Only closed-list fields; fixed stderr messages; no request headers.

## 7. Outcomes, pair identity, budget and resume

**Logical pair key:** `(approval_id, seal_digest, row_id, replicate)`. `run_id` is provenance.
- Within an approval, a set's capture keeps its **original `run_id`**: continuing or recovering uses `run --resume <run_id>`.
- A new `run_id` cannot capture any pair that already has a reservation, terminal or uncertain record.

**Outcome classes.** The runner decides its own conditions first, from its own latch and state, and never from the runtime's error classification:
- **Runner-owned, capture incomplete:** `contract_failure`, `credential_echo`, `budget_exhausted`, `write_failure`, `unattempted`, `uncertain`. These are never model failures.
- **Content-bearing** (`production_returned: true`): `InterpretQuestion` returned with no error.
  - `content_attempt_seq` is the last 2xx attempt of the final draw.
  - Proof: the response body bytes, sha and `choices[0].message.content` match `raw_text`.
  - Replay decides semantic correctness.
- **Genuine model failure** (`production_returned: false`): the runtime returned an error that no runner-owned condition explains: `invalid_output` (the final draw was validator-rejected), `rate_limited`, `model_unavailable`, `deadline`, `runtime_error`.
  - Proof: `content_attempt_seq` is null; `raw_text` is null; the complete reservation and attempt trace; and the receipt `Outcome` and error class agree with the classification.
  - **Scored zero.** Replay never promotes an output that production failed to return.
- A 2xx body of an **earlier, rejected** draw stays diagnostic evidence (`diagnostic_attempt_seqs`). It never becomes the terminal output, even when a later draw fails.

**Approval ledger:** `$IT/data/heldout/incumbent-approval-ledger.jsonl` (0600, `flock LOCK_EX|LOCK_NB` for the whole run, fsync per line). Records:
- `approval {approval_id, cap_http_attempts, approved_by:"human:chris", source, at}`. It is written by `gocapture approve` (chris). A raise is a new `approval` line with the **same `approval_id`** and a higher cap; all earlier spending counts.
- `run_started {approval_id, run_id, set, seal_digest, run_config_sha256, run_cap_http_attempts: N, at}`. It is written after `run.json` and before the first reservation.
- `pair_reserved {approval_id, seal_digest, row_id, replicate, run_id, at}`.
- `http_reserved {approval_id, seal_digest, row_id, replicate, run_id, attempt_seq, draw, expected_descriptor_sha256, at}`.
- `pair_terminal {…pair key…, run_id, outcome, production_returned, artifact_sha256, at}`.
- `pair_uncertain {…pair key…, run_id, at}`.
- `recovery_authorized {…pair key…, run_id, authorized_by:"human:chris", reason, at}`.

**Budget.** Before **every** `http_reserved`, the runner checks two counters: the approval total is below the approval cap, **and** the run total is below the run cap N. The check happens under the ledger lock.
- N is durably bound in `run_started` and in the immutable run config.
- On resume, the run's cumulative reservations count; the allowance never resets.
- A request over either cap is not sent: the pair ends `budget_exhausted`, and the capture is incomplete.
- Reservations stay charged after a crash, including ones whose request never reached the server. So server count equals reservations only in fault-free tests.

**Write order per pair:**
1. `pair_reserved` (fsync).
2. Per attempt: `http_reserved` (fsync), then send.
3. Publish the canonical artifact (§8).
4. `pair_terminal` (fsync).

**Resume** (same `run_id`, same run config):
- A pair with `pair_terminal` is final.
- A pair with `pair_reserved` and no terminal: if a **verified**, ledger-bound published artifact exists (matching pair key, run id, config sha and self-hash), the runner writes `pair_terminal`. Otherwise it writes `pair_uncertain` and **never resends**.
- A pair never reserved is captured.
- **The first terminal outcome is canonical.** There is no retry flag.

**Recovery.** `gocapture recover --run-id … --pair <row_id>:<replicate> --authorized-by human:chris --reason …` applies only to uncertain pairs.
- It writes `recovery_authorized`, then captures into a **separate immutable recovery artifact** (`…-r<replicate>.recovery-<k>.json`), with the same budget checks. The earlier uncertainty and any artifacts are preserved.
- The derived row is labelled `recovered: true`.

**Immutable run config.** `run.json` is published write-once (§8) before `run_started`. It holds:
- the input, seal and session digests; `profile_sha256`; the build manifest sha;
- the allowlist; `min_interval`; N; the approval id; the source pin.
- `run_config_sha256` = sha256(CJ(config)). A resume with any difference: exit 2.

**Rate and stop rules.**
- One invocation at a time.
- `--min-interval` (default 1 s) between HTTP sends, enforced in the transport.
- The run stops on: budget exhaustion; contract failure; credential echo; an allowlist breach, redirect or 3xx; a write or fsync failure (no call after it); 2 consecutive `model_unavailable` terminals; a ledger lock or format error.

## 8. Output and publication

Everything goes under `$IT/data/heldout/<set>/captures/<run_id>/` (0700 directories, 0600 files).
- Artifact names are `sha256(row_id)` plus the replicate; the raw id is never used. Symlinks, `..` and non-regular files are refused.

**Canonical, write-once** (`run.json`, `raw/<hash>-r<k>.json`, recovery artifacts), published in this order:
1. Write a temp file in the same directory (`O_CREAT|O_EXCL|O_NOFOLLOW`, 0600), then fsync it.
2. **`link(temp, final)`**: atomic and **no-replace**. `EEXIST` means a collision.
3. `unlink(temp)`, then fsync the directory.

A plain `rename` is never used for canonical files. Fresh creation treats a collision as exit 2. Resume admits an existing final file only when it is verified and ledger-bound (§7). Left-over temp files are removed on resume and are never canonical.

**Derived, regenerated** (`responses.jsonl`, `report.json`, `verify.json`): each is rebuilt deterministically from the canonical artifacts and the ledger by `gocapture derive --run-id …`. It is written to a temp file, fsync'd, then `rename`d over the old file, which is the only place replacement is allowed, and then the directory is fsync'd.

**Artifact** `{schema:"gocapture.artifact.v1", …}` holds:
- identity: the pair key, `run_id`, `example_id`, `set`, `source_pin`, `binary_sha256`, `profile_sha256`, `run_config_sha256`;
- `attempts[]` (§6) and `draw_authorizations[]`;
- `latch {tripped, reason}`;
- `outcome`, `production_returned`, `content_attempt_seq`, `diagnostic_attempt_seqs`, `raw_text`;
- `receipt {outcome, attempts, fallback_used, error_class}`, `normalized_sha256`;
- `recovered`, `recovery_of`, `captured_at`;
- `self_sha256`: sha256 of CJ(the artifact without this field).

**`responses.jsonl`** holds one §7 row per canonical pair:
- identity: `candidate:"incumbent"`, `row_id`, `example_id`, `draw:<replicate>`, `remote:true`;
- model: `model_id`, `model_revision`, `adapter_id:null`, `adapter_sha256:null`;
- hashes: `request_sha256:<input_sha256>`, `prompt_sha256:<expected system sha>`;
- contract: `contract_match`, `contract_reason`, `contract_detail {method:"wire", descriptor_shas[]}`;
- output: `decoding {temperature:null, seed}`, `raw_text` (null unless content-bearing), `finish_reason`, `token_counts`, `timings_s.generate`, `error`, `production_returned`;
- `capture {artifact_path, artifact_sha256, outcome, content_attempt_seq, response_sha256, recovered}`.

## 9. The evaluator (Python hook; the sealing lane builds it)

`evaluate --final --split heldout-<set> --incumbent-capture <dir>` enforces the following. Each failure is blocking.
1. **Recompute everything from the saved bytes.** Verify each artifact's `self_sha256` and file sha against the ledger `pair_terminal`. For every attempt:
   - Re-hash the saved request body, and **reconstruct the observed descriptor** from it using the algorithm in §6.
   - **Independently reconstruct the expected descriptor** from the verified input request (`interpreq`-equivalent render hash from the seal membership), the seal's system sha, the profile model, the golden file and the authorized draw index.
   - Compare both the values and the hashes.
   - The shared test vectors `gocapture/testdata/descriptor_vectors.json` (request body → descriptor CJ → sha) must pass in Python.
2. **Proof branch per pair** (§7):
   - A content-bearing pair: the response body, sha and content equal `raw_text`.
   - A genuine model failure: null content, a complete trace, and a consistent receipt. It is scored zero.
   - A runner-owned outcome: the capture is incomplete.
3. **Completeness.** Every sealed `row_id` has exactly replicates {0, 1, 2}, each with one canonical terminal record. Anything else is incomplete, which blocks the decision report; a subset is never averaged. Recovered pairs are reported.
4. **Cross-check** (reported, not blocking). The runtime's receipt and `normalized_sha256` are compared with the evaluator's replay of `raw_text`.
5. **Paired report.** Draws are averaged per row over exactly 3 outcomes. The partial-coverage path (`report.py:248`) is not used for held-out.

## 10. Tests

Tests use a loopback `httptest` server, fixture seals, sessions and profiles, and a fixture approval; no sealed data.
- The server counts requests and can script 2xx, 4xx, 5xx, 3xx, delays and credential echoes.
- Every test is shown red on a planted defect and restored by file hash.

| Test | Covers | Planted defect |
|---|---|---|
| T1 | 2 rows × 3 replicates → artifacts → `derive` → §7 rows; exact grid; a Python evaluator run once the sealing lane ships it | a dropped `draw` or `row_id`; a grid gap |
| T2 | the wire system and user shas equal the descriptor | whole-body hashing |
| T3 | a wrong expected system sha: 0 requests reach the server in that invocation; the run stops | a check after send; a send after the latch |
| T4 | a wrong row `input_sha256`: the same | only the system part checked |
| T5 | allowlist: scheme, host, path, method, model, base URL, fallback, insecure, redirect/3xx | a check only in `main`; redirects followed |
| T6 | missing env, 0 connections; malformed or conflicting key file; missing or malformed profile; env tuning conflict | the runtime built before the checks; a profile default |
| T7 | a key echoed in a 200 body and in a 401 body: latch, stop, `credential_echo`; no content field in the decoded artifacts, logs or stderr | a plaintext grep only; non-2xx not scanned; `raw_text` kept |
| T8 | 429 → 200; repeated 500 → SDK and runtime retries; timeout; a rejected draw → redraw authorized by the event, seed d=1; a rejected draw then a failing draw → the earlier body is diagnostic only | the first attempt taken; the seed self-authorizing; an earlier body promoted |
| T9 | reservations equal the server count (fault-free); budgets omitted, ≤0 or >720 refused; the run cap N enforced mid-run; spending persists across runs; a raise keeps spending; exhaustion → incomplete | a run cap checked only at start; a cap checked after send |
| T10 | seal and session: forged header; altered, duplicate, missing, extra and relabelled rows; a membership digest mismatch; a closed session; the wrong phase; a reopening without a reason | trusting the header |
| T11 | crash injection after `pair_reserved`, after send, after the temp write, after `link`, before `pair_terminal`; resume completes a verified pair, marks others uncertain, never resends; a new `run_id` refused; `recover` writes a separate artifact and charges the budget | resend on resume; a new run id recapturing |
| T12 | modes on every file; pre-existing paths, symlinks, `..`; `link` refuses to replace; derived files regenerate atomically | a `rename` overwrite of a canonical file |
| T13 | build manifest: a binary hash mismatch; a modified production path at the same revision; an allowed experiment file | a revision-only check |
| T14 | the `interpreq` request and input shas equal the helper's `render` on fixture requests (the tests build the helper from `gohelper` in `TestMain`) | a divergent decode |
| T15 | descriptor: a missing, wrong or >2^53 seed; each golden top key changed, removed or added; `temperature`; an extra message member; a parts-versus-string change; a third message; a swapped order | a subset key check |
| T16 | the golden file regenerated from the production runtime over loopback equals the checked-in file | a stale golden |
| T17 | a write failure stops calls; 2 consecutive `model_unavailable` stop; `--min-interval` measured at the server; a concurrent runner refused by the lock; a changed run config on resume refused; the exposure line durable before the first request; `descriptor_vectors.json` regenerates identically | each guard removed |

## 11. Open points

None for design. The Python side depends on the sealing lane's implementation of §5 and §9.

## R4. Code review r1 fixes (gpt-6.1-sol, `private/reviews/sol-capture-r1/REPORT.md`)

Section R4 overrides any earlier text it contradicts.

1. **Credential scanning** (H1).
   - The scanner covers raw bytes and JSON-decoded strings (keys and values), so `\uXXXX` escapes are resolved. It also covers percent-decoded strings, and base64 at every byte alignment in the std and url alphabets, padded and raw, recursing into decoded base64.
   - It runs on every request body, on every response body (any status), and at a **final gate** over every field about to be persisted. The final gate covers the artifact JSON, `raw_text`, and the decoded request and response bodies.
   - On any hit: the pair ends `credential_echo` (runner-owned); every content field is suppressed, request bodies included; the run stops.
   - A 2xx body that is not valid UTF-8 ends the pair `invalid_utf8` (runner-owned), because the evaluator could not recompute it.
2. **Durable state machine** (H2, H3, M7).
   - **Reconcile before any send.** Every reservation left open is settled from durable evidence, and nothing is resent. A pair with a verified, ledger-bound published artifact gets its terminal record; otherwise it is marked uncertain.
   - **Stop state is rebuilt from the ledger before any send.** A run stops on a `run_stopped` record, on any runner-owned terminal, or on 2 consecutive `model_unavailable` terminals in ledger order. A resumed run that is stopped sends nothing and reports the stop as incomplete.
   - **Recovery takes an explicit index.** `recover --recovery-index k` records `recovery_authorized` (k) and then `recovery_reserved` (k), in that order, before the send.
     - A crashed recovery is settled to a terminal, or to `recovery_uncertain`. Index k is then used and is never sent again.
     - Another attempt needs chris to authorize k+1.
     - The ledger refuses an out-of-order index, an authorization while an earlier recovery is unreconciled, and any recovery once a recovery outcome exists.
   - **Pacing is durable.** The boundary is the latest `http_reserved` time in the ledger, which survives a restart.
3. **Pacing** (H4).
   - `--min-interval` is applied **between invocations, before the deadline is armed**. The transport never sleeps, and retries inside an invocation follow production's own timing.
   - If harness overhead inside an invocation reaches 5% of the request deadline and the invocation ends in `deadline`, the pair ends `harness_interference` (runner-owned; the capture is incomplete).
4. **Session admission** (M5).
   - The exposure line is written in one transaction, under the openings flock, after the session is re-validated.
   - Every invocation is admitted under the same flock, and the flock is held until the invocation ends.
   - The Python side must close a session under that same flock. A session closed mid-run stops admission of the next invocation.
5. **Path confinement** (M6).
   - Every path below the resolved data root is walked component by component with `openat(O_NOFOLLOW|O_DIRECTORY)`. Files are opened with `O_NOFOLLOW`, and publication uses `linkat` on directory descriptors.
   - Directories must be 0700; files must be regular and 0600.
   - Every derived name must match `^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$` with no `..`: run ids, set names, artifact names, the approval id.
   - `derive` accepts only a run id that is ledger-bound: it has a `run_started` for the seal, and `run.json` has its config sha.
6. **Validation before any write** (M8).
   - The profile uses production's ranges: `model_timeout` 1 s to 2 min, `model_max_transport_retries` 0 to 5, attempts 1 to 3, resynthesis 1 to 3.
   - The production runtime is constructed, and so validated, before the first write.
7. **Canonical JSON** (M10).
   - DEL (0x7f) is escaped as `\u007f`, as Python `ensure_ascii` does.
   - `cjRaw` refuses invalid UTF-8.
   - A Go test runs `python3` as an oracle for parity on control characters, DEL, astral code points and the artifact self-hash.

## R5. Harness interference (gpt-6.1-sol code review r2)

- **What is measured.** The runner measures its own time inside production's deadlines on every attempt. That covers the pre-send checks (descriptor, credential scan, ledger reservation) and the post-response work. The amount is recorded per attempt (`harness_overhead_ms`) and per invocation (artifact `harness_overhead_ms`). The run report records the largest (`max_harness_overhead_ms`) and the threshold. Provider time and production's own retry backoff are not counted.
- **Material.** The overhead is material when it reaches **5% of the tightest applicable deadline**: min(`model_timeout`, `acr_request_timeout`) / 20. For example, 1 s / 1.2 s gives 50 ms, and 45 s / 15 s gives 750 ms. The threshold is recorded in `run.json` (`interference_threshold_ms`).
- **Rule.** With material overhead, the pair becomes `harness_interference` (runner-owned, not scored, capture incomplete, run stopped) in either of two cases:
  - **Non-ok outcome:** `invalid_output` (a redraw the budget gate refused), `deadline`, `model_unavailable`, `rate_limited` or `runtime_error`.
  - **ok, but only after a failed or timed-out attempt:** the overhead may have caused the retry.

## R6. End-to-end contract alignment with the Python side (2026-09-30)

These changes come from the end-to-end run, `intent-training` `tests/test_capture_e2e.py`:
- **Directories.** The runner creates its own directories 0700. It accepts directories the Python side creates (`data/review`, `data/heldout`, 0755) when they are not writable by group or others.
- **Files.** The runner creates its own files 0600. It reads Python-written files (`seals.jsonl`, `heldout-openings.jsonl`, `capture-input.jsonl`) when they are regular and not writable by group or others.
- **Openings lock.** Admission and exposure take the flock on `data/review/heldout-openings.lock`, which is `heldout.OPENINGS_LOCK`. The Python side takes the same lock to open and close sessions and to record exposures.
- **Loopback test build.** `gocapture/build.sh --loopback` builds `bin/gocapture-loopback` with the `gocapture_loopback` tag (`gocapture/loopback.go`). It is for end-to-end tests only and allows a `127.0.0.1` provider. The production build has no such code.

## R7. Prompt-variant control (2026-10-01)

**What it is.** A second, labelled capture of the same sealed rows with the same model: the production system message followed by a fixed separator and an appendix file. It answers one question: how much of a gap closes when the production model is told rules that its prompt does not carry. It is **never the incumbent**. Its label is `incumbent-variant[<name>]` everywhere.

**Command.** `gocapture run ... --prompt-variant <name> --system-append-file <path>`. Both flags or neither. Without them the run is the incumbent capture, byte for byte as before (the request, `run.json`, the artifacts, the ledger lines and the response rows gain no member; `TestNonVariantRequestAndFilesUnchanged`).
- `<name>`: lower-case letters, digits, `_`, `.`, `-`; at most 64 characters.
- The appendix: a regular 0600 file, valid UTF-8, not empty, no control character other than newline and tab, at most 64 KiB, and not holding the credential.
- Everything else comes from the deployment profile exactly as for the incumbent: model, seed, response format, no temperature, 3 replicates, deadlines and retries.

**The system message.** `<production system message>` + `"\n\n"` + `<appendix file bytes>`. The separator is fixed in the code; its sha256 is recorded.

**How the request is made.** Production builds the request, as for the incumbent. In the transport, per HTTP attempt and before anything is sent:
1. **Base proof.** The request production built is checked against the complete expected descriptor of the **incumbent** (§6), with the sealed `expected_system_message_sha256`. Any difference: refused unsent, latch, `contract_failure`, run stopped.
2. **One change.** The appendix is added to the end of the system message's content string. Every byte outside that one JSON string is unchanged; inside it, the original bytes are kept and the JSON-escaped separator and appendix are added before the closing quote. The caller's request object is not changed; a copy is sent.
3. **Variant proof.** The changed request is checked against the same expected descriptor with one value replaced: the system `content_sha256` is `sha256(production system message + separator + appendix)`. Any difference: refused unsent.
4. Reservation, send and recording as in §6 and §7. The recorded request body, `request_sha256` and both descriptors describe the request that was **sent**. The attempt also records the base proof: `base_request_sha256`, `base_expected_descriptor_sha256`, `base_observed_descriptor_sha256`.

Retries and redraws pass through the same steps, so every attempt carries the appendix exactly once, and a redraw is still authorized only by production's rejection event.

**Approval.** A variant run makes live calls. They spend from the **same approval** and the same cap as the incumbent's.
- The run is refused, before any write or call, unless the ledger holds a `variant_approval {approval_id, variant, appendix_sha256, approved_by:"human:chris", source, at}` record for this name **and** this appendix. chris writes it with `gocapture approve-variant --data-root … --approval-id … --prompt-variant <name> --system-append-file <path> --source … --approved-by human:chris`.
- One name, one appendix: a changed appendix needs a new name and a new approval.

**Ledger.** Every record of a variant run carries `variant: <name>` (`run_started` also `appendix_sha256`). The pair key of a variant is `(approval_id, seal_digest, row_id, replicate, variant)`: a variant's pairs never share a record with the incumbent's, so both can be captured for one seal. A record whose series differs from its run's is refused. Incumbent records have no `variant` member. Binaries built before this revision refuse a ledger that holds a variant record (unknown member).

**Files.**
- `run.json` has a `variant` block: `{name, candidate, appendix_sha256, appendix_bytes, separator_sha256, base_system_message_sha256}`. A resume with or without the flags changes the run configuration and is refused.
- `system-append.md`: the appendix, published write-once in the run directory before `run_started`. A resume checks it.
- Artifacts have the schema `gocapture.variant-artifact.v1` and the same `variant` block plus `system_message_sha256` (the system message that was sent).
- `responses.jsonl` rows have `candidate: "incumbent-variant[<name>]"`, `prompt_sha256` = the sent system message's sha256, and the `variant` block. `report.json` names the variant.
- `derive` rebuilds only the run's own series and refuses an artifact of another series.

**Evaluator (Python).** `evaluate --heldout … --incumbent-capture <dir> --variant-capture <dir>` (the second flag is repeatable; each run's `responses.jsonl` goes in `--responses`).
- The variant proof (`capturecontract.verify_variant_capture`) is the §9 proof against the variant's own ledger records, with the system message the evaluator rebuilds itself: the helper's production system message (its sha256 must be the sealed one), the separator, and the appendix kept in the run directory (its sha256 must be the one `run.json` and chris's `variant_approval` name). It also checks that each sent request's descriptor differs from the recorded base descriptor only in the system-message sha.
- A variant capture given as `--incumbent-capture` is refused, and an incumbent capture given as `--variant-capture` is refused. Each proof also refuses the other kind of artifact, row and ledger record.
- The variant is scored like any candidate, as the series `incumbent-variant[<name>]#<draw>`. `paired_with_incumbent` pairs on the real incumbent only.
- `report --heldout` shows each variant under its own label with two paired differences per stratum: tuned minus variant, and variant minus incumbent, and the share of the tuned-minus-incumbent gap that the variant closes.

## R8. Candidate-prompt mode: a system message in place of production's (2026-10-02)

**What it is.** A second mode of the prompt variant (R7). The file is the WHOLE candidate system message and is sent in place of the production system message. It measures a change of the prompt text itself, where R7 measures text added after the prompt. It is still a labelled control: `incumbent-variant[<name>]`, never the incumbent.

**Command.** `gocapture run ... --prompt-variant <name> --system-message-file <path>`. A name goes with exactly one of `--system-append-file` (R7, append mode) and `--system-message-file` (replace mode); both files together are refused. The file rules are R7's. The file is the complete system message as the helper prints it (`interp-helper system-message`) from a build at the candidate commit: the prompt and the output instruction.

**How the request is made.** The steps are R7's, with one difference in step 2 and step 3:
1. **Base proof.** Unchanged: the request production built is the incumbent's, by the complete expected descriptor.
2. **One change.** The content string of the system message is replaced by the file, JSON-encoded as one string. Every byte outside that one JSON string is unchanged (`replaceSystemMessage`).
3. **Variant proof.** The changed request is checked against the same expected descriptor with one value replaced: the system `content_sha256` is `sha256(file)`.

So the model, the user message, the seed, the response format, the decoding, the deadlines and the retries are production's by proof. A file that equals the production system message is the incumbent and is refused: before any write when its sha256 is the sealed `expected_system_message_sha256`, and again in the transport.

**Approval.** Its own record: `variant_approval {…, variant, appendix_sha256, variant_mode:"replace", …}`, written by chris with `gocapture approve-variant … --prompt-variant <name> --system-message-file <path> …`. One name, one mode, one file: a name approved as an appendix does not approve a replacement, the same file approved in one mode does not approve the other, and a changed file needs a new name. The calls spend from the same approval and cap.

**Ledger and files.**
- `variant_mode: "replace"` is on `variant_approval` and `run_started` only; the ledger refuses the member on any other record. The append mode writes no `variant_mode` member, so its ledger lines, `run.json` and artifacts are byte for byte as in R7 (`TestAppendModeRecordsHaveNoModeMember`). In a replace record, `appendix_sha256` and `appendix_bytes` describe the system message file.
- The `variant` block of `run.json`, of artifacts and of response rows has `mode: "replace"` and no `separator_sha256`.
- `system-message.md`: the file, published write-once in the run directory before `run_started`; a resume checks it. A replace run directory has no `system-append.md`.
- Binaries built before this revision refuse a ledger that holds a `variant_mode` member (unknown member).

**Wire proof (test).** `TestCandidateWireDiffIsTheSystemMessageOnly`: for every request of a replace run, the set of JSON paths that differ from the incumbent's request for the same user message is exactly `messages[0].content`, and the bytes outside that string are equal. `TestJSONDiffPathsSeesEveryChange` shows that the comparison reports a second change.

**Limits.**
- Only the system message moves. A candidate that also changes the output schema or a validator is not measured by this mode.
- The read-back of the changed request (`replaceSystemMessage`, last check) has no test that makes it fail: a correct construction cannot produce the case. The same holds for the append mode's read-back.
- **Evaluator (Python).** `capturecontract.verify_variant_capture` in the intent-training repository reads `mode` from `run.json`. For a replace run it rebuilds the system message from `system-message.md` alone and requires `variant_mode: "replace"` on chris's `variant_approval` and on `run_started`; it refuses a run directory that holds both kept files, a separator on a replace block, an unknown mode, and the production system message under a variant label. A verifier from before that change refuses a replace capture ("the variant run used another separator than this evaluator knows") and scores nothing: executed 2026-10-02 against a loopback capture.
