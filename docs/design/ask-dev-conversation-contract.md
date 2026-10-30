# Ask Dev conversation contract (threads)

Status: the contract (plan item I-28). The store and the routes come in the
next item (I-29); the run lease, attach and cancel (I-S10a), feedback (I-32a)
and the retention job (I-30) build on this contract and add their own parts
of it. §6 lists what those items must keep.

Plan of record: "Ask Dev in web — plan of record (2026-10-29)" (Linear), §2.0
INV-1/INV-2/INV-4, §2.5, §2.6, §2.7.5, §2.7.9, §5.3 E6. Decisions of record
(chris, 2026-10-29): ACR owns the conversation history; retention `0` means
deleted within 24 hours of the last turn; prod stays off.

Design review: codex `gpt-6-astra` xhigh, read-only, round
`i28-design-astra-r1` on the first draft: REQUEST CHANGES, six findings, all
argued from source. Every finding changed this design; §7 maps them.

## 1. What a thread is

A thread is one Ask Dev conversation of one principal. The client mints the
thread id once per conversation and sends it on every turn as
`X-ACR-Thread-ID` (already stored on the submission record by migration
`0056`). ACR builds the thread from the submission records grouped by
`(org_id, owner, thread_id)` plus thread metadata (retention, closed).

- The key includes the owner. Another principal's turn with the same
  `thread_id` is a turn of another thread, never of this one.
- A turn is a submission record. Turn order is `admitted_at`, then
  `submission_id`.
- A turn carries the question it was admitted with: the caller's own words,
  so a reload can show a running, unsaved or abandoned turn too.
- A turn names its result by id. The thread carries no answer content; the
  result is read through `GET /api/v1/context-fabric/investigations/{result_id}`,
  which decides the read again (INV-1, North Star check 18).
- Fields of record (plan §2.6): thread id, organization, principal, ordered
  turns with their result ids, retention `0|30`, deleted. Organization and
  principal come from authentication and never appear in a body.

## 2. Retention

| `retention_days` | Kept until (`expires_at`) |
| --- | --- |
| `0` | `last_turn_at` + 24 hours |
| `30` | `last_turn_at` + 30 days (720 hours) |

- The client chooses with `X-ACR-Thread-Retention-Days: 0|30` on a turn. The
  value of the latest turn (turn order) that named one is the thread's
  retention, so a user can change it while the conversation goes on. A
  thread whose turns named none has retention `0` (the shortest; a client
  that wants history asks for it).
- The header is not part of the submission digest, like `X-ACR-Thread-ID`: a
  retry of a submission keeps its first admission's value.
- Any other value (`1`, `30d`, `-0`, `00`, empty) is `400 invalid_request`.
  `ParseContextFabricThreadRetention` is the one grammar.
- `expires_at` is computed from `last_turn_at` and the retention
  (`ContextFabricThreadExpiresAt`); the Go validator holds the identity.
- From `expires_at` on, the thread is closed: every read of the thread and of
  its turns' results, submission status, replay and attach fails closed as
  for an unknown id. The retention job (I-30) then removes the backing set
  (plan §2.6, D-22) under §6.
- Results with no submission link (MCP, Workbench) are not in any thread and
  are out of scope.

## 3. Wire

### 3.1 New schemas

`context_fabric_thread.v1` — `GET /api/v1/context-fabric/threads/{thread_id}[?before=<cursor>]`:

| Field | Rule |
| --- | --- |
| `schema_version` | const |
| `thread_id` | client correlation grammar `^[A-Za-z0-9._:-]{8,128}$` |
| `retention_days` | `0` or `30` |
| `turn_count` | every turn of the thread, ≥ 1 |
| `created_at` | the first turn's `admitted_at` |
| `last_turn_at` | the latest turn's `admitted_at` |
| `expires_at` | `last_turn_at` + the retention window |
| `turns` | one page of 1..100 turns, oldest first: the latest turns, or the turns before `before` |
| `earlier_turns_cursor` | present when turns earlier than the page exist; pass it as `before` |

A turn: `submission_id`, `state` (the submission states `running`, `done`,
`done_unsaved`, `abandoned`), `question` (the admitted question, 1..8000
runes, as the request's own bound; absent only on a turn admitted before the
service recorded questions), `admitted_at`, `updated_at`, and on a `done`
turn exactly one of:

- `result_id` — the result read by id would serve the result to the reader
  now;
- `result_withheld: true` — it would not; the id is not given.

`context_fabric_thread_list.v1` — `GET /api/v1/context-fabric/threads`:
`threads[]` (≤ 50) of summaries (`thread_id`, `retention_days`, `turn_count`,
`created_at`, `last_turn_at`, `expires_at`), newest `last_turn_at` first, then
`thread_id`; `next_cursor` (opaque, present when more threads exist). Query:
`limit` 1..50 (default 20), `cursor`. Closed threads are not listed.

Both cursors share one grammar (`ValidContextFabricThreadCursor`: 1..512
URL-safe base64 characters). The Go validators hold what the schema cannot:
turn and list order, unique ids, `expires_at` = `last_turn_at` + window, the
turns inside `created_at..last_turn_at`, `created_at` equal to the first
turn on the page that reaches it, and a cursor only when earlier turns exist.

A Go value cannot tell an absent key from a key given its zero value, so the
consumer decoders `ParseContextFabricThread` and
`ParseContextFabricThreadList` hold raw bytes to what the schema holds before
`Validate`: no key twice, no unknown key (nor one spelled in another case),
no trailing content, valid UTF-8, no null anywhere (encoding/json reads a
null into a non-pointer field as no value, so `"retention_days": null` would
read as `0`; no field is nullable), no key present as empty, false or zero
where the writer omits the field, and no required key missing (a missing
`retention_days` would read as `0` too). Every string bound is the schema's exactly (the
question is 1..8000 runes, untrimmed). A key given twice is the one rule
only Go holds: JSON Schema has none for it.

### 3.2 Routes (published here, served by I-29)

| Route | Success | Not the caller's, unknown, closed, malformed |
| --- | --- | --- |
| `GET /api/v1/context-fabric/threads` | 200 list | — (a bad `limit`/`cursor` is 400) |
| `GET /api/v1/context-fabric/threads/{thread_id}` | 200 thread | 404 `not_found`, one body (a bad `before` is 400) |
| `DELETE /api/v1/context-fabric/threads/{thread_id}` | 204 | 204 for a well-formed id (idempotent, existence-neutral); 404 for a malformed id |

All three are protected (`context:read`, the `agent_context_runtime`
entitlement, web assertions allowed), like the submission status read.
Delete runs under `context:read`: it removes only the caller's own records.
Deleting closes the thread at once (§6).

### 3.3 Changes to existing contracts (expand only)

- Create investigation: new optional request header
  `X-ACR-Thread-Retention-Days`; the `X-ACR-Thread-ID` description now says
  what a thread is and when a turn is refused.
- `error.v1`: new code `thread_closed` (409, not retryable): the turn's
  `X-ACR-Thread-ID` (or `X-ACR-Submission-ID`) belongs to a thread of the
  caller that was deleted or has expired, while ACR holds its closure record
  (§6 T1). Nothing runs. Start a new thread.
- `StoredResultGate`: new surface `thread` (§4), in the closed vocabulary and
  the `StoredResultAuthorization` event's vocabulary.
- Nothing becomes required in a published schema; no member is removed or
  redefined. New schemas carry no baseline, so their required fields are not
  newly required fields (`internal/contractcheck/required_baseline.go`
  compares only schemas present at the baseline tag).
- INV-4: nothing emits a new field, code or surface until I-29 ships, and the
  web and ask-dev pins move with the M4 train before any live proof.

## 4. The gate surface (INV-1)

`StoredResultGate` gains the surface `thread`. The thread read takes, for the
result of each `done` turn on the page, the same serving decision
`GET /api/v1/context-fabric/investigations/{result_id}` takes — the gate on
surface `thread`, then that read's serving rules (today the flood rule
`StoredResultFloodWithheld` and the stored work-item tuple) — through one
shared function, live:

- served → `result_id`;
- not served (a gate denial of any reason, or a serving rule that withholds,
  or a result row that is gone) → `result_withheld: true`, no id;
- unavailable → the whole read is `503`, never a partial thread.

So a thread never offers a link the reader cannot open now. The thread read
never serves result content: the flood census classifies the surface as
"not served to a caller", with that reason. The decision line is the
existing `StoredResultAuthorization` event with `surface=thread`.

Thread list, read and delete are keyed by the principal, the same principal
check the submission status read uses: another principal's thread is not
found by construction. Feedback, attach and cancel get their own surfaces
with their items (I-32a, I-S10a).

## 5. Decisions and alternatives

| Decision | Chosen | Alternative not taken, and why |
| --- | --- | --- |
| Thread content | ids, states and each turn's admitted question | ids and states only (first draft): a reload cannot show a running, unsaved or abandoned turn's question (review F3). The answer stays in the result, read through the gate. |
| Question read policy | served to the owner with the thread, no result gate | through the result gate: running, unsaved and abandoned turns have no result to decide. The question is the caller's own input, not derived evidence; it is in the thread's backing set and goes with retention. |
| Result id on a turn | only when the result read by id would serve it | gate admission alone (first draft): the read by id also withholds (flood rule, work-item tuple), so the link could still be dead (review F6). |
| Retention default | `0` | `30`: retains content no one asked to keep. |
| Retention change | latest turn's header wins (turn order) | a separate PATCH route: one more surface for one bit. |
| Long threads | pages of 100 turns with `earlier_turns_cursor` / `before` | latest 100 only (first draft): earlier turns of a retained thread were out of reach after a reload (review F5). |
| Delete answer | 204 for any well-formed id | 404 for an unknown id: also existence-neutral, but a retried delete after a lost response would read as a failure. |
| Closed thread on a new turn | `409 thread_closed` while the closure record lives (§6 T1) | start a fresh thread under the same id: would mix new turns with rows the job is about to delete, and reopen a deleted conversation (review F1). |

## 6. What the store, the routes and the retention job must keep

These are obligations of I-29, I-S10a and I-30, written here because they are
part of what the contract promises. Each needs its own executed proof in the
item that implements it.

- **T1 Closure outlives the content.** Delete and expiry close a thread. The
  service keeps a content-free closure record — organization, owner, thread
  id, the thread's submission ids and their digests, `closed_at`; no
  question, no result id, no answer — until the thread's `expires_at`. While
  it lives, a request with that `X-ACR-Thread-ID`, or a repeat of one of its
  submission ids, is `409 thread_closed` and runs nothing (INV-2 holds across
  delete). After it is removed, those ids are unknown: a request with them is
  new. That residual is bounded by the client contract — a submission id is
  retried only within its run deadline (`W` ≤ 600 s), and a thread id is
  minted per conversation (plan §2.5) — and by retention itself: a closure
  record lives at least 24 hours.
- **T2 Reservation fence.** A run admitted under a submission saves its
  result bound to the submission's owner, or not at all. Delete and the
  retention job never remove a submission record while its run can still
  save (state `running`, or `abandoned` within its save window). A save of a
  reserved result id whose record is gone is refused, never stored unbound:
  an unbound row would be readable by the whole organization
  (`stored_result_authorization.go` admits unbound results by design).
- **T3 A shared result lives as long as a live turn names it.** Answer reuse
  can make turns of two threads of one owner name one result
  (`answer_reuse.go` keeps the stored result id). Closing a thread stops its
  turns from naming the result; the retention job removes a result only when
  no turn of a live thread names it. A closed thread never serves it; a live
  thread still does.
- **T4 One serving decision.** The thread read's `result_id` /
  `result_withheld` choice and the read by id are one function (§4). A test
  plants a withholding rule and sees the thread withhold.
- **T5 Unsaved stays unsaved.** A `done_unsaved` answer was shown once and is
  not recoverable. The turn keeps its question, so the client can say
  "answered but not saved — ask again".
- **T6 Closed means closed everywhere.** From a delete or from `expires_at`,
  thread read, thread list, result by id (for the thread's results not named
  by a live thread, T3), submission status, replay and attach fail closed as
  for an unknown id.

## 7. Review findings (round `i28-design-astra-r1`)

| Finding | Severity | Change |
| --- | --- | --- |
| F1 cleanup reopens deleted threads and lets a delayed retry run again | P1 | T1 closure record until `expires_at`; `thread_closed` also answers a repeat of a closed thread's submission id; the residual after removal is stated and bounded |
| F2 removing the reservation turns a late save into an organization-readable result | P1 | T2 reservation fence |
| F3 ids and states cannot rebuild running, abandoned or unsaved turns | P1 | `question` on each turn; T5 |
| F4 cross-thread reuse makes retention promises conflict | P2 | T3 reference-held result lifetime |
| F5 the latest-100 cap strands retained history | P2 | turn pages: `before` / `earlier_turns_cursor` |
| F6 gate admission does not prove the result is readable | P2 | §4 one serving decision; T4 |

## 8. Diagram

`docs/design/context-fabric-architecture-diagrams.md` §18.
