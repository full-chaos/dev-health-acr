"""Pins for CHAOS-5664: stop the chain on a turn whose offers are all
unredeemable by the harness's OWN redemption rules, instead of a bare
re-ask.

Root cause: a `named_subject` question whose declared subject string
resolves to no real entity offers a kind list that never contains the
declared `requested_kind`, and subject candidates whose kind never
matches `anchor_kind`. Both refusals (`:355` no kind option matches
requested_kind; `:402` no subject candidate matches anchor_kind) already
fire correctly and leave `receipts` empty for that turn -- but with no
window need offered either, the turn was then re-asked BARE, the engine
answered with the SAME two refusals next turn, and the row spent 5 turns
(MAX_TURNS) reaching a terminal it was already unable to reach on turn 2.

The fix does not change WHAT is unanswerable -- both refusals, and the
bare-re-ask branch for every OTHER empty-receipts cause (a need already
answered, nothing new offered), are untouched. It only stops the chain the turn
either refusal fires with no other offer to redeem, records
`no_redeemable_offer_flag`/`stop_reason` on the row (named for WHICH
axis -- kind, subject, or both -- actually failed that turn, and
surfaced through `run_shard.detail_for`, the field set the real corpus
path and `reclassify_deadlines.py` both actually emit, not just the
intermediate `run_replicate` result), and leaves
`final_payload_status`/`chain` exactly what the engine's own last turn
said -- so `merge_corpus.py`'s classify() buckets the row identically
(`clarification_needed`, via the same bare `clarification_required` value
CLARIFICATION_VALUES already recognizes), only fewer turns were spent
reaching it.

Every case below drives `harness.run_replicate` directly against a
scripted `post()` stub -- no real HTTP, no rig. `harness.OUTDIR` is
redirected to a tempdir so this never writes into the real
`scripts/corpus/replicate/` tree.
"""
import sys
import tempfile
from pathlib import Path

HERE = Path(__file__).parent
sys.path.insert(0, str(HERE))
import harness  # noqa: E402


class _FindingsError(Exception):
    pass


def _require(cond, msg):
    if not cond:
        raise _FindingsError(msg)


def _stub_post(turns):
    """Returns a `post(body)`-shaped stub that yields the next scripted
    (status, result) pair each call, and a `calls` list recording every
    body sent -- so a test can assert exactly how many requests went out,
    and with what, never just the final row shape."""
    calls = []
    remaining = list(turns)

    def _post(body):
        calls.append(body)
        _require(remaining, f"post() called more times than scripted: {body!r}")
        status, result = remaining.pop(0)
        return status, {"result": result}, 0.1, False

    return _post, calls


def _run(turns, want_kind="project", anchor_kind="project"):
    stub, calls = _stub_post(turns)
    real_post = harness.post
    real_outdir = harness.OUTDIR
    real_requested = dict(harness.REQUESTED_KIND)
    real_anchor = dict(harness.ANCHOR_KIND)
    with tempfile.TemporaryDirectory() as tmp:
        harness.post = stub
        harness.OUTDIR = Path(tmp)
        harness.REQUESTED_KIND = {"fixture-row": want_kind}
        harness.ANCHOR_KIND = {"fixture-row": anchor_kind}
        try:
            row = harness.run_replicate("fixture-row", "fixture question text", 1)
        finally:
            harness.post = real_post
            harness.OUTDIR = real_outdir
            harness.REQUESTED_KIND = real_requested
            harness.ANCHOR_KIND = real_anchor
    return row, calls


# T1: window offered (redeemable), no kind offer, no subject candidates --
# receipts non-empty (window), chain continues normally.
_T1_REDEEMABLE_WINDOW = (200, {
    "result_id": "r1", "status": "clarification_required",
    "structure_needs": {"window_options": [{"relative_id": "trailing_90d", "receipt_id": "w1"}]},
})

# T2 (the unredeemable turn): NO window need this turn, a kind list that
# never contains the declared requested_kind ("project"), and subject
# candidates whose kind never matches anchor_kind ("project") either --
# both harness.py:355/:402 refusals fire, receipts end up empty.
_T2_UNREDEEMABLE = (200, {
    "result_id": "r2", "status": "clarification_required",
    "structure_needs": {"kind_options": [{"kind": "repository", "receipt_id": "k1"},
                                          {"kind": "team", "receipt_id": "k2"}]},
    "subject_resolution": {"candidates": [
        {"receipt_id": "c1", "subject": {"kind": "ci_pipeline_run"}},
        {"receipt_id": "c2", "subject": {"kind": "pull_request"}},
    ]},
})

# T2' control variant: neither a kind nor a subject offer at all this turn
# (both needs already answered on an earlier turn, nothing new to redeem) --
# the PRE-EXISTING bare-re-ask cause, must be UNCHANGED by this fix.
_T2_ALREADY_ANSWERED = (200, {
    "result_id": "r2", "status": "clarification_required",
})

_T3_TERMINAL_NO_MATCH = (200, {"result_id": "r3", "status": "no_match"})


def test_unredeemable_turn_stops_the_chain_not_a_bare_reask():
    row, calls = _run([_T1_REDEEMABLE_WINDOW, _T2_UNREDEEMABLE])
    _require(len(calls) == 2, f"expected exactly 2 requests (turn count 5 -> 2), got {len(calls)}: {calls}")
    _require(row["chain"] == "t1=clarification_required -> t2=clarification_required",
              f"chain: {row['chain']!r}")
    _require(row["no_redeemable_offer_flag"] is True, row)
    _require(row["stop_reason"] == "no_redeemable_offer_for_declared_kind_and_subject", row)
    _require(row["wrong_kind_flag"] is True, "the :355 refusal must still fire and be reported")
    _require(row["wrong_subject_flag"] is True, "the :402 refusal must still fire and be reported")
    # Bucket-preserving: final_payload_status is the engine's own turn-2 status,
    # unmodified, matching bare CLARIFICATION_VALUES the same way MAX_TURNS
    # exhaustion's "clarification_required(max_turns_exhausted)" already does --
    # merge_corpus.classify() reads either spelling into clarification_needed.
    _require(row["final_payload_status"] == "clarification_required", row)
    _require(row["attempts"] == 2, f"2 turns, 1 attempt each: {row['attempts']}")


def test_the_bare_reask_path_is_gone_for_this_class_third_request_never_sent():
    """RED CONTROL: the pre-fix behaviour would have sent a 3rd, 4th, and 5th
    identical request (the bare re-ask, MAX_TURNS=5) after the SAME turn-2
    shape. Prove no 3rd request is ever sent -- not just that the returned
    row LOOKS stopped, but that post() itself was never called again."""
    _, calls = _run([_T1_REDEEMABLE_WINDOW, _T2_UNREDEEMABLE, _T3_TERMINAL_NO_MATCH,
                      _T3_TERMINAL_NO_MATCH, _T3_TERMINAL_NO_MATCH])
    _require(len(calls) == 2,
              f"a 3rd request was sent -- the bare re-ask path fired when it must not have: {calls}")


def test_only_wrong_kind_alone_without_wrong_subject_also_stops():
    """A single unmet redemption rule with nothing else offered is still
    fully unredeemable -- the stop condition is `wrong_kind OR wrong_subject`,
    not `AND`. Kind mismatches (subject_resolution absent this turn, so
    wrong_subject never even evaluates -- see `elif cands:` in
    update_memory_and_build_receipts, which is False on an empty list)."""
    t2 = (200, {"result_id": "r2", "status": "clarification_required",
                "structure_needs": {"kind_options": [{"kind": "repository", "receipt_id": "k1"}]}})
    row, calls = _run([_T1_REDEEMABLE_WINDOW, t2])
    _require(len(calls) == 2, calls)
    _require(row["no_redeemable_offer_flag"] is True, row)
    _require(row["wrong_kind_flag"] is True, row)
    _require(row["wrong_subject_flag"] is False, "no subject offer this turn -- must not be flagged")


def test_already_answered_empty_receipts_still_bare_reasks_unchanged():
    """GREEN CONTROL: the PRE-EXISTING bare-re-ask cause (every need already
    answered, nothing new offered this turn -- neither refusal fires) must
    be completely unaffected by this fix. The chain continues past turn 2
    with a bare `{"question": ...}` re-ask, exactly as before."""
    row, calls = _run([_T1_REDEEMABLE_WINDOW, _T2_ALREADY_ANSWERED, _T3_TERMINAL_NO_MATCH])
    _require(len(calls) == 3, f"the bare re-ask for THIS cause must still happen: {calls}")
    _require(calls[2] == {"question": "fixture question text"},
              f"turn 3 must be a bare re-ask (no receipts survive an already-answered turn): {calls[2]}")
    _require(row["no_redeemable_offer_flag"] is False, row)
    _require(row["stop_reason"] is None, row)
    _require(row["chain"] == "t1=clarification_required -> t2=clarification_required -> t3=no_match", row)


def test_a_candidate_of_the_declared_kind_still_redeems_and_continues():
    """Control: when a candidate DOES carry the declared anchor_kind, the
    subject receipt is redeemable -- this must never trip the new stop,
    even with no kind offer that turn."""
    t2 = (200, {
        "result_id": "r2", "status": "clarification_required",
        "subject_resolution": {"candidates": [{"receipt_id": "c1", "subject": {"kind": "project"}}]},
    })
    row, calls = _run([_T1_REDEEMABLE_WINDOW, t2, _T3_TERMINAL_NO_MATCH])
    _require(len(calls) == 3, calls)
    _require(calls[2] == {"question": "fixture question text", "priorSubjectReceipts": [
        {"result_id": "r2", "receipt_id": "c1"}]}, calls[2])
    _require(row["no_redeemable_offer_flag"] is False, row)


def test_stop_reason_names_the_axis_that_actually_failed():
    """`stop_reason` must name WHICH redemption rule(s) failed, never a fixed
    string regardless of cause -- a subject-only mismatch (no kind offer at
    all that turn) must not read "...declared_kind"."""
    kind_only = (200, {"result_id": "r2", "status": "clarification_required",
                        "structure_needs": {"kind_options": [{"kind": "repository", "receipt_id": "k1"}]}})
    subject_only = (200, {"result_id": "r2", "status": "clarification_required",
                           "subject_resolution": {"candidates": [
                               {"receipt_id": "c1", "subject": {"kind": "repository"}},
                               {"receipt_id": "c2", "subject": {"kind": "team"}}]}})
    both = _T2_UNREDEEMABLE

    row, _ = _run([_T1_REDEEMABLE_WINDOW, kind_only])
    _require(row["wrong_kind_flag"] is True and row["wrong_subject_flag"] is False, row)
    _require(row["stop_reason"] == "no_redeemable_offer_for_declared_kind", row)

    row, _ = _run([_T1_REDEEMABLE_WINDOW, subject_only])
    _require(row["wrong_kind_flag"] is False and row["wrong_subject_flag"] is True, row)
    _require(row["stop_reason"] == "no_redeemable_offer_for_declared_subject", row)

    row, _ = _run([_T1_REDEEMABLE_WINDOW, both])
    _require(row["wrong_kind_flag"] is True and row["wrong_subject_flag"] is True, row)
    _require(row["stop_reason"] == "no_redeemable_offer_for_declared_kind_and_subject", row)


def test_detail_for_surfaces_the_new_fields():
    """`run_shard.detail_for` is the field set the real corpus path (and
    reclassify_deadlines.py) actually emits -- run_replicate's row alone is
    never what lands in a shard-summary.json. Both new fields must survive
    that translation, not just exist on the intermediate run_replicate
    result."""
    import run_shard as RS

    stub, calls = _stub_post([_T1_REDEEMABLE_WINDOW, _T2_UNREDEEMABLE])
    real_post = harness.post
    real_outdir = harness.OUTDIR
    real_requested = dict(harness.REQUESTED_KIND)
    real_anchor = dict(harness.ANCHOR_KIND)
    with tempfile.TemporaryDirectory() as tmp:
        outdir = Path(tmp)
        harness.post = stub
        harness.OUTDIR = outdir
        harness.REQUESTED_KIND = {"fixture-row": "project"}
        harness.ANCHOR_KIND = {"fixture-row": "project"}
        try:
            r = harness.run_replicate("fixture-row", "fixture question text", 1)
            detail = RS.detail_for(outdir, "fixture-row", {"note": "", "family": None}, r, 0.5, 1)
        finally:
            harness.post = real_post
            harness.OUTDIR = real_outdir
            harness.REQUESTED_KIND = real_requested
            harness.ANCHOR_KIND = real_anchor
    _require(detail["no_redeemable_offer_flag"] is True, detail)
    _require(detail["stop_reason"] == "no_redeemable_offer_for_declared_kind_and_subject", detail)


def main():
    tests = [v for k, v in sorted(globals().items()) if k.startswith("test_") and callable(v)]
    for test in tests:
        test()
        print(f"PASS: {test.__name__}")
    print(f"PASS: {len(tests)} CHAOS-5664 findings pinned")


if __name__ == "__main__":
    main()
