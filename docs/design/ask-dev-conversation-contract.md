# Ask Dev conversation contract (threads)

Status: design for the contract PR (plan item I-28). The store and the routes
come in the next item (I-29); the run lease, attach and cancel (I-S10a),
feedback (I-32a) and the retention job (I-30) build on this contract and add
their own parts of it.

Plan of record: "Ask Dev in web — plan of record (2026-10-29)" (Linear), §2.0
INV-1/INV-2/INV-4, §2.5, §2.6, §2.7.5, §2.7.9, §5.3 E6. Decisions of record
(chris, 2026-10-29): ACR owns the conversation history; retention `0` means
deleted within 24 hours of the last turn; prod stays off.

## 1. What a thread is

A thread is one Ask Dev conversation of one principal. The client mints the
thread id once per conversation and sends it on every turn as
`X-ACR-Thread-ID` (already stored on the submission record by migration
`0056`). ACR builds the thread from the submission records grouped by
`(org_id, owner, thread_id)` plus thread metadata (retention, deleted).

- The key includes the owner. Another principal's turn with the same
  `thread_id` is a turn of another thread, never of this one.
- A turn is a submission record. Its order is `admitted_at`, then
  `submission_id`.
- The thread holds no answer content. A turn names its result by id; the
  result itself is read through `GET /api/v1/context-fabric/investigations/{result_id}`,
  which decides the read again (INV-1, North Star check 18).
- Fields of record (plan §2.6): thread id, organization, principal, ordered
  result ids, retention `0|30`, deleted. Organization and principal come from
  authentication and never appear in a body.

## 2. Retention

| `retention_days` | Kept until (`expires_at`) |
| --- | --- |
| `0` | `last_turn_at` + 24 hours |
| `30` | `last_turn_at` + 30 days (720 hours) |

- The client chooses with `X-ACR-Thread-Retention-Days: 0|30` on a turn. The
  value of the latest turn that named one is the thread's retention, so a user
  can change it while the conversation goes on. A thread whose turns named
  none has retention `0` (the shortest; a client that wants history asks for
  it).
- The header is not part of the submission digest, like `X-ACR-Thread-ID`: a
  retry of a submission keeps its first admission's value.
- Any other value (`1`, `30d`, `-0`, empty) is `400 invalid_request`. One
  parser in `internal/contracts/v1` holds the grammar.
- `expires_at` is computed from `last_turn_at` and the retention, never
  stored on its own; the Go validator holds the identity.
- From `expires_at` on, every read of the thread and of its turns' results,
  submission status, replay and attach fails closed as for an unknown id. The
  retention job (I-30) then deletes the backing set (plan §2.6, D-22).
- Results with no submission link (MCP, Workbench) are not in any thread and
  are out of scope.

## 3. Wire

### 3.1 New schemas

`context_fabric_thread.v1` — `GET /api/v1/context-fabric/threads/{thread_id}`:

| Field | Rule |
| --- | --- |
| `schema_version` | const |
| `thread_id` | client correlation grammar `^[A-Za-z0-9._:-]{8,128}$` |
| `retention_days` | `0` or `30` |
| `created_at` | the first turn's `admitted_at` |
| `last_turn_at` | the latest turn's `admitted_at` |
| `expires_at` | `last_turn_at` + the retention window |
| `turn_count` | every turn of the thread, ≥ 1 |
| `turns` | the latest `min(turn_count, 100)` turns, oldest first |

A turn: `submission_id`, `state` (the submission states `running`, `done`,
`done_unsaved`, `abandoned`), `admitted_at`, `updated_at`, and on a `done`
turn exactly one of `result_id` (the gate admits the reader) or
`result_withheld: true` (the gate denies the reader now). When `turn_count` is
over 100, `turn_count - len(turns)` earlier turns are omitted and the count
says so (check 12: an omission is stated, not silent).

`context_fabric_thread_list.v1` — `GET /api/v1/context-fabric/threads`:
`threads[]` (≤ 50) of summaries (`thread_id`, `retention_days`, `turn_count`,
`created_at`, `last_turn_at`, `expires_at`), newest `last_turn_at` first, then
`thread_id`; `next_cursor` (opaque, present when more threads exist). Query:
`limit` 1..50 (default 20), `cursor`.

Every field of both new schemas is required except `result_id`,
`result_withheld` and `next_cursor`, which have exact presence rules. New
schemas carry no baseline, so this is not a newly required field in the
expand-then-contract sense.

### 3.2 Routes (published here, served by I-29)

| Route | Success | Not the caller's, unknown, deleted, expired, malformed |
| --- | --- | --- |
| `GET /api/v1/context-fabric/threads` | 200 list | — |
| `GET /api/v1/context-fabric/threads/{thread_id}` | 200 thread | 404 `not_found`, one body |
| `DELETE /api/v1/context-fabric/threads/{thread_id}` | 204 | 204 for a well-formed id (idempotent, existence-neutral); 404 for a malformed id |

All three are protected (`context:read`, the `agent_context_runtime`
entitlement, web assertions allowed), like the submission status read.
Deleting a thread removes it and the results of its turns from every read at
once (thread, list, result by id, replay, status, attach); the rows go with
the retention job.

### 3.3 Changes to existing contracts (expand only)

- Create investigation: new optional request header
  `X-ACR-Thread-Retention-Days`.
- `error.v1`: new code `thread_closed` (409, not retryable): the turn names a
  thread of the caller that was deleted or has expired, while ACR still holds
  a record of it. Start a new thread. After the retention job removed it, the
  id names nothing and a turn with it starts a new thread; a client mints a
  new thread id per conversation (plan §2.5), so it never meets that window.
- Nothing becomes required in a published schema; no member is removed or
  redefined. `go run ./cmd/contractcheck -required-baseline` passes.
- INV-4: nothing emits a new field or code until I-29 ships, and the web and
  ask-dev pins move with the M4 train before any live proof.

## 4. The gate surface (INV-1)

`StoredResultGate` gains the surface `thread`. The thread read decides the
result of each `done` turn on it, live, before it names the result:

- admitted → `result_id`;
- denied (any reason: owner, grant, subject) → `result_withheld: true`, no id;
- unavailable → the whole read is `503`, never a partial thread.

The thread read never serves result content, so the flood rule
(`StoredResultFloodWithheld`) does not apply to it; the surface is classified
as "not served to a caller" in the flood census, with that reason. The
decision line is the existing `StoredResultAuthorization` event with
`surface=thread`.

Thread list, read and delete are keyed by the principal, the same principal
check the submission status read uses: another principal's thread is not
found by construction. Feedback, attach and cancel get their own surfaces
with their items (I-32a, I-S10a).

## 5. Decisions and alternatives

| Decision | Chosen | Alternative not taken, and why |
| --- | --- | --- |
| Thread content | ids and states only | a title or the question per turn: the plan's thread fields are ids, retention and deleted; the question lives in the result row and is read through the gate. Additive later if web needs it. |
| Gate on the thread read | per `done` turn, surface `thread` | principal check only (cheaper): a thread would then offer a result id the reader can no longer open, a dead link that reads as an answer (checks 12, 18). |
| Retention default | `0` | `30`: retains content no one asked to keep. |
| Retention change | latest turn's header wins | a separate PATCH route: one more surface for one bit. |
| Turn cap | serve the latest 100, state the rest in `turn_count` | refuse turn 101 (new error code, breaks a long conversation). |
| Delete answer | 204 for any well-formed id | 404 for an unknown id: also existence-neutral, but a retried delete after a lost response would read as a failure. |
| Closed thread on a new turn | `409 thread_closed` | start a fresh thread under the same id: would mix new turns with rows the job is about to delete. |

## 6. Diagram

`docs/design/context-fabric-architecture-diagrams.md` §18.
