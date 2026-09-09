#!/usr/bin/env python3
"""lane-corpus-v2 replication harness -- 3 reps/question, NEEDS-DRIVEN turn chain.

v2: fixes two defects found by lane-4926-pr-b in the inherited pr3/rig-advance-14
driver shape (v1 of this file copied that shape and inherited both):

  1. KIND OFFER MUST BE MATCHED, NEVER INDEXED. An `expected_kind` clarification
     offers `structure_needs.kind_options`, each with its own `kind`. Taking
     index 0 can confirm a WRONG kind (observed: "which repositories does the
     platform team own" offered ci_pipeline_run at index 0) -- a served answer
     under that receipt is a wrong-kind answer manufactured by the instrument,
     not a measurement. Fix: match against corpus.REQUESTED_KIND[qid], the kind
     the question is DECLARED to be about (never inferred from question text).
     No match -> warn loudly and record the row as NO RESULT (do not let it
     silently look served).
  2. TURNS MUST BE NEEDS-DRIVEN, WITH OFFERS CARRIED FORWARD IN MEMORY, not
     turn-number-driven reading only the immediately-previous turn. A need
     (kind / window / subject) can be offered on turn 1 and be gone from
     `structure_needs`/`subject_resolution` by turn 2 while STILL sitting in
     `missing` -- a driver that only reads the previous turn's offer never
     answers it, and the row loops clarification_required to MAX_TURNS,
     reading as UNSERVED (a harness gap wearing a regression's clothes, same
     shape as the 09-02 20:49 brief addendum). Fix: a `memory` dict holds the
     newest offer of each type WITH the result_id that produced it; every turn
     answers every need present in `missing` for which memory holds an offer.
     v4 supersedes: gating is now purely OFFER-presence, not `missing`-string
     content at all (the subject need has >=4 unstable wire spellings).
  3. SUBJECT CANDIDATE MUST BE MATCHED BY KIND, NEVER INDEX 0. Committing
     `subject_resolution.candidates[0]` can commit a WRONG-KIND anchor
     (observed: "which repositories does the platform team own" -- every
     candidate matched "platform" at 0.5 confidence/state ambiguous, a
     ci_pipeline_run sorted first, index 0 committed it as the scope anchor,
     the row died no_match). Fix: match against corpus.ANCHOR_KIND[qid], the
     anchor's declared kind (distinct from REQUESTED_KIND -- the anchor of
     "the platform team" is team even though the question's requested_kind
     is repository). No match -> leave uncommitted, never index-0.

MAX_TURNS raised 3->5 and per-turn retry cap kept at 5, matching lane-s7b-i-pr3
/ lane-rig-advance-14's validated method (their docstring, inherited verbatim
by lane-4926-pr-b's run_rig.py:8-15): retry a retryable failure up to 5x per
turn (model-output nondeterminism else reads as a regression), and priorWindow/
priorSubject/priorKind receipts are sent ONLY while their need is still
outstanding -- a receipt resent after its need left `missing` gets an
instant `veto_stale_superseded_offer`/`veto_unresolved`, not a real turn.

Posed through :3040 (ask-dev) -> :18090 (acr-api), org 70d529e0, kiac dh_0830
real data. request_id/telemetry join happens offline in tally.py.
"""
import json
import os
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent))
from corpus import CORPUS, REQUESTED_KIND, ANCHOR_KIND  # noqa: E402
from validators import validate_attempt, validate_response  # noqa: E402
# CHAOS-5380 review round 2: the producer and every reader SHARE these values rather than
# a pin deriving one from the other -- a derived oracle was measured hollowing out
# silently, reading the source shape its author thought of and getting the right answer by
# accident. They live in `contract`, which imports nothing: the PRODUCER MUST NOT IMPORT
# THE CLASSIFIER, or the direction of the dependency starts asserting something about who
# defines the contract.
import contract  # noqa: E402

# ONE env var, default
# byte-identical to the frozen value, so an unset environment reproduces the rig
# runs exactly. Needed because the 3A-read control drives a PRIVATE ask-dev leg on
# :3042 (in front of a private embed-free acr-api on :18092) while the shared rig
# keeps serving on :3040 — a control must never touch the shared acceptance surface.
BASE = os.environ.get("CORPUS_BASE", "http://127.0.0.1:3040/api/investigations")
OUTDIR = Path(__file__).parent / "replicate"
OUTDIR.mkdir(exist_ok=True)
MAX_TURNS = 5
MAX_ATTEMPTS_PER_TURN = 5
SERVED_STATUSES = {"complete", "partial", "degraded", "answered"}
TERMINAL_STATUSES = SERVED_STATUSES | {"no_match", "refused"}


def validate_live_payload(status, payload):
    """Validate a LIVE response before anything dereferences it.

    The artefact path has one validating loader; this is the same boundary on the other
    ingestion point. `load_attempt` cannot serve here because there is no file -- but the
    SHAPE check is the same one, so a server returning `{"result": [1]}` is rejected here
    rather than crashing a `.get()` three frames later. A payload that fails is replaced
    by a failure envelope naming the reason, so the row records what happened instead of
    the run dying.
    """
    # Validated as a RESPONSE, which is what it is. Wrapping it in a synthetic attempt
    # envelope made the measured envelope fields -- `dt` and `request`, which only exist
    # once the harness has WRITTEN the artefact -- required of a live body that cannot
    # carry them, so every live response would come back malformed. Presence is measured
    # from stored artefacts; the live payload is only the response half of one.
    ok, reason = (validate_response(payload) if isinstance(payload, dict)
                  else (False, f"response body is {type(payload).__name__}, not a mapping"))
    if isinstance(payload, dict) and not ok:
        return {"failure": {"code": "acr_malformed_response", "message": reason,
                            "httpStatus": status}}
    if not isinstance(payload, dict):
        return {"failure": {"code": "acr_malformed_response",
                            "message": f"response body is {type(payload).__name__}, not a mapping",
                            "httpStatus": status}}
    return payload


def post(body):
    """Returns (status, response, dt, body_undecodable).

    r4 (astra) found two defects in the r3 P1-1 fix, both fixed here together because
    they are the same shape of mistake: a failure inside the "we got a response" path
    must never be allowed to look like something it is not.

    1. `e.read()` on the HTTPError arm was UNPROTECTED: an exception raised reading a
       truncated error body (IncompleteRead) is not caught by the `except` clause it is
       raised inside -- a "sibling" except does not catch it -- so it propagated out of
       `post` entirely and crashed the shard instead of producing a row. Reading the body
       is now wrapped on BOTH arms (success and HTTPError); a read failure is treated
       exactly like a decode failure -- the exchange completed, the BODY could not be
       obtained -- never like a transport failure.
    2. The "body did not decode" signal was a KEY INSIDE THE RESPONSE DICT
       (`{UNDECODABLE_BODY_KEY: ...}`), the SAME namespace server-controlled JSON content
       lives in -- so a real, validly-decoded response that happened to carry that exact
       key was indistinguishable from a genuine decode failure. `body_undecodable` is now
       a FOURTH RETURN VALUE, reported by `run_replicate` as an ARTEFACT-level sibling
       field (next to `status`/`response`/`dt`), never inside `response` itself -- a
       field the server's own JSON content can never touch, because the server only
       controls what is INSIDE the response body, not the envelope the harness writes
       around it.
    """
    data = json.dumps(body).encode("utf-8")
    req = urllib.request.Request(BASE, data=data, headers={"Content-Type": "application/json"}, method="POST")
    t0 = time.time()
    try:
        with urllib.request.urlopen(req, timeout=180) as resp:
            status = resp.status
            try:
                raw = resp.read()
            except Exception:
                raw = None
    except urllib.error.HTTPError as e:
        status = e.code
        try:
            raw = e.read()
        except Exception:
            raw = None
    except Exception as e:  # noqa: BLE001 -- a transport failure is a row, not a crash
        # NO STATUS EVER CAME BACK (connection refused, DNS, read timeout before a
        # response line was received): the only arm that writes status 0.
        return 0, {contract.ERROR_BODY_KEY: str(e)}, time.time() - t0, False
    # An exchange COMPLETED -- `status` is real. A body that could not be READ (raw is
    # None) or could not be DECODED is the same fact from here: the exchange happened,
    # the body did not. Neither ever falls back into the transport arm's status=0.
    if raw is not None:
        try:
            payload = json.loads(raw.decode("utf-8"))
        except Exception:
            raw = None
    if raw is None:
        return status, {}, time.time() - t0, True
    return status, validate_live_payload(status, payload), time.time() - t0, False


def is_retryable(status, payload):
    return bool((payload or {}).get("failure", {}).get("retryable"))


def update_memory_and_build_receipts(prev_result, memory, want_kind, anchor_kind, warn):
    """Record every offer prev_result makes, then answer every need in its
    `missing` list for which memory (this turn's or an earlier turn's offer)
    has something to answer with. Returns (receipt_fields, missing_set,
    wrong_kind_bool).

    OFFER-DRIVEN, NOT `missing`-STRING-DRIVEN. Confirmed live on
    neg-mentions-teams-but-not-grouped rep1 (2026-09-03): the subject
    clarification's presence in `structure_needs.missing` is NOT a stable
    vocabulary -- observed as absent entirely (structure_needs itself gone
    from the response, subject_resolution.candidates non-empty regardless),
    then `subject_handle`, then `subject_candidate`+`subject_handle` across
    turns of the SAME replicate. Gating priorSubjectReceipts on `"subject_anchor"
    in missing or "subject" in missing` (this file's own v3, and
    lane-4926-pr-b's run_rig.py:137) never fires when `missing` is absent,
    so the receipt is never sent, the server re-offers the SAME original
    clarification from scratch next turn, and the replicate cycles without
    converging until MAX_TURNS. Matches ask-dev's own source
    (clarification-popup.ts:168): the subject_resolution candidate page is
    gated on `status === "clarification_required"` alone, never on `missing`.
    Fix: an offer is "outstanding" iff it is PRESENT on THIS turn's response
    (kind_options / window_options-or-window_clarification.options /
    subject_resolution.candidates, each non-empty); answer every such offer
    every turn. `memory` still exists only so an in-flight offer isn't lost
    to a caller that reads solely the newest response, but it is DROPPED the
    instant it is no longer being offered -- never resent once satisfied,
    the same rule rig-advance-14 already established for windows."""
    rid = prev_result.get("result_id")
    sn = prev_result.get("structure_needs") or {}
    wc = prev_result.get("window_clarification")
    window_opts_now = sn.get("window_options") or (wc.get("options") if wc else None)
    candidates_now = (prev_result.get("subject_resolution") or {}).get("candidates") or []

    if sn.get("kind_options"):
        memory["kind"] = (rid, sn["kind_options"])
    else:
        memory.pop("kind", None)
    if window_opts_now:
        memory["window"] = (rid, window_opts_now)
    else:
        memory.pop("window", None)
    if candidates_now:
        memory["subject"] = (rid, candidates_now)
    else:
        memory.pop("subject", None)

    out = {}
    wrong_kind = False

    if "kind" in memory:
        krid, opts = memory["kind"]
        match = [o for o in opts if o.get("kind") == want_kind]
        if match:
            out["priorKindReceipts"] = [{"result_id": krid, "receipt_id": match[0]["receipt_id"]}]
        else:
            # Binding rule: NO index-0 fallback. A row with
            # no offer matching its declared requested_kind is left UNANSWERED
            # -- the caller records it as no_matching_kind_offer (UNKNOWN),
            # never a manufactured answer, and the chain continues regardless.
            wrong_kind = True
            warn(f"NO kind option matches requested_kind={want_kind!r}; "
                 f"options={[o.get('kind') for o in opts]} -- leaving expected_kind "
                 f"UNANSWERED (no_matching_kind_offer), never falling back to an index")

    if "window" in memory:
        wrid, opts = memory["window"]
        pick = next((o for o in opts if o.get("relative_id") == "trailing_90d"), None) or (opts[0] if opts else None)
        if pick:
            out["priorWindowReceipts"] = [{"result_id": wrid, "receipt_id": pick["receipt_id"]}]

    wrong_subject = False
    subject_kind_mismatch = False
    if "subject" in memory:
        srid, cands = memory["subject"]
        # Binding rule (defect #6): candidates[0] can be a
        # WRONG-KIND anchor -- confirmed live on "which repositories does the
        # platform team own": every candidate matched "platform" at 0.5
        # confidence (state ambiguous), a ci_pipeline_run sorted first, and
        # index 0 committed it as the scope anchor, killing the row no_match.
        # Match the candidate's OWN declared kind against this row's
        # anchor_kind (never inferred from the question, never index-0).
        #
        # v7 amendment (defect #7, lane-4926-pr-b, ruled): a SINGLE candidate
        # is not a choice. Refusing to commit it because it disagrees with
        # this lane's own ANCHOR_KIND guess INVENTS a failure -- confirmed
        # live on neg-mentions-teams-but-not-grouped, which offers exactly
        # one `team` candidate while this file's anchor table said
        # `repository`; refusing it produced a false 0/3. One candidate is
        # always committed; a kind disagreement is recorded as its own
        # `subject_kind_mismatch` flag, never folded into served/unserved
        # and never treated as a refusal. Two-or-more candidates keep the
        # match-by-anchor_kind rule (ambiguity IS real there).
        if len(cands) == 1:
            c = cands[0]
            out["priorSubjectReceipts"] = [{"result_id": srid, "receipt_id": c["receipt_id"]}]
            committed_kind = (c.get("subject") or {}).get("kind")
            if anchor_kind and committed_kind != anchor_kind:
                subject_kind_mismatch = True
                warn(f"single subject candidate kind={committed_kind!r} != anchor_kind={anchor_kind!r} "
                     f"-- COMMITTING it anyway (a single candidate is not a choice), "
                     f"flagged subject_kind_mismatch (not a refusal)")
        elif cands:
            match = [c for c in cands if (c.get("subject") or {}).get("kind") == anchor_kind] if anchor_kind else []
            if match:
                out["priorSubjectReceipts"] = [{"result_id": srid, "receipt_id": match[0]["receipt_id"]}]
            else:
                wrong_subject = True
                warn(f"NO subject candidate (of {len(cands)}) matches anchor_kind={anchor_kind!r}; "
                     f"candidate kinds={[(c.get('subject') or {}).get('kind') for c in cands]} -- "
                     f"leaving subject UNCOMMITTED (no_matching_subject_candidate), never index-0")

    return out, set(sn.get("missing") or []), wrong_kind, wrong_subject, subject_kind_mismatch


def run_replicate(qid, question, rep, warn=print):
    tag = f"{qid} rep{rep}"
    want_kind = REQUESTED_KIND.get(qid, "")
    want_anchor_kind = ANCHOR_KIND.get(qid)
    memory = {}
    body = {"question": question}
    total_attempts = 0
    last_status, last_payload = None, None
    wrong_kind_flag = False
    wrong_subject_flag = False
    subject_kind_mismatch_flag = False
    chain = []

    for turn in range(1, MAX_TURNS + 1):
        status, payload, attempts_used = None, None, 0
        for attempt in range(1, MAX_ATTEMPTS_PER_TURN + 1):
            status, payload, dt, body_undecodable = post(body)
            attempts_used += 1
            fname = OUTDIR / f"{qid}-rep{rep}-t{turn}-a{attempt}.json"
            with open(fname, "w") as f:
                # `body_undecodable` is an ARTEFACT-level sibling of `response`, never a
                # key inside it -- see post()'s docstring (r4 finding: a key inside
                # `response` shares a namespace with server-controlled content and a
                # validly-decoded response could impersonate it).
                json.dump({"request": body, "status": status, "response": payload,
                          "dt": round(dt, 1), "body_undecodable": body_undecodable},
                         f, indent=2)
            print(f"  [{tag}] t{turn} a{attempt}: http={status} dt={dt:.1f}s", flush=True)
            if contract.is_success_status(status) or not is_retryable(status, payload):
                break
            print(f"    retryable failure ({(payload.get('failure') or {}).get('code')}), retrying...", flush=True)
        total_attempts += attempts_used
        last_status, last_payload = status, payload

        if not contract.is_success_status(status):
            chain.append(f"t{turn}=http{status}")
            break

        result = payload.get("result", {})
        st = result.get("status")
        chain.append(f"t{turn}={st}")

        if st in TERMINAL_STATUSES or not result:
            break

        def _warn(msg, _tag=tag):
            print(f"  [{_tag}] WARNING: {msg}", flush=True)
            warn(f"[{_tag}] {msg}")

        receipts, missing, this_turn_wrong_kind, this_turn_wrong_subject, this_turn_mismatch = \
            update_memory_and_build_receipts(result, memory, want_kind, want_anchor_kind, _warn)
        if this_turn_wrong_kind:
            # expected_kind has no offer matching this row's declared
            # requested_kind (no index-0 fallback, ever). Per the
            # correction: do NOT break here -- leave expected_kind UNMET
            # (no priorKindReceipts) and let the chain run to its own
            # terminal (served/no_match/refused, or MAX_TURNS exhaustion).
            # Breaking early measures nothing about what the engine does
            # with an unresolved kind need; continuing does.
            wrong_kind_flag = True
        if this_turn_wrong_subject:
            wrong_subject_flag = True
        if this_turn_mismatch:
            subject_kind_mismatch_flag = True
        if not receipts and turn > 1:
            # No new receipt of ANY kind this turn (every need either has no
            # offer, per above, or was already answered and left `missing`).
            # Resend the bare question -- an identical re-ask is itself a
            # terminal data point (the engine cannot get unstuck without the
            # unmet need) and MAX_TURNS bounds the cost.
            body = {"question": question}
        else:
            body = {"question": question, **receipts}

    # final_payload_status is always the ENGINE's own terminal outcome, never
    # overwritten by the harness -- wrong_kind_flag is reported alongside it,
    # never in place of it (continuing measures what the engine
    # does with an unresolved kind need, which requires seeing its real
    # terminal, not a harness-synthesized one).
    if contract.is_success_status(last_status):
        final_status = (last_payload or {}).get("result", {}).get("status")
        if final_status == "clarification_required" and turn >= MAX_TURNS:
            final_status = "clarification_required(max_turns_exhausted)"
    else:
        final_status = f"http_{last_status}:{(last_payload or {}).get('failure', {}).get('code')}"

    return {
        "id": qid, "rep": rep, "question": question,
        "final_http": last_status,
        "final_payload_status": final_status,
        "chain": " -> ".join(chain),
        "attempts": total_attempts,
        "wrong_kind_flag": wrong_kind_flag,
        "wrong_subject_flag": wrong_subject_flag,
        "subject_kind_mismatch_flag": subject_kind_mismatch_flag,
    }


def main():
    which = sys.argv[1:] if len(sys.argv) > 1 else None
    rows = []
    total = 0
    for row in CORPUS:
        if which and row["id"] not in which:
            continue
        for rep in range(1, 4):
            print(f"=== {row['id']} rep{rep} ===", flush=True)
            r = run_replicate(row["id"], row["text"], rep)
            rows.append(r)
            total += r["attempts"]
            print(f"  -> attempts={r['attempts']} cumulative_total={total} chain={r['chain']}", flush=True)
    with open(OUTDIR / "summary.json", "w") as f:
        json.dump(rows, f, indent=2)
    print(f"DONE {len(rows)} replicate-rows, {total} total attempts -> {OUTDIR}/summary.json")


if __name__ == "__main__":
    main()
