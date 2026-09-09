"""RED-FIRST pins for CHAOS-5380: the attempt sequence rides BESIDE the terminal bucket.

The row's terminal status is not evidence about what happened during the row. A 504 the
harness retried into a 200 leaves no trace in `final_http`, which is how the 09-06 smoke
report printed ZERO deadline failures against five logged 504s (regression-diagnosis doc
§4 O4, snapshot :172). Two counters already exist for that -- `attempt_upstream_504_n`
and `attempt_overrun_413_n` -- and they close the 504/413 half. These pins close the rest:

  * the sequence itself, per row, beside the bucket, not two scalars summarising it;
  * 422 and 400, which §6 (:214) names in the eight sequential non-200 attempts and which
    no per-row counter has ever carried;
  * ONE classifier, so the two modules that count attempts cannot disagree;
  * a merge that REFUSES rather than summing an absent measurement to zero (§5 :200).

Where a pin could pass for the wrong reason it ships with a negative control.
"""
import json
import os
import subprocess
import sys
import tempfile
from pathlib import Path

HERE = Path(__file__).parent
sys.path.insert(0, str(HERE))

import attempt_classes as AC     # noqa: E402
import engine_failures as EF     # noqa: E402
import merge_corpus as MC        # noqa: E402
import reclassify_deadlines as RD  # noqa: E402
import run_shard as RS           # noqa: E402


def _attempt(status, failure=None, dt=1.0, result=None):
    response = {}
    if failure is not None:
        response["failure"] = failure
    if result is not None:
        response["result"] = result
    return {"request": {}, "status": status, "dt": dt, "response": response}


def _write_run(tmp, attempts, qid="q-a", rep=1, turn=1):
    """Write one row's attempts as the harness writes them: q-rep<R>-t<T>-a<A>.json.

    `turn` defaults to 1 for the single-turn fixtures. It is a PARAMETER because every
    fixture in the first version of this file hard-coded t1, which is exactly how the
    turns-counted-as-retries defect passed every pin: the fixtures agreed with the bug.
    Use _write_turns for anything that must exercise more than one turn.
    """
    out = Path(tmp) / "shard-00"
    (out / "replicate").mkdir(parents=True, exist_ok=True)
    for i, a in enumerate(attempts, start=1):
        (out / "replicate" / f"{qid}-rep{rep}-t{turn}-a{i}.json").write_text(json.dumps(a))
    return out / "replicate"


def _write_turns(tmp, turns, qid="q-a", rep=1):
    """turns = {turn_number: [attempt, ...]}. THE shape the harness really writes."""
    out = Path(tmp) / "shard-00"
    (out / "replicate").mkdir(parents=True, exist_ok=True)
    for t, attempts in turns.items():
        for i, a in enumerate(attempts, start=1):
            (out / "replicate" / f"{qid}-rep{rep}-t{t}-a{i}.json").write_text(json.dumps(a))
    return out / "replicate"


def _contract_violation():
    """The LIVE shape: the consumer returns 502 while the upstream call returned 200 --
    acr answered and its BODY failed the investigation contract. Copied field for field
    from a real attempt artefact of the 2026-09-09 private-pair replicate."""
    return _attempt(502, failure={
        "code": "acr_contract_violation",
        "message": "ACR returned a result that does not satisfy the investigation contract.",
        "httpStatus": 200,
        "details": [" must NOT have additional properties",
                    "/completeness must NOT have additional properties"],
        "retryable": False,
    })


def test_row_carries_the_attempt_sequence_not_only_a_count():
    """ACCEPTANCE (a). `attempts` says 3; it cannot say 504 then 422 then served.

    The list is ordered by RECORDED SEQUENCE (attempt_order), never by path -- the
    ordering defect that put t10 before t9 is exactly the one that would silently
    reverse this list.
    """
    with tempfile.TemporaryDirectory() as tmp:
        out = _write_run(tmp, [
            _attempt(504),
            _attempt(200, failure={"httpStatus": 422, "code": "acr_rejected_request"}),
            _attempt(200, result={"status": "complete"}),
        ])
        diag = RS.attempt_diagnostics(out, "q-a", 1)

    seq = diag["attempt_outcomes"]
    assert [a["class"] for a in seq] == ["upstream_504", "unprocessable_422", "ok_200"], seq
    assert [a["attempt"] for a in seq] == [1, 2, 3], seq
    assert diag["attempts_total"] == 3, diag
    assert diag["attempts_retried"] == 2, diag
    # NEGATIVE CONTROL: a one-attempt row must not produce the same shape.
    with tempfile.TemporaryDirectory() as tmp:
        out = _write_run(tmp, [_attempt(200, result={"status": "complete"})])
        solo = RS.attempt_diagnostics(out, "q-a", 1)
    assert [a["class"] for a in solo["attempt_outcomes"]] == ["ok_200"], solo
    assert solo["attempts_retried"] == 0, "an unretried row must report an EXPLICIT zero"


def test_every_class_is_counted_with_explicit_zeros():
    """422 and 400 have never been counted per row -- §6 (:214) names four 422s and one
    400 among the eight sequential non-200 attempts, and the row said nothing about any
    of them. All keys are always present: a missing key and a zero must not look alike."""
    with tempfile.TemporaryDirectory() as tmp:
        out = _write_run(tmp, [
            _attempt(200, failure={"httpStatus": 422, "code": "acr_rejected_request"}),
            _attempt(200, failure={"httpStatus": 400, "code": "acr_rejected_request"}),
            _attempt(200, failure={"httpStatus": 413, "measuredItems": 33, "maxItems": 30}),
            _attempt(504),
            _attempt(200, result={"status": "complete"}),
        ])
        diag = RS.attempt_diagnostics(out, "q-a", 1)

    counts = diag["attempt_class_n"]
    assert set(counts) == set(AC.CLASSES), "the class counter is not the closed vocabulary"
    assert counts["unprocessable_422"] == 1, counts
    assert counts["rejected_400"] == 1, counts
    assert counts["overrun_413"] == 1, counts
    assert counts["upstream_504"] == 1, counts
    assert counts["ok_200"] == 1, counts
    assert counts["other_5xx"] == 0, "an unobserved class must be an explicit zero"
    # The two frozen counters keep their names AND their meaning, derived from the same
    # walk so a future edit cannot move one without the other.
    assert diag["attempt_upstream_504_n"] == 1, diag
    assert diag["attempt_overrun_413_n"] == 1, diag


def test_one_classifier_serves_both_counters():
    """Two modules counted attempts with two ladders. Every instrument defect on this
    seam has been two counters disagreeing; the fix is one classifier, not a third.

    Asserted on the AST, not on source text (codex r2 called the text form P3-strength,
    and it was right: `"attempt_classes" in src` passes on a comment, on an import that
    nothing calls, and on the word appearing in a docstring). This walks for a real
    CALL through the module and for the absence of a second ladder.
    """
    import ast as _ast

    def calls_into(path, module):
        tree = _ast.parse((HERE / path).read_text())
        found = set()
        for node in _ast.walk(tree):
            if isinstance(node, _ast.Call) and isinstance(node.func, _ast.Attribute):
                base = node.func.value
                if isinstance(base, _ast.Name) and base.id == module:
                    found.add(node.func.attr)
        return found

    shard = calls_into("run_shard.py", "attempt_classes")
    assert {"classify", "outcome", "zero_counts"} <= shard, \
        f"run_shard does not CALL the shared classifier: {shard}"
    failures = calls_into("engine_failures.py", "attempt_classes")
    assert "legacy_engine_failure_kind" in failures, \
        f"engine_failures does not CALL the shared ladder: {failures}"

    # NEGATIVE CONTROL: the same walk over a module that genuinely does not use it
    # must come back empty, so the assertion is not satisfied by the walk itself.
    assert calls_into("attempt_order.py", "attempt_classes") == set()

    # And no second ladder: neither consumer may compare a status to the sentinel
    # values the classifier owns.
    for path in ("run_shard.py", "engine_failures.py"):
        tree = _ast.parse((HERE / path).read_text())
        literals = {n.value for n in _ast.walk(tree)
                    if isinstance(n, _ast.Constant) and isinstance(n.value, int)}
        assert not ({502, 503, 504, 413, 422} & literals), \
            f"{path} carries HTTP status literals -- a second ladder is growing back"


ORIGINAL_LADDER_SHA = "3822c1d7d6e1ecc60c3dc31d6c55ebec49625340"


def _original_scan_kind():
    """Extract the ORIGINAL ladder from git and expose it as a callable.

    Not transcribed into this test either: the reference is `engine_failures.scan` as it
    stood on the merge base, read out of git at test time and exercised through its own
    public entry point on real files. A reference the test types out by hand is the same
    mistake at one remove -- round 3's whole finding was a hand-reconstruction that agreed
    with the original everywhere except one column.
    """
    import subprocess, tempfile, importlib.util, sys, json
    src = subprocess.run(["git", "-C", str(HERE), "show",
                          f"{ORIGINAL_LADDER_SHA}:scripts/corpus/engine_failures.py"],
                         capture_output=True, text=True, check=True).stdout
    tmpdir = tempfile.mkdtemp()
    mod_path = Path(tmpdir) / "engine_failures_original.py"
    mod_path.write_text(src)
    sys.path.insert(0, str(HERE))          # for its `validators` import
    spec = importlib.util.spec_from_file_location("engine_failures_original", mod_path)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)

    def kind_of(attempt):
        with tempfile.TemporaryDirectory() as run:
            out = Path(run) / "shard-00" / "replicate"
            out.mkdir(parents=True)
            (out / "q-a-rep1-t1-a1.json").write_text(json.dumps(attempt))
            recs = mod.scan(Path(run))
        return recs[0]["kind"] if recs else None

    return kind_of


def test_legacy_ladder_is_equivalent_over_the_whole_shape_space():
    """OPTION (a), and the proof that it worked. Every cell of the enumerated input shape
    space is compared against the ORIGINAL scan, read out of git and run on real files.

    Round 3 found 24 differing cells out of 364, every one of them in the single column
    `failure object present, httpStatus absent`, and not one of them covered by a fixture.
    The lesson was not "add those 24 fixtures" -- it was that a hand-reconstruction cannot
    be spot-checked into correctness. So the assertion is TOTAL: zero differences, over the
    cross-product, against the original itself.
    """
    original = _original_scan_kind()
    status_bands = [None, 0, 1, 99, 200, 201, 204, 302, 399, 400, 404, 413, 422,
                    500, 502, 503, 504, 599, 999]
    failure_shapes = [
        None,                                            # no failure key at all
        {},                                              # present but EMPTY -- falsy, and
                                                         # the original's `or {}` sends it
                                                         # down the status-only branch
        {"code": "provider_error"},                      # present, NO httpStatus
        {"code": "acr_contract_violation"},
    ] + [{"code": "x", "httpStatus": up} for up in (200, 400, 413, 422, 500, 502, 503, 504, 599)]

    differences, cells = [], 0
    for status in status_bands:
        for failure in failure_shapes:
            attempt = {"request": {}, "dt": 1.0, "response": {}}
            if status is not None:
                attempt["status"] = status
            if failure is not None:
                attempt["response"]["failure"] = failure
            cells += 1
            want, got = original(attempt), AC.legacy_engine_failure_kind(attempt)
            if want != got:
                differences.append((status, failure, want, got))

    assert cells == len(status_bands) * len(failure_shapes)
    assert not differences, (
        f"{len(differences)} of {cells} cells differ from the ORIGINAL ladder; "
        f"first five: {differences[:5]}")

    # NEGATIVE CONTROL: the comparison must be capable of failing. A deliberately wrong
    # ladder over the same cells has to produce differences, or this pin proves nothing.
    wrong = 0
    for status in status_bands:
        for failure in failure_shapes:
            attempt = {"request": {}, "dt": 1.0, "response": {}}
            if status is not None:
                attempt["status"] = status
            if failure is not None:
                attempt["response"]["failure"] = failure
            http, upstream = AC._statuses(attempt)
            # round 3's exact defect: branch on `upstream is None` instead of presence
            if upstream is None:
                bad = (f"UPSTREAM_{http}" if http in (502, 503, 504)
                       else f"OTHER_{http}") if isinstance(http, int) and http >= 400 else None
            else:
                bad = f"OTHER_{upstream}"
            if bad != original(attempt):
                wrong += 1
    assert wrong >= 24, f"the control found only {wrong} differences; it is not discriminating"


def test_legacy_engine_failure_kinds_are_unchanged():
    """POSITIVE CONTROL for the frozen artefact. `post_hoc_attempt_classes` is keyed by
    engine_failures' own kind strings and lives in the verdict of record, so the shared
    classifier must reproduce the OLD ladder character for character -- including its
    two inconsistencies (a bare 502/503 status is UPSTREAM_*, a PARSED 502 is OTHER_502).
    Every branch of the old ladder is represented."""
    cases = [
        (_attempt(504), "UPSTREAM_504"),
        (_attempt(502), "UPSTREAM_502"),
        (_attempt(503), "UPSTREAM_503"),
        (_attempt(500), "OTHER_500"),
        (_attempt(400), "OTHER_400"),
        (_attempt(200, failure={"httpStatus": 500}), "ENGINE_INVALID_RESULT"),
        (_attempt(200, failure={"httpStatus": 413}), "RIG_CEILING_413"),
        (_attempt(200, failure={"httpStatus": 504}), "UPSTREAM_504"),
        (_attempt(504, failure={"httpStatus": 422}), "UPSTREAM_504"),
        (_attempt(200, failure={"httpStatus": 422}), "OTHER_422"),
        (_attempt(200, failure={"httpStatus": 502}), "OTHER_502"),
    ]
    for attempt, want in cases:
        got = AC.legacy_engine_failure_kind(attempt)
        assert got == want, f"{attempt} -> {got}, want {want}"
    # NEGATIVE CONTROL: a served attempt is not a failure and has no legacy kind.
    assert AC.legacy_engine_failure_kind(_attempt(200, result={"status": "complete"})) is None


def test_scan_still_reports_the_same_kinds_through_the_shared_classifier():
    """The class proof for the pin above: engine_failures.scan, end to end on files."""
    with tempfile.TemporaryDirectory() as tmp:
        out = _write_run(tmp, [
            _attempt(504),
            _attempt(200, failure={"httpStatus": 413, "measuredItems": 33, "maxItems": 30}),
            _attempt(200, failure={"httpStatus": 500}),
            _attempt(200, failure={"httpStatus": 422}),
            _attempt(200, result={"status": "complete"}),
        ])
        kinds = sorted(r["kind"] for r in EF.scan(out.parent))
    assert kinds == ["ENGINE_INVALID_RESULT", "OTHER_422", "RIG_CEILING_413", "UPSTREAM_504"], kinds


def test_merge_refuses_to_report_an_absent_measurement_as_zero():
    """REPRODUCES THE 09-06 FALSE ZERO. Rows written by an instrument that never ran the
    attempt walk carry no counters; summing them prints a confident 0, which is exactly
    the report that said zero deadline failures against five logged 504s. §5 (:200):
    report incomplete capture rather than treat an absent event as a measured zero."""
    unmeasured = [{"corpus_id": "q-a", "attempts": 3}, {"corpus_id": "q-b", "attempts": 1}]
    totals = MC.attempt_class_totals(unmeasured)
    assert totals["attempt_classes_unavailable"] == 2, totals
    assert totals.get("attempt_class_totals") is None, \
        "a total was published for rows that were never measured"

    # POSITIVE CONTROL: measured rows DO produce totals, and the unavailable count is an
    # explicit zero -- so the refusal above is a measurement, not a constant.
    # A "measured" row must now ALSO have reconciled its walk against the harness's own
    # attempt count (codex r2 P1) -- a complete class table alone no longer earns the
    # right to be summed.
    measured = [
        {"corpus_id": "q-a", "attempts_reconciled": True,
         "attempt_class_n": dict(AC.zero_counts(), upstream_504=2, ok_200=1)},
        {"corpus_id": "q-b", "attempts_reconciled": True,
         "attempt_class_n": dict(AC.zero_counts(), unprocessable_422=1, ok_200=1)},
    ]
    totals = MC.attempt_class_totals(measured)
    assert totals["attempt_classes_unavailable"] == 0, totals
    assert totals["attempt_class_totals"]["upstream_504"] == 2, totals
    assert totals["attempt_class_totals"]["unprocessable_422"] == 1, totals
    assert totals["rows_with"]["upstream_504"] == 1, totals
    assert totals["attempt_class_totals"]["other_4xx"] == 0, "explicit zero expected"

    # A row from an instrument that predates the reconciliation check carries no flag at
    # all. That is not a measured row either -- absence of the check is not a pass.
    noflag = MC.attempt_class_totals([{"corpus_id": "noflag",
                                       "attempt_class_n": AC.zero_counts()}])
    assert noflag["attempt_classes_unavailable"] == 1, noflag
    assert noflag["attempt_class_totals"] is None, noflag

    # MIXED: one measured, one not. A partial run must not silently under-report.
    mixed = MC.attempt_class_totals(measured + unmeasured)
    assert mixed["attempt_classes_unavailable"] == 2, mixed

    # codex r1 P1, reproduced live: a row carrying a SYNTACTICALLY VALID PARTIAL dict was
    # accepted, and every class it omitted was published as a zero -- the false zero this
    # function exists to refuse, arriving through the one shape "isinstance(..., dict)"
    # does not cover. The key set must be EXACTLY the closed vocabulary.
    partial = MC.attempt_class_totals([{"corpus_id": "partial", "attempts_reconciled": True,
                                        "attempt_class_n": {"upstream_504": 1}}])
    assert partial["attempt_classes_unavailable"] == 1, partial
    assert partial["attempt_class_totals"] is None, \
        "a partial row published totals -- the omitted classes read as measured zeros"
    assert partial["attempt_classes_unavailable_ids"] == ["partial"], partial
    # ...and an EXTRA key is refused too: a row typed by some other vocabulary is not a
    # row this one can sum, in either direction.
    extra = MC.attempt_class_totals([{"corpus_id": "extra", "attempts_reconciled": True,
                                      "attempt_class_n": dict(AC.zero_counts(), invented_class=3)}])
    assert extra["attempt_classes_unavailable"] == 1, extra
    assert extra["attempt_class_totals"] is None, extra


def test_merged_row_carries_the_sequence_beside_the_bucket():
    """ACCEPTANCE (a), at the artefact boundary: run the REAL merge over a real shard and
    read the verdict it wrote. An earlier draft of this pin asserted on a dict literal it
    had built itself, which is the inert-control shape -- an instrument must READ the
    result, never RESTATE the expectation."""
    from corpus import CORPUS
    qid = "example-serve-named-project"
    with tempfile.TemporaryDirectory() as tmp:
        indir = Path(tmp) / "seq"
        shard = indir / "shard-00"
        (shard / "replicate").mkdir(parents=True)
        rows = []
        # Every corpus id runs: the merge REFUSES a partial run as inadmissible
        # evidence, which is itself the behaviour that stops a short run being
        # reported as a complete one.
        for entry in CORPUS:
            cid = entry["id"]
            attempts = ([_attempt(504)] if cid == qid else []) + [
                _attempt(200, result={"status": "complete"})]
            for i, a in enumerate(attempts, start=1):
                (shard / "replicate" / f"{cid}-rep1-t1-a{i}.json").write_text(json.dumps(a))
            row = {"corpus_id": cid, "family": entry.get("family"), "section_note": "",
                   "final_http": 200, "final_payload_status": "complete",
                   "chain": "t1=complete", "attempts": len(attempts),
                   "wrong_kind_flag": False, "wrong_subject_flag": False,
                   "subject_kind_mismatch_flag": False, "wall_seconds": 1.0,
                   "claimed_facts_n": 1, "failure_code": None}
            row.update(RS.attempt_diagnostics(shard / "replicate", cid, 1))
            rows.append(row)
        (shard / "shard-summary.json").write_text(json.dumps(
            {"shard": 0, "planned_ids": [r["corpus_id"] for r in rows], "rows": rows,
             "total_wall_seconds": 1.0, "started_unix": 1, "finished_unix": 2}))
        outfile = Path(tmp) / "verdict.json"
        proc = subprocess.run(
            [sys.executable, str(HERE / "merge_corpus.py"), "--shape", "sequential",
             "--in", str(indir), "--out", str(outfile)],
            capture_output=True, text=True, cwd=str(HERE),
            env={**os.environ, "PYTHONPATH": str(HERE / "testdata_corpus")})
        assert proc.returncode == 0, proc.stdout + proc.stderr
        verdict = json.loads(outfile.read_text())

    merged = next(r for r in verdict["rows"] if r["corpus_id"] == qid)
    # THE ACCEPTANCE: the terminal bucket and the attempt sequence, side by side.
    assert merged["bucket"] == "served_with_data", merged["bucket"]
    assert [a["class"] for a in merged["attempt_outcomes"]] == ["upstream_504", "ok_200"], merged
    assert merged["attempts_total"] == 2 and merged["attempts_retried"] == 1, merged
    assert merged["attempt_class_n"]["upstream_504"] == 1, merged
    # ...and the run-level totals the diagnostics block publishes agree with the row.
    diagnostics = verdict["rig_diagnostics"]
    assert diagnostics["attempt_classes_unavailable"] == 0, diagnostics
    assert diagnostics["attempt_class_totals"]["upstream_504"] == 1, diagnostics
    # NEGATIVE CONTROL: reading the terminal alone reports NOTHING about that 504 --
    # the exact statistic the 09-06 smoke report published as zero.
    assert merged["final_http"] == 200 and merged["failure_code"] is None, merged


def test_an_unreadable_attempt_is_recorded_never_skipped():
    """A scanner that drops what it cannot parse reports a smaller, cleaner run than the
    one that happened -- the silent-skip shape that has shown up here as a dropped
    battery arm and as a needle matching zero occurrences. The class is TOTAL, so an
    unreadable file lands in `unreadable` rather than nowhere."""
    with tempfile.TemporaryDirectory() as tmp:
        out = _write_run(tmp, [_attempt(504), _attempt(200, result={"status": "complete"})])
        (out / "q-a-rep1-t1-a3.json").write_text("{ this is not json")
        diag = RS.attempt_diagnostics(out, "q-a", 1)

    assert diag["attempts_total"] == 3, diag
    assert diag["attempt_class_n"]["unreadable"] == 1, diag["attempt_class_n"]
    bad = [a for a in diag["attempt_outcomes"] if a["class"] == "unreadable"]
    assert len(bad) == 1 and bad[0]["attempt"] == 3, bad
    assert bad[0].get("detail"), "an unreadable artefact must carry WHY it did not parse"
    # NEGATIVE CONTROL: with the bad file removed the class is an explicit zero, so the
    # count above measures the file rather than restating the expectation.
    with tempfile.TemporaryDirectory() as tmp:
        out = _write_run(tmp, [_attempt(504), _attempt(200, result={"status": "complete"})])
        clean = RS.attempt_diagnostics(out, "q-a", 1)
    assert clean["attempt_class_n"]["unreadable"] == 0, clean["attempt_class_n"]


def test_the_sequence_comes_from_the_filename_not_the_listing_position():
    """Lexicographic order puts t10 before t9, and that defect has already selected the
    wrong terminal attempt twice on this instrument. The per-attempt list carries the
    RECORDED turn/attempt, so a reader can never mistake listing position for sequence."""
    with tempfile.TemporaryDirectory() as tmp:
        out = Path(tmp) / "shard-00" / "replicate"
        out.mkdir(parents=True)
        # written t9 last, and named so that a bare sort would put t10 first
        (out / "q-a-rep1-t10-a1.json").write_text(json.dumps(_attempt(504)))
        (out / "q-a-rep1-t9-a1.json").write_text(
            json.dumps(_attempt(200, result={"status": "complete"})))
        diag = RS.attempt_diagnostics(out, "q-a", 1)

    turns = [a["turn"] for a in diag["attempt_outcomes"]]
    assert turns == [9, 10], f"t10 sorted ahead of t9: {turns}"
    classes = [a["class"] for a in diag["attempt_outcomes"]]
    assert classes == ["ok_200", "upstream_504"], classes
    # the per-attempt index is the FILENAME's, never the position in the list
    assert all(a["attempt"] == 1 for a in diag["attempt_outcomes"]), diag["attempt_outcomes"]


def test_a_follow_up_TURN_is_not_a_retry():
    """LIVE REPRO, 2026-09-09 private-pair replicate. `attempts_retried` was len-1 over a
    walk that spans every TURN, so a two-turn conversation with one attempt each reported
    one retry when nothing was retried: 32 of 36 rows wrong, 69 reported against 6 real.

    The row that caught it: chain `t1=clarification_required -> t2=http502`, files
    `…-rep1-t1-a1.json` and `…-rep1-t2-a1.json`. Every pin fixture before this one put its
    attempts under t1, so the pins agreed with the bug -- the fixture, not the predicate,
    was what was wrong.
    """
    with tempfile.TemporaryDirectory() as tmp:
        out = _write_turns(tmp, {
            1: [_attempt(200, result={"status": "clarification_required"})],
            2: [_contract_violation()],
        })
        diag = RS.attempt_diagnostics(out, "q-a", 1)

    assert diag["attempts_total"] == 2, diag
    assert diag["attempts_retried"] == 0, \
        f"a follow-up TURN was counted as a retry: {diag['attempts_retried']}"
    assert [(a["turn"], a["attempt"]) for a in diag["attempt_outcomes"]] == [(1, 1), (2, 1)], diag

    # POSITIVE CONTROL: a real retry, inside ONE turn, still counts.
    with tempfile.TemporaryDirectory() as tmp:
        out = _write_turns(tmp, {1: [_attempt(504), _attempt(200, result={"status": "complete"})]})
        retried = RS.attempt_diagnostics(out, "q-a", 1)
    assert retried["attempts_retried"] == 1, retried

    # AND BOTH AT ONCE: two turns, the second of which retried once. Two attempts beyond
    # a first-in-turn would be 3 under the old rule; the true answer is 1.
    with tempfile.TemporaryDirectory() as tmp:
        out = _write_turns(tmp, {
            1: [_attempt(200, result={"status": "clarification_required"})],
            2: [_attempt(504), _attempt(200, result={"status": "complete"})],
        })
        both = RS.attempt_diagnostics(out, "q-a", 1)
    assert both["attempts_total"] == 3, both
    assert both["attempts_retried"] == 1, f"expected 1 real retry, got {both['attempts_retried']}"


def test_a_contract_violation_is_a_failure_not_ok_200():
    """LIVE REPRO, same replicate: 7 attempts classed `ok_200` while the consumer had
    returned 502. `classify` let the upstream status win whenever it was present, and on a
    contract violation the upstream IS 200 -- acr answered, its body failed the contract.

    That is the ticket's own defect class -- a failure hidden inside the reporting --
    reintroduced one level down, so it is pinned at the class: the ATTEMPT's own status
    decides whether it failed, and the upstream only refines why.
    """
    with tempfile.TemporaryDirectory() as tmp:
        out = _write_turns(tmp, {1: [_contract_violation()],
                                 2: [_attempt(200, result={"status": "complete"})]})
        diag = RS.attempt_diagnostics(out, "q-a", 1)

    classes = [a["class"] for a in diag["attempt_outcomes"]]
    assert classes == ["contract_violation", "ok_200"], classes
    assert diag["attempt_class_n"]["contract_violation"] == 1, diag["attempt_class_n"]
    assert diag["attempt_class_n"]["ok_200"] == 1, \
        "the failed attempt was counted as a success"

    # The predicate, directly, at the class rather than the one example.
    assert AC.failed(_contract_violation()) is True
    assert AC.failed(_attempt(200, result={"status": "complete"})) is False
    # NEGATIVE CONTROL: an upstream 200 must not rescue ANY non-2xx attempt, whatever the
    # code. A rule written to the one code that was observed would leave the class open.
    other = _attempt(503, failure={"code": "something_else", "httpStatus": 200})
    assert AC.classify(other) != "ok_200", AC.classify(other)
    assert AC.classify(other) == "upstream_503", AC.classify(other)
    # ...and a 2xx attempt that nonetheless carries a failure object is not ok either.
    weird = _attempt(200, failure={"code": "who_knows", "httpStatus": 200})
    assert AC.classify(weird) == "failure_under_2xx", AC.classify(weird)


def test_the_measured_shapes_of_the_live_run_all_classify_correctly():
    """The four (attempt_http, upstream, code) shapes MEASURED across all 105 attempts of
    the private-pair replicate, each with the class it must get. A table read off the run
    rather than off my expectations -- the instrument must READ the result."""
    cases = [
        (_attempt(200, result={"status": "complete"}), "ok_200", 91),
        (_contract_violation(), "contract_violation", 7),
        (_attempt(422, failure={"code": "acr_answer_rejected", "httpStatus": 422}), "unprocessable_422", 6),
        (_attempt(400, failure={"code": "acr_rejected_request", "httpStatus": 400}), "rejected_400", 1),
    ]
    for attempt, want, _n in cases:
        got = AC.classify(attempt)
        assert got == want, f"{attempt.get('status')} -> {got}, want {want}"
    assert sum(n for _, _, n in cases) == 105, "the shape table no longer covers the run"


def test_a_transport_failure_is_not_ok_200():
    """codex r2 P1, reproduced before the fix. `harness.post` returns `0, {"error": ...}`
    on ANY transport exception -- connection refused, DNS, read timeout -- so there is no
    status >= 400 and no failure envelope, and a rule written as "http >= 400 OR a failure
    object" fired on neither. A refused connection classified as `ok_200`.
    """
    transport = {"request": {}, "status": 0, "dt": 1.0,
                 "response": {"error": "<urlopen error [Errno 111] Connection refused>"}}
    assert AC.failed(transport) is True
    assert AC.classify(transport) == "transport_failure", AC.classify(transport)

    # THE CLASS, not the instance: every non-success status fails closed, including ones
    # this module has never seen and never enumerated.
    for status in (0, 1, 99, 599, 999):
        a = {"request": {}, "status": status, "dt": 1.0, "response": {}}
        assert AC.failed(a) is True, f"status {status} read as served"
        assert AC.classify(a) != "ok_200", f"status {status} classified ok_200"

    # POSITIVE CONTROL: 200 with no failure object still serves, so the pin is not merely
    # "everything fails now".
    served = {"request": {}, "status": 200, "dt": 1.0,
              "response": {"result": {"status": "complete"}}}
    assert AC.failed(served) is False
    assert AC.classify(served) == "ok_200"
    # ...and a 200 carrying a failure object is still a failure (the r1/r2 shape).
    assert AC.failed({"status": 200, "response": {"failure": {"code": "x", "httpStatus": 200}}}) is True


def test_the_attempt_class_and_the_row_bucket_agree_about_success():
    """codex r4 P1. `failed()` accepted all of [200, 400) while the PRODUCER accepts only
    200: `harness.post` retries or terminates on anything else, and `merge_corpus.classify`
    buckets `http != 200` as `error`. A 201 therefore classified `ok_200` at attempt level
    while its row bucketed `error` -- one artefact, two verdicts.

    The pin this replaces asserted 201/204/302/399 were `ok_200` as a "positive control",
    so the fixture CERTIFIED the disagreement instead of testing for it. Third time a
    fixture of mine agreed with my model rather than measuring it, and the first time it
    was load-bearing.

    The assertion is now the agreement itself, over the status space, against the real
    merge bucketer -- and both sides read ONE predicate.
    """
    for status in (200, 201, 204, 299, 302, 399, 400, 413, 422, 500, 502, 504, 0, 599):
        attempt = {"request": {}, "status": status, "dt": 1.0,
                   "response": {"result": {"status": "complete"}}}
        row = {"corpus_id": "q", "final_http": status, "final_payload_status": "complete",
               "claimed_facts_n": 1, "chain": "t1=complete"}
        attempt_served = not AC.failed(attempt)
        row_served = MC.classify(row) != "error"
        assert attempt_served == row_served, (
            f"http={status}: attempt says served={attempt_served}, "
            f"row says served={row_served} (bucket {MC.classify(row)})")
        assert attempt_served == (status == 200), f"http={status} served={attempt_served}"

    # Both sides must call the SAME predicate, not two spellings that happen to agree.
    assert AC.is_success_status(200) is True
    for status in (0, 201, 204, 302, 399, 400, 504):
        assert AC.is_success_status(status) is False, status
    src = (HERE / "merge_corpus.py").read_text()
    assert "attempt_classes.is_success_status" in src, \
        "merge_corpus spells its own success rule again instead of sharing the predicate"


def test_the_frozen_counters_are_INDEPENDENT_predicates():
    """codex r4 P1, reproduced against the BASE reader before the fix.

    The frozen counters were rebuilt off the EXCLUSIVE class -- one class per attempt --
    but the originals were independent tests: an attempt could count as BOTH a 504 and a
    413. So an outer 504 the engine answered with an inner 422 or 413 lost its deadline
    count entirely, and `reclassify_deadlines.is_deadline` reads that counter, so the row
    was never selected for reclassification. Measured, before:

        outer504_inner422  OLD(504,413)=(1,0)  NEW=(0,0)
        bare413            OLD(504,413)=(0,0)  NEW=(0,1)
        outer504_inner413  OLD(504,413)=(1,1)  NEW=(0,1)
    """
    cases = {
        # (attempts, expected 504 count, expected 413 count)
        "outer504_inner422": ([_attempt(504, failure={"code": "x", "httpStatus": 422})], 1, 0),
        # FAITHFUL to the original, which counts a bare 413 nowhere. team-lead's ruling:
        # frozen means frozen -- `attempt_overrun_413_n` keys a published artefact, so a
        # widening would change what past numbers meant and would read as a rise in
        # ceiling rejections to anyone looking at a trend. The gap is ticketed separately.
        # It is not invisible meanwhile: the exclusive CLASS below still says overrun_413.
        "bare413": ([_attempt(413)], 0, 0),
        "outer504_inner413": ([_attempt(504, failure={"code": "x", "httpStatus": 413})], 1, 1),
        # controls: the ordinary shapes must be unchanged
        "plain504": ([_attempt(504)], 1, 0),
        "inner413_only": ([_attempt(200, failure={"code": "x", "httpStatus": 413})], 0, 1),
        "served": ([_attempt(200, result={"status": "complete"})], 0, 0),
    }
    for name, (attempts, want504, want413) in cases.items():
        with tempfile.TemporaryDirectory() as tmp:
            out = _write_turns(tmp, {1: attempts})
            diag = RS.attempt_diagnostics(out, "q-a", 1, harness_attempts=len(attempts))
        got = (diag["attempt_upstream_504_n"], diag["attempt_overrun_413_n"])
        assert got == (want504, want413), f"{name}: got {got}, want {(want504, want413)}"
        if name == "bare413":
            # the frozen counter stays 0, AND the new class vocabulary still sees it, so
            # faithfulness costs no visibility.
            assert diag["attempt_class_n"]["overrun_413"] == 1, diag["attempt_class_n"]
        # the counters are INDEPENDENT of the exclusive class, by construction
        if want504 and want413:
            assert len({c["class"] for c in diag["attempt_outcomes"]}) == 1, \
                f"{name}: one attempt, one class -- yet BOTH counters must fire"

    # THE CONSEQUENCE the reviewer named: the deadline selector picks the row again.
    with tempfile.TemporaryDirectory() as tmp:
        out = _write_turns(tmp, {1: [_attempt(504, failure={"code": "x", "httpStatus": 422})]})
        diag = RS.attempt_diagnostics(out, "q-a", 1, harness_attempts=1)
    row = {"corpus_id": "q-a", "final_payload_status": "complete", "failure_code": None, **diag}
    assert RD.is_deadline(row) is True, \
        "a 504 answered with an inner 422 is still a deadline and must be reclassified"
    # NEGATIVE CONTROL: a row with no deadline is not selected.
    with tempfile.TemporaryDirectory() as tmp:
        out = _write_turns(tmp, {1: [_attempt(200, result={"status": "complete"})]})
        clean = RS.attempt_diagnostics(out, "q-a", 1, harness_attempts=1)
    assert RD.is_deadline({"corpus_id": "q-a", "final_payload_status": "complete",
                           "failure_code": None, **clean}) is False


def test_a_dropped_artefact_makes_the_row_unmeasured():
    """codex r2 P1, reproduced before the fix. `attempt_files` silently skips a filename
    that matches the glob but cannot be sequenced -- it lands in UNSEQUENCED, which nothing
    downstream read -- so the row published fewer attempts than happened and the 504 that
    attempt carried simply vanished, while the class table stayed structurally complete so
    the merge's exact-key guard passed it:

        harness would report attempts = 2   diagnostics attempts_total = 1
        attempt_upstream_504_n = 0          attempt_class_n complete? = True

    The row now RECONCILES against the harness's own count, and a mismatch makes it
    unmeasured rather than smaller-and-cleaner.
    """
    with tempfile.TemporaryDirectory() as tmp:
        out = Path(tmp) / "shard-00" / "replicate"
        out.mkdir(parents=True)
        (out / "q1-rep1-t1-a1.json").write_text(
            json.dumps(_attempt(200, result={"status": "complete"})))
        # matches the glob, cannot be sequenced
        (out / "q1-rep1-t?-a2.json").write_text(json.dumps(_attempt(504)))
        diag = RS.attempt_diagnostics(out, "q1", 1, harness_attempts=2)

    assert diag["attempts_total"] == 1, diag
    assert diag["attempts_reconciled"] is False, \
        "the walk lost an artefact and the row still called itself measured"
    assert diag["harness_attempts"] == 2, diag
    assert "q1-rep1-t?-a2.json" in diag["unsequenced_files"], diag

    # THE CONSEQUENCE: the merge refuses it rather than summing a short run.
    row = {"corpus_id": "q1", **diag}
    totals = MC.attempt_class_totals([row])
    assert totals["attempt_classes_unavailable"] == 1, totals
    assert totals["attempt_class_totals"] is None, "a short-walked row published totals"
    assert totals["attempt_classes_unreconciled"][0]["harness_attempts"] == 2, totals
    assert totals["attempt_classes_unreconciled"][0]["attempts_total"] == 1, totals

    # POSITIVE CONTROL: a row whose walk DOES reconcile publishes normally, so the
    # refusal above measures the discrepancy rather than refusing everything.
    with tempfile.TemporaryDirectory() as tmp:
        out = _write_turns(tmp, {1: [_attempt(504), _attempt(200, result={"status": "complete"})]})
        good = RS.attempt_diagnostics(out, "q-a", 1, harness_attempts=2)
    assert good["attempts_reconciled"] is True, good
    ok = MC.attempt_class_totals([{"corpus_id": "q-a", **good}])
    assert ok["attempt_classes_unavailable"] == 0, ok
    assert ok["attempt_class_totals"]["upstream_504"] == 1, ok


def test_detail_for_passes_the_harness_count_so_the_row_can_reconcile():
    """The reconciliation is only real if the producer actually wires it. Asserted on the
    AST: `detail_for` must call attempt_diagnostics WITH harness_attempts, not merely
    define a parameter nothing fills."""
    import ast as _ast
    tree = _ast.parse((HERE / "run_shard.py").read_text())
    fn = next(n for n in _ast.walk(tree)
              if isinstance(n, _ast.FunctionDef) and n.name == "detail_for")
    calls = [n for n in _ast.walk(fn)
             if isinstance(n, _ast.Call) and getattr(n.func, "id", None) == "attempt_diagnostics"]
    assert calls, "detail_for no longer calls attempt_diagnostics"
    kwargs = {k.arg for c in calls for k in c.keywords}
    assert "harness_attempts" in kwargs, \
        "detail_for calls attempt_diagnostics without the harness count -- the row cannot reconcile"


def test_the_committed_shape_space_is_regenerable_and_shows_no_divergence():
    """The shape space is COMMITTED, and this proves the committed copy is what the
    generator produces from the current tree -- not a stale artefact from a past state.

    codex r4 pointed out that the earlier packet referenced an UNTRACKED shape_space.json,
    which never reached the reviewer's fresh worktree; it correctly reported the file
    absent. Committing it fixes that, and regenerating it here is what keeps the committed
    copy honest.

    The two enumerations in this change are DIFFERENT SIZES and were conflated once: this
    sweep is 364 cells; the equivalence pin above is 247 (19 status bands x 13 failure
    shapes). Both numbers are asserted so neither can drift into the other.
    """
    committed = json.loads((HERE / "shape_space.json").read_text())
    assert len(committed) == 364, f"committed shape space has {len(committed)} cells, want 364"
    assert all(row["frozen"] for row in committed), \
        [r for r in committed if not r["frozen"]][:3]

    proc = subprocess.run([sys.executable, str(HERE / "shape_space.py")],
                          capture_output=True, text=True, cwd=str(HERE),
                          env={**os.environ, "PYTHONPATH": str(HERE / "testdata_corpus")})
    assert proc.returncode == 0, proc.stdout + proc.stderr
    assert "cells=364" in proc.stdout and "divergences=0" in proc.stdout, proc.stdout
    regenerated = json.loads((HERE / "shape_space.json").read_text())
    assert regenerated == committed, "the committed shape space is not what the tree generates"


if __name__ == "__main__":
    fails = 0
    for name, fn in sorted(globals().items()):
        if name.startswith("test_") and callable(fn):
            try:
                fn()
                print(f"PASS  {name}")
            except Exception as exc:
                fails += 1
                print(f"FAIL  {name}: {type(exc).__name__}: {str(exc)[:170]}")
    print(f"\n{fails} failing")
    raise SystemExit(1 if fails else 0)
