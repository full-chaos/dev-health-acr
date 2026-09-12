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

# CHAOS-5562 r2: refuse BEFORE the `corpus` import below, which needs an external
# module on sys.path for a reason that has nothing to do with CORPUS_BASE. r2 review
# found that a direct run with CORPUS_BASE unset AND no corpus module supplied hit a
# raw `ModuleNotFoundError` instead of ever reaching require_base()'s message -- the
# module-level import runs before any of this file's own code, so no check placed
# later in the file (require_base() included) can pre-empt it. Gated on
# `__name__ == "__main__"` so `import harness` (every pin file does this without
# CORPUS_BASE set) is completely unaffected; only a DIRECT run refuses this early.
_MISSING_BASE_MSG = (
    "CORPUS_BASE is not set -- refusing to start. There is no default rig "
    "leg; set CORPUS_BASE to the investigations endpoint you own (see "
    "scripts/corpus/README.md)."
)
if __name__ == "__main__" and not os.environ.get("CORPUS_BASE"):
    sys.exit(_MISSING_BASE_MSG)

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

# CHAOS-5562: NO DEFAULT. A silent default to the shared rig leg is exactly how
# lane-thread-a-engine drove 5 investigations into a shared rig leg it did not
# own, on 2026-09-11, without meaning to touch it at all -- an unset environment
# must refuse, not reproduce that leg by accident. Every caller states its own base explicitly
# (`run_corpus_sequential.sh` / `run_corpus_parallel.sh` already export one before
# invoking `run_shard.py`; that is THEIR considered default, not this module's).
BASE = os.environ.get("CORPUS_BASE")
# Optional guard rail: refuse to proceed once the first response names a served
# build that disagrees with this. Env by default (so it reaches every caller that
# imports this module -- run_shard.py included -- without extra plumbing); harness.py's
# own CLI also accepts `--expected-build`, which overrides the env value for a direct run.
EXPECTED_BUILD = os.environ.get("CORPUS_EXPECTED_BUILD")
OUTDIR = Path(__file__).parent / "replicate"
OUTDIR.mkdir(exist_ok=True)
MAX_TURNS = 5
MAX_ATTEMPTS_PER_TURN = 5
SERVED_STATUSES = {"complete", "partial", "degraded", "answered"}
TERMINAL_STATUSES = SERVED_STATUSES | {"no_match", "refused"}


class MissingCorpusBase(RuntimeError):
    """CORPUS_BASE is unset. Refuse to start rather than default to a rig leg."""


class ServedBuildMismatch(RuntimeError):
    """The first response's service_version disagrees with the caller's expected build."""


def require_base():
    if not BASE:
        raise MissingCorpusBase(_MISSING_BASE_MSG)


def _service_version(response):
    """Same extraction shape as run_shard.py's own reading of `versions.service_version`,
    so the two never disagree about where the build name lives."""
    if not isinstance(response, dict):
        return None
    result = response.get("result")
    if not isinstance(result, dict):
        return None
    versions = result.get("versions")
    if not isinstance(versions, dict):
        return None
    return versions.get("service_version")


def _redacted_base():
    """CORPUS_BASE with any userinfo (`user:pass@`) and query string stripped before it
    ever reaches a log line. r1 review found the un-redacted form printed a live
    credential/token straight into shard logs -- scheme+host+path is enough to show a
    caller which leg it hit; a query token or basic-auth password is never needed for
    that and must never be logged."""
    if not BASE:
        return BASE
    from urllib.parse import urlsplit, urlunsplit
    u = urlsplit(BASE)
    host = u.hostname or ""
    if u.port:
        host = f"{host}:{u.port}"
    return urlunsplit((u.scheme, host, u.path, "", ""))


_base_printed = False
_build_checked = False


def _note_base_selected():
    """Print CORPUS_BASE exactly once, before the first byte of the first real request
    goes out (CHAOS-5562) -- independent of whether that request ever gets a usable
    response, so this fires even if every attempt times out."""
    global _base_printed
    if _base_printed:
        return
    _base_printed = True
    print(f"[corpus] CORPUS_BASE={_redacted_base()}", flush=True)


def _report_first_response(status, response):
    """Check the served service_version against CORPUS_EXPECTED_BUILD, once, the first
    time a response actually CARRIES a service_version -- so a caller pointed at the
    wrong build finds out after very few requests, never after the whole run (CHAOS-5562).

    NOT gated on "the first response of any kind": r1 review found that a retryable
    failure (no body, no service_version) as the very first attempt consumed a naive
    one-shot flag and permanently disarmed the check -- a SECOND attempt then served the
    wrong build and nothing caught it. The flag is consumed only once a response actually
    yields a determinate service_version to compare; an indeterminate response (transport
    failure, malformed body, a body with no `versions.service_version`) is reported but
    leaves the check armed for the next response.
    """
    global _build_checked
    served = _service_version(response)
    if _build_checked:
        return
    if served is None:
        print(f"[corpus] response http={status} service_version=None "
              f"(undetermined, still watching)", flush=True)
        return
    _build_checked = True
    print(f"[corpus] first determined response: http={status} "
          f"service_version={served!r}", flush=True)
    if EXPECTED_BUILD and served != EXPECTED_BUILD:
        raise ServedBuildMismatch(
            f"served service_version={served!r} != expected {EXPECTED_BUILD!r} "
            f"(CORPUS_BASE={_redacted_base()}) -- refusing to continue"
        )


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
    # CHAOS-5562: refuse before the first byte goes anywhere near a socket.
    require_base()
    _note_base_selected()
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
        _report_first_response(0, {contract.ERROR_BODY_KEY: str(e)})
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
        _report_first_response(status, {})
        return status, {}, time.time() - t0, True
    # CHAOS-5562 r2: check against the RAW decoded payload, never the validated one.
    # r2 review found a payload that is malformed by SOME OTHER measure (an unrelated
    # required field missing/wrong-shaped) but genuinely carries a real
    # `versions.service_version` -- validate_live_payload replaces the whole body with
    # a bare failure envelope, which has no `result` key at all, so the genuine served
    # build was thrown away before this file ever looked at it. The raw payload is
    # already known to be a dict at this point (the json.loads above succeeded); a
    # response that is malformed in a way that also loses/omits the version is still
    # reported as indeterminate and leaves the check armed, same as before.
    _report_first_response(status, payload)
    validated = validate_live_payload(status, payload)
    return status, validated, time.time() - t0, False


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
    no_redeemable_offer_flag = False
    stop_reason = None
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
            if this_turn_wrong_kind or this_turn_wrong_subject:
                # Every offer THIS turn failed one of the two redemption rules
                # right above (no kind option satisfies requested_kind; no
                # candidate satisfies anchor_kind), and nothing else was
                # offered to redeem instead (no window need this turn) --
                # receipts is empty because the declared need cannot be met
                # by anything on offer, not because every need was already
                # answered. A bare re-ask cannot change that: the same
                # unredeemable offers recur next turn (chain_depth resets to
                # 0, no prior reference survives a bare re-ask), so
                # continuing only repeats this turn to MAX_TURNS. Stop here.
                no_redeemable_offer_flag = True
                stop_reason = "no_redeemable_offer_for_declared_kind"
                break
            # Every need was already answered and left `missing`, offering
            # nothing new to redeem. Resend the bare question -- an
            # identical re-ask is itself a terminal data point (the engine
            # cannot get unstuck without the unmet need) and MAX_TURNS
            # bounds the cost.
            body = {"question": question}
        else:
            body = {"question": question, **receipts}

    # final_payload_status is always the ENGINE's own terminal outcome, never
    # overwritten by the harness -- wrong_kind_flag/no_redeemable_offer_flag
    # are reported alongside it, never in place of it. A no-redeemable-offer
    # break still lands here on the SAME turn's real response (last_status/
    # last_payload were set for that turn before the break), so this reads
    # the engine's actual non-terminal status (e.g. "clarification_required"),
    # never a harness-synthesized one -- the same status MAX_TURNS exhaustion
    # would have reached on turn 5, just recorded at the turn it first became
    # unrecoverable instead of after three more identical re-asks.
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
        "no_redeemable_offer_flag": no_redeemable_offer_flag,
        "stop_reason": stop_reason,
    }


def _parse_argv(argv):
    """Corpus-id filters plus an optional `--expected-build VALUE` / `--expected-build=VALUE`
    and an optional `--check-only` flag. A CLI expected-build value overrides
    CORPUS_EXPECTED_BUILD for this run; unset leaves the env value (possibly None) in
    place. `--check-only` needs no id filter and ignores any given."""
    ids = []
    expected_build = EXPECTED_BUILD
    check_only = False
    i = 0
    while i < len(argv):
        a = argv[i]
        if a == "--expected-build":
            i += 1
            if i >= len(argv):
                sys.exit("--expected-build requires a value")
            expected_build = argv[i]
        elif a.startswith("--expected-build="):
            expected_build = a.split("=", 1)[1]
        elif a == "--check-only":
            check_only = True
        else:
            ids.append(a)
        i += 1
    return ids, expected_build, check_only


def check_only():
    """CHAOS-5562 r3: make exactly ONE request and let post()'s own checks
    (require_base, print the base, print+check the first determined service_version)
    run -- nothing else. Exists so a caller that is about to fan out N parallel shards
    can verify the base/build ONCE, before starting any of them, instead of each shard
    independently discovering a mismatch on its OWN first request (r3 review: a real
    parallel run sent one request PER SHARD before the whole thing aborted, scaling
    the very blast radius this ticket exists to shrink). The per-shard check inside
    run_replicate stays in place as defence in depth -- this is a fast-fail gate in
    FRONT of it, not a replacement.

    Uses the first CORPUS row's own text: a synthetic/placeholder question risks
    behaving differently server-side than a real investigation, and the first row is
    exactly what the first real replicate would ask anyway.
    """
    if not CORPUS:
        sys.exit("check-only: the supplied corpus is empty, nothing to probe with")
    row = CORPUS[0]
    print(f"[corpus] check-only: probing with {row['id']!r}", flush=True)
    try:
        status, response, _dt, _undecodable = post({"question": row["text"]})
    except ServedBuildMismatch as e:
        sys.exit(str(e))
    if not contract.is_success_status(status):
        failure = (response or {}).get("failure", {})
        sys.exit(f"check-only: request failed, http={status} code={failure.get('code')} "
                  f"-- cannot verify the base/build; refusing to start any shard")
    print("[corpus] check-only: base and build verified, proceeding", flush=True)


def main():
    global EXPECTED_BUILD
    try:
        require_base()
    except MissingCorpusBase as e:
        sys.exit(str(e))
    ids, EXPECTED_BUILD, want_check_only = _parse_argv(sys.argv[1:])
    if want_check_only:
        check_only()
        return
    which = ids or None
    rows = []
    total = 0
    try:
        for row in CORPUS:
            if which and row["id"] not in which:
                continue
            for rep in range(1, 4):
                print(f"=== {row['id']} rep{rep} ===", flush=True)
                r = run_replicate(row["id"], row["text"], rep)
                rows.append(r)
                total += r["attempts"]
                print(f"  -> attempts={r['attempts']} cumulative_total={total} chain={r['chain']}", flush=True)
    except ServedBuildMismatch as e:
        sys.exit(str(e))
    with open(OUTDIR / "summary.json", "w") as f:
        json.dump(rows, f, indent=2)
    print(f"DONE {len(rows)} replicate-rows, {total} total attempts -> {OUTDIR}/summary.json")


if __name__ == "__main__":
    main()
