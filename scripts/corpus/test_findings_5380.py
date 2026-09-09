"""RED-FIRST pins for CHAOS-5380 (corpus half): the attempt sequence rides BESIDE the
terminal bucket, and no failure is allowed to hide inside the reporting.

A row's terminal status is not evidence about what happened during the row. A 504 the
harness retried into a 200 leaves no trace in `final_http`, which is how the 09-06 smoke
report printed ZERO deadline failures against five logged 504s (regression-diagnosis doc
§4 O4, snapshot :172). Counting 504s and 413s closed that ONE class; §6 (:214) records
eight sequential non-200 attempts comprising four 422s, one 504, two 413s and one 400,
and no per-row counter has ever carried the 422s or the 400.

HOW THESE FIXTURES WERE PRODUCED, AND WHY IT IS DIFFERENT THIS TIME.
Nine defects of ONE class -- a failure the reporting hides -- were found in the previous
attempt at this change across five review rounds and two live replicates. Every single
time the cause was the same: the pins verified the shapes the author was thinking about
while the code was wrong in shapes the author was not. Three separate fixtures agreed
with the bug (all attempts under `t1`; all class tables complete; every failure object
carrying an `httpStatus`), and one pin actively CERTIFIED the defect it was meant to
catch (it asserted 201/204/302/399 were `ok_200` "as a positive control").

So the fixtures here are GENERATED FROM AN ENUMERATED AXIS LIST, never written from the
mental model that wrote the predicate, and the assertions are PROPERTIES over that
enumeration rather than a hand-written expected value per cell. The axes are:

  A  one attempt artefact  status band x response shape x failure shape x httpStatus x code
  B  the row's file set    turn structure x sequenceability x readability x harness count
  C  the row table at merge  class-table shape x reconciliation flag x row mix x corpus_id
  D  the row bucket        final_http x payload status x claimed facts
  E  the frozen artefact   the legacy kind ladder and the two frozen counters

REACHABILITY IS MEASURED, NOT ASSUMED. `validators.load_attempt` is the only decoder, so
it decides which cells a real artefact can occupy. Probed both ways and pinned below: it
ACCEPTS a status-less attempt, any int status including 0, an absent `response`, a failure
that is absent / `{}` / carries no `httpStatus`, and the exact transport artefact
`harness.post` writes. It REJECTS a str/bool status, a non-dict `response`, a non-dict
`failure`, and an explicitly null `httpStatus`. Where the classifier guards a shape the
loader refuses, that is a BOUNDED DEVIATION and it is pinned in both directions rather
than left implicit.

Where a pin could pass for the wrong reason it ships with a negative control, and a
control that does not fail when it should is itself the finding.
"""
import ast as _ast
import importlib.util
import itertools
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
import run_shard as RS           # noqa: E402
import validators as VAL         # noqa: E402


# ===================================================================== THE AXES
# One definition, used by every generator below. Adding a value here widens every
# property pin at once, which is the point: a cell nobody thought of cannot be
# covered by a fixture nobody wrote, but it CAN be covered by a property.

# A1. The attempt's own HTTP status. `None` means the key is ABSENT (the loader
# accepts that; it rejects an explicit null). 0 is what harness.post writes on any
# transport exception.
A1_STATUS = [None, 0, 1, 99, 199, 200, 201, 204, 302, 399, 400, 404, 413, 422,
             500, 502, 503, 504, 599, 999]

# A3/A4/A5. The failure object. `None` = no `failure` key at all; `{}` = present but
# EMPTY, which is FALSY and which the original ladder's `or {}` sends down the
# status-only branch -- the arm a hand-reconstruction got wrong.
A345_FAILURE = (
    [None, {}, {"code": "provider_error"}, {"code": "acr_contract_violation"}]
    + [{"code": "provider_error", "httpStatus": up}
       for up in (200, 400, 413, 422, 500, 502, 503, 504, 599)]
    + [{"code": "acr_contract_violation", "httpStatus": up} for up in (200, 502)]
)

# B1. Turn structure, as {turn: n_attempts}. The fourth and fifth entries are the
# shapes every fixture of the previous attempt was missing: a row spans several
# TURNS, and a new turn is a follow-up, not a retry.
B1_TURNS = [
    {},                       # no files at all
    {1: 1},                   # served first try
    {1: 3},                   # one turn, retried twice
    {1: 1, 2: 1},             # two turns, one attempt each -- ZERO retries
    {1: 1, 2: 3},             # a retry inside the SECOND turn only
    {1: 2, 2: 1, 3: 2},       # retries in the first and third turns
    {1: 1, 9: 1, 10: 1},      # t9 before t10, the lexicographic trap
]


def _attempt(status, failure=None, dt=1.0, result=None):
    response = {}
    if failure is not None:
        response["failure"] = failure
    if result is not None:
        response["result"] = result
    a = {"request": {}, "dt": dt, "response": response}
    if status is not None:
        a["status"] = status
    return a


def _served():
    return _attempt(200, result={"status": "complete"})


def _cells():
    """Every (status, failure) cell of the A axes, as loadable attempt artefacts."""
    for status, failure in itertools.product(A1_STATUS, A345_FAILURE):
        yield status, failure, _attempt(status, failure=failure)


def _write_turns(tmp, turns, qid="q-a", rep=1, attempt_for=None):
    """turns = {turn: [attempt, ...]} or {turn: n_attempts}. THE shape the harness writes.

    A helper that only ever wrote `t1` is how the turns-counted-as-retries defect passed
    every pin in the previous attempt, so the turn is a first-class parameter here and
    the single-turn helper is deliberately absent.
    """
    out = Path(tmp) / "shard-00" / "replicate"
    out.mkdir(parents=True, exist_ok=True)
    written = []
    for turn, spec in turns.items():
        attempts = spec if isinstance(spec, list) else [
            (attempt_for or (lambda t, i: _served()))(turn, i) for i in range(1, spec + 1)]
        for i, a in enumerate(attempts, start=1):
            p = out / f"{qid}-rep{rep}-t{turn}-a{i}.json"
            p.write_text(json.dumps(a))
            written.append(p)
    return out, written


def _contract_violation():
    """The LIVE shape, field for field from a real artefact of the 2026-09-09 private-pair
    replicate: the consumer returns 502 while the upstream call returned 200 -- acr
    answered, and its BODY failed the investigation contract."""
    return _attempt(502, failure={
        "code": "acr_contract_violation",
        "message": "ACR returned a result that does not satisfy the investigation contract.",
        "httpStatus": 200,
        "details": [" must NOT have additional properties",
                    "/completeness must NOT have additional properties"],
        "retryable": False,
    })


# ============================================================ A. reachability, measured
def test_the_loader_decides_which_cells_a_real_artefact_can_occupy():
    """BOTH DIRECTIONS. The classifier carries `isinstance` guards for shapes the loader
    refuses outright; that is a bounded deviation, and the bound is measured here rather
    than asserted in a comment. A guard whose reachability nobody checked is how the
    previous attempt shipped a deviation it could only describe.
    """
    accepted = [
        ("status absent", _attempt(None)),
        ("status 0, transport artefact", {"request": {}, "status": 0, "dt": 1.0,
                                          "response": {"error": "Connection refused"}}),
        ("response absent", {"request": {}, "status": 200, "dt": 1.0}),
        ("failure empty", _attempt(502, failure={})),
        ("failure without httpStatus", _attempt(502, failure={"code": "x", "message": "m"})),
    ]
    for name, a in accepted:
        ok, reason = VAL.validate_attempt(a)
        assert ok, f"the loader REFUSES {name}, so the cell is unreachable: {reason}"

    refused = [
        ("status str", {"request": {}, "status": "200", "dt": 1.0, "response": {}}),
        ("status bool", {"request": {}, "status": True, "dt": 1.0, "response": {}}),
        ("response non-dict", {"request": {}, "status": 200, "dt": 1.0, "response": "x"}),
        ("failure non-dict", {"request": {}, "status": 502, "dt": 1.0,
                              "response": {"failure": "x"}}),
        ("httpStatus null", _attempt(502, failure={"code": "x", "message": "m",
                                                   "httpStatus": None})),
    ]
    for name, a in refused:
        ok, _ = VAL.validate_attempt(a)
        assert not ok, f"the loader ACCEPTS {name}; the classifier's guard for it is LIVE"

    # The guards still hold on the refused shapes, because a defence that is unreachable
    # today is one loader change away from being reachable tomorrow.
    assert AC.classify({"request": {}, "status": 200, "dt": 1.0, "response": "x"}) == "ok_200"
    assert AC.classify(_attempt(502, failure="x")) == "upstream_502"


def test_every_enumerated_cell_is_loadable_or_named_unloadable():
    """The A-axis generator must not quietly produce artefacts a real run cannot contain.
    Every cell is put through the real loader and the refusals are NAMED, so the space
    the properties below range over is a space of REACHABLE inputs."""
    unloadable = [(s, f) for s, f, a in _cells() if not VAL.validate_attempt(a)[0]]
    assert not unloadable, f"generated unreachable cells: {unloadable[:5]}"


# ================================================= A. properties over the attempt space
def test_classify_is_total_over_the_enumerated_attempt_space():
    """Every attempt lands in exactly one member of the CLOSED vocabulary. A class
    nobody thought of cannot vanish into a default."""
    bad = [(s, f, AC.classify(a)) for s, f, a in _cells() if AC.classify(a) not in AC.CLASSES]
    assert not bad, f"{len(bad)} cells classify outside the vocabulary: {bad[:5]}"


def test_no_evidence_of_failure_is_ever_classified_ok_200():
    """THE CLASS INVARIANT, and the one property all nine defects violated.

    Stated independently of how `failed()` is written, so it cannot be satisfied by the
    same reasoning that produced the predicate: an attempt is SERVED only when its own
    status is present AND is exactly 200 AND it carries no failure object. Everything
    else -- a 502 whose upstream said 200, a transport 0, a bare 413, a 201, an attempt
    with no status at all -- is a failure. Enumerating SUCCESS is the whole inversion;
    every previous version listed the shapes that count as broken and was bitten by a
    shape that was not on the list.
    """
    served_cells, failed_cells = [], []
    for status, failure, a in _cells():
        served = (status == 200) and (failure is None)
        got_failed, got_class = AC.failed(a), AC.classify(a)
        if served:
            served_cells.append((status, failure))
            assert got_failed is False, f"a served attempt reads as failed: {status}/{failure}"
            assert got_class == "ok_200", f"a served attempt is not ok_200: {got_class}"
        else:
            failed_cells.append((status, failure))
            assert got_failed is True, \
                f"NO EVIDENCE OF SERVICE, yet failed()=False: status={status} failure={failure}"
            assert got_class != "ok_200", \
                f"a failure classified ok_200 -- the whole defect class: {status}/{failure}"
    # The partition must be non-trivial in BOTH directions, or the loop above proves
    # nothing: a predicate that is constantly True satisfies half of it for free.
    assert served_cells, "the enumeration contains no served cell"
    assert len(failed_cells) > len(served_cells), "the enumeration barely exercises failure"


def test_ok_200_is_exactly_the_not_failed_class():
    """A second counter over the same space, deliberately redundant with the one above.
    Every defect on this seam was caught by two counters disagreeing, so the file carries
    two rather than one."""
    for status, failure, a in _cells():
        assert (AC.classify(a) == "ok_200") == (not AC.failed(a)), (status, failure)


def test_failed_agrees_with_the_producer_about_what_served_means():
    """codex r4 P1-2, and the pin that certified it. `failed()` once accepted all of
    [200,400) while the PRODUCER accepts only 200 -- `harness.post`'s caller breaks on
    `status == 200` and `merge_corpus.classify` buckets `http != 200` as `error`. A 201
    therefore classified `ok_200` at attempt level while its own row bucketed `error`:
    one artefact, two verdicts, which is the disagreement this module exists to make
    impossible. Worse, a pin asserted 201/204/302/399 WERE `ok_200` as a "positive
    control", so the fixture certified the disagreement instead of testing for it.

    There is now ONE definition of served and both readers import it. This ranges over
    the whole status axis rather than the four statuses that happened to be noticed.
    """
    for status in A1_STATUS:
        attempt_served = not AC.failed(_attempt(status, result={"status": "complete"}))
        row = {"final_http": status, "final_payload_status": "complete", "claimed_facts_n": 1}
        row_served = MC.classify(row) != "error"
        assert attempt_served == row_served, (
            f"status={status}: attempt says served={attempt_served} while the row bucket "
            f"says served={row_served} -- {MC.classify(row)}")
    # NEGATIVE CONTROL: the crossing must be capable of disagreeing, or it is vacuous.
    # The retired [200,400) predicate has to break it.
    disagreements = [s for s in A1_STATUS
                     if (isinstance(s, int) and 200 <= s < 400)
                     != (MC.classify({"final_http": s, "final_payload_status": "complete",
                                      "claimed_facts_n": 1}) != "error")]
    assert disagreements, "the control cannot discriminate; the crossing proves nothing"


def test_a_2xx_that_is_not_200_is_a_failure_not_a_positive_control():
    """The RE-AIMED cell. Named separately from the property above because this exact
    assertion existed with the opposite sense and passed for three rounds."""
    for status in (201, 204, 302, 399):
        a = _attempt(status, result={"status": "complete"})
        assert AC.failed(a) is True, f"{status} reads as served"
        assert AC.classify(a) != "ok_200", f"{status} classified ok_200"
        assert AC.classify(a) == "failure_under_2xx", AC.classify(a)


def test_the_upstream_refines_the_class_only_when_it_is_itself_an_error():
    """Defect 1's axis: the attempt's OWN status decides whether it failed, the upstream
    only refines WHY. An earlier version let the upstream win whenever present, and on a
    contract violation (consumer 502, upstream 200) SEVEN failed attempts read `ok_200`.

    A third counter over the space: the governing status is computed here from the axis
    values, and the class token is required to follow it.
    """
    by_status = {400: "rejected_400", 413: "overrun_413", 422: "unprocessable_422",
                 500: "engine_invalid_500", 502: "upstream_502", 503: "upstream_503",
                 504: "upstream_504"}
    for status, failure, a in _cells():
        if not AC.failed(a):
            continue
        got = AC.classify(a)
        if status is None and (failure is None or "httpStatus" not in (failure or {})):
            assert got == "unreadable", (status, failure, got)
            continue
        if isinstance(status, int) and status < 200:
            assert got == "transport_failure", (status, failure, got)
            continue
        if (failure or {}).get("code") == "acr_contract_violation":
            assert got == "contract_violation", (status, failure, got)
            continue
        up = (failure or {}).get("httpStatus")
        governing = up if (isinstance(up, int) and up >= 400) else status
        if governing is None or governing < 400:
            assert got == "failure_under_2xx", (status, failure, got)
        else:
            want = by_status.get(governing,
                                 "other_4xx" if governing < 500 else "other_5xx")
            assert got == want, (status, failure, got, want)


# ================================================= A. named repros of the defect shapes
def test_a_contract_violation_is_a_failure_not_ok_200():
    """DEFECT 1, live: 7 attempts over 6 rows, every one http 502 with
    `failure.httpStatus` 200 and code `acr_contract_violation`."""
    a = _contract_violation()
    assert AC.failed(a) is True
    assert AC.classify(a) == "contract_violation", AC.classify(a)
    assert AC.legacy_engine_failure_kind(a) == "OTHER_200"
    # NEGATIVE CONTROL: the same status pair WITHOUT the code is not a contract
    # violation -- the class is keyed on the code, not on the status pair.
    assert AC.classify(_attempt(502, failure={"code": "provider_error",
                                              "httpStatus": 200})) == "upstream_502"


def test_a_transport_failure_is_not_ok_200():
    """DEFECT 4. `harness.post` returns `0, {"error": str(e)}` on any transport exception
    -- status 0, NO failure envelope -- so a rule written as `http >= 400 or a failure
    object` fired on neither and a refused connection classified as served."""
    a = {"request": {}, "status": 0, "dt": 0.0, "response": {"error": "Connection refused"}}
    assert VAL.validate_attempt(a)[0], "the real transport artefact must be loadable"
    assert AC.failed(a) is True
    assert AC.classify(a) == "transport_failure", AC.classify(a)
    # "never reached the service" and "the service answered 5xx" are different facts.
    assert AC.classify(_attempt(503)) == "upstream_503"


def test_sub_200_statuses_other_than_zero_are_transport_failures():
    """The rest of the sub-200 band. Nothing writes 1 or 199 today; the point of a closed
    total vocabulary is that a status nobody writes yet still lands somewhere visible."""
    for status in (1, 99, 199):
        assert AC.classify(_attempt(status)) == "transport_failure", status


def test_an_attempt_with_no_status_is_not_silently_served():
    """A1=absent. The loader ACCEPTS a status-less attempt, so this cell is reachable.
    It must fail CLOSED: no status is not evidence of service.

    `unreadable` names two different facts -- an artefact that did not parse, and one
    that parsed but carried no status. Both fail closed, which is the property that
    matters here, and the two are now told apart by `unreadable_reason` BESIDE the class
    (team-lead ruling: keep the published vocabulary, add the reason). See
    test_every_unreadable_outcome_says_WHY for both values and the None case.
    """
    a = _attempt(None)
    assert VAL.validate_attempt(a)[0], "a status-less attempt must be loadable"
    assert AC.failed(a) is True
    assert AC.classify(a) == "unreadable", AC.classify(a)
    assert AC.unreadable_reason(a) == AC.UNREADABLE_STATUS_ABSENT, AC.unreadable_reason(a)
    assert AC.legacy_engine_failure_kind(a) is None, "the original yields no record here"


def test_every_unreadable_outcome_says_WHY():
    """q1, ruled by team-lead: the class vocabulary is UNCHANGED and the reason rides
    beside it. `unreadable` covers two genuinely different instrument failures -- a file
    the loader could not decode, and one it decoded that carried no `status` -- and a
    reader must not have to guess which happened.

    The key is present on EVERY outcome record, None included: a missing key and a
    known-readable attempt must never look alike, the same rule the class table's
    explicit zeros follow.
    """
    with tempfile.TemporaryDirectory() as tmp:
        out, _ = _write_turns(tmp, {1: [_attempt(None), _served(), _attempt(504)]})
        (out / "q-a-rep1-t1-a4.json").write_text("{ not json")
        diag = _diagnose(out)
    seq = diag["attempt_outcomes"]
    assert len(seq) == 4, seq
    assert all("unreadable_reason" in o for o in seq), \
        "the key must be present on EVERY record, not only the unreadable ones"
    got = [(o["class"], o["unreadable_reason"]) for o in seq]
    assert got == [("unreadable", AC.UNREADABLE_STATUS_ABSENT),
                   ("ok_200", None),
                   ("upstream_504", None),
                   ("unreadable", AC.UNREADABLE_PARSE_FAILED)], got
    # The two reasons must be DIFFERENT values, or the field carries no information.
    assert AC.UNREADABLE_STATUS_ABSENT != AC.UNREADABLE_PARSE_FAILED
    # NEGATIVE CONTROL: a readable, served attempt reports None -- so a non-None value
    # is a measurement rather than a constant.
    assert AC.unreadable_reason(_served()) is None
    assert AC.unreadable_reason(_attempt(502)) is None


def test_the_measured_shapes_of_the_live_run_all_classify_correctly():
    """The exact distribution measured on a private pair, 2026-09-09: 91 served, 7
    contract violations, 6 rejected answers, 1 rejected request. Real shapes, not
    invented ones."""
    live = [
        (_served(), "ok_200"),
        (_contract_violation(), "contract_violation"),
        (_attempt(422, failure={"code": "acr_answer_rejected", "httpStatus": 422}),
         "unprocessable_422"),
        (_attempt(400, failure={"code": "acr_rejected_request", "httpStatus": 400}),
         "rejected_400"),
    ]
    for a, want in live:
        assert AC.classify(a) == want, (a, AC.classify(a), want)


# ================================== E. the frozen artefact: DERIVED, not reconstructed
# The merge base of this branch. `post_hoc_attempt_classes` in the merged verdict is keyed
# by engine_failures' own kind strings, and the two frozen counters key the deliverable, so
# both are read out of git at THIS sha and RUN, never transcribed. A reference the test
# types out by hand is the same mistake at one remove: the previous attempt's
# hand-reconstruction agreed with the original everywhere except one column and was
# believed for three review rounds.
ORIGINAL_SHA = "f4dfef13a1e86b8b759af81aa0503c6a4b931ff6"

# Committed copy of the original module, so the equivalence pin still runs in a shallow
# clone that lacks the object. It is asserted byte-identical to git below; the frozen copy
# is a fallback, never the primary reference.
FROZEN_ORIGINAL = HERE / "testdata_corpus" / "engine_failures_original.py"


def _original_source(path):
    """The original module's bytes at ORIGINAL_SHA, or None if the object is absent."""
    r = subprocess.run(["git", "-C", str(HERE), "show", f"{ORIGINAL_SHA}:{path}"],
                       capture_output=True, text=True)
    return r.stdout if r.returncode == 0 else None


def _load_original(path, name):
    src = _original_source(path)
    if src is None:
        assert FROZEN_ORIGINAL.exists(), (
            f"{ORIGINAL_SHA}:{path} is not in this clone and no frozen copy exists -- "
            "run in a full clone; a skip here would be a pass, and it is not one")
        assert path.endswith("engine_failures.py"), \
            f"no frozen fallback exists for {path}; run in a full clone"
        src = FROZEN_ORIGINAL.read_text()
    tmpdir = tempfile.mkdtemp()
    mod_path = Path(tmpdir) / f"{name}.py"
    mod_path.write_text(src)
    sys.path.insert(0, str(HERE))          # so its own imports resolve
    spec = importlib.util.spec_from_file_location(name, mod_path)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def test_the_frozen_original_copy_is_byte_identical_to_git():
    """The fallback must be the original, not a drifted copy of it. If the object is in
    this clone the two are compared byte for byte; if it is not, this pin says so out
    loud rather than passing quietly."""
    src = _original_source("scripts/corpus/engine_failures.py")
    assert src is not None, (
        f"{ORIGINAL_SHA} is not in this clone, so the frozen copy cannot be verified "
        "against it -- the equivalence pin below is running on an UNVERIFIED reference")
    assert FROZEN_ORIGINAL.exists(), f"the frozen copy is missing: {FROZEN_ORIGINAL}"
    assert FROZEN_ORIGINAL.read_text() == src, \
        "the committed frozen copy has drifted from the original at ORIGINAL_SHA"


def _original_scan_kind():
    """The ORIGINAL ladder, exercised through its own public `scan` on real files."""
    mod = _load_original("scripts/corpus/engine_failures.py", "engine_failures_original")

    def kind_of(attempt):
        with tempfile.TemporaryDirectory() as run:
            out = Path(run) / "shard-00" / "replicate"
            out.mkdir(parents=True)
            (out / "q-a-rep1-t1-a1.json").write_text(json.dumps(attempt))
            recs = mod.scan(Path(run))
        return recs[0]["kind"] if recs else None

    return kind_of


def test_legacy_ladder_is_equivalent_over_the_whole_shape_space():
    """OPTION (a) -- chris 2026-09-09 -- and the proof that it worked.

    Round 3 found 24 differing cells, every one in the single column `failure object
    present, httpStatus absent`, and not one covered by a fixture. The lesson was not
    "add those 24 fixtures"; it was that a hand-reconstruction cannot be spot-checked
    into correctness. So the assertion is TOTAL over the enumerated A axes, against the
    original itself, read out of git and run.
    """
    original = _original_scan_kind()
    differences, cells = [], 0
    for status, failure, attempt in _cells():
        cells += 1
        want, got = original(attempt), AC.legacy_engine_failure_kind(attempt)
        if want != got:
            differences.append((status, failure, want, got))
    assert cells == len(A1_STATUS) * len(A345_FAILURE), cells
    assert not differences, (
        f"{len(differences)} of {cells} cells differ from the ORIGINAL ladder; "
        f"first five: {differences[:5]}")

    # NEGATIVE CONTROL: round 3's exact defect -- branch on `upstream is None` rather
    # than on whether a failure object is PRESENT AND TRUTHY. It must be caught, or this
    # pin proves nothing about the predicate it is guarding.
    wrong = 0
    for _s, _f, attempt in _cells():
        http, upstream = AC._statuses(attempt)
        if upstream is None:
            bad = ((f"UPSTREAM_{http}" if http in (502, 503, 504) else f"OTHER_{http}")
                   if isinstance(http, int) and http >= 400 else None)
        else:
            bad = f"OTHER_{upstream}"
        if bad != original(attempt):
            wrong += 1
    assert wrong >= 24, f"the control found only {wrong} differences; it is not discriminating"


def test_legacy_engine_failure_kinds_are_unchanged():
    """POSITIVE CONTROL for the frozen strings, branch by branch -- including the old
    ladder's own two inconsistencies (a bare 502/503 status is `UPSTREAM_*`, a PARSED 502
    is `OTHER_502`), which are frozen precisely because they are inconsistent."""
    cases = [
        (_attempt(504), "UPSTREAM_504"),
        (_attempt(502), "UPSTREAM_502"),
        (_attempt(503), "UPSTREAM_503"),
        (_attempt(500), "OTHER_500"),
        (_attempt(400), "OTHER_400"),
        (_attempt(413), "OTHER_413"),
        (_attempt(502, failure={}), "UPSTREAM_502"),
        (_attempt(200, failure={"httpStatus": 500}), "ENGINE_INVALID_RESULT"),
        (_attempt(200, failure={"httpStatus": 413}), "RIG_CEILING_413"),
        (_attempt(200, failure={"httpStatus": 504}), "UPSTREAM_504"),
        (_attempt(504, failure={"httpStatus": 422}), "UPSTREAM_504"),
        (_attempt(200, failure={"httpStatus": 422}), "OTHER_422"),
        (_attempt(200, failure={"httpStatus": 502}), "OTHER_502"),
        (_attempt(200, failure={"code": "x"}), "OTHER_None"),
        (_attempt(502, failure={"code": "x"}), "OTHER_None"),
    ]
    for attempt, want in cases:
        got = AC.legacy_engine_failure_kind(attempt)
        assert got == want, f"{attempt} -> {got}, want {want}"
    assert AC.legacy_engine_failure_kind(_served()) is None


def test_scan_still_reports_the_same_kinds_through_the_shared_classifier():
    """The class proof for the pin above: engine_failures.scan, end to end on files."""
    with tempfile.TemporaryDirectory() as tmp:
        out, _ = _write_turns(tmp, {1: [
            _attempt(504),
            _attempt(200, failure={"httpStatus": 413, "measuredItems": 33, "maxItems": 30}),
            _attempt(200, failure={"httpStatus": 500}),
            _attempt(200, failure={"httpStatus": 422}),
            _served(),
        ]})
        kinds = sorted(r["kind"] for r in EF.scan(out.parent))
    assert kinds == ["ENGINE_INVALID_RESULT", "OTHER_422", "RIG_CEILING_413", "UPSTREAM_504"], kinds


def test_the_frozen_counters_are_INDEPENDENT_predicates():
    """DEFECT 7 (codex r4 P1-1). The two frozen counters were rebuilt off the EXCLUSIVE
    class -- one class per attempt -- but the originals are INDEPENDENT tests, and an
    attempt may count as BOTH a 504 and a 413. Deriving them from the class silently
    dropped the deadline on every 504 the engine answered with an inner 422 or 413, and
    `reclassify_deadlines.is_deadline` reads that counter, so the row was never selected
    for reclassification: a deadline hidden by the reporting, in the counter the previous
    attempt twice called safe "because it is derived from the same walk". Same walk,
    different SHAPE of predicate.

    FROZEN MEANS FROZEN. A bare 413 -- the attempt's own status with no parsed failure
    object -- counts ZERO in the frozen counter even though the class vocabulary names it
    `overrun_413`, because `attempt_overrun_413_n` keys a published artefact and widening
    a frozen key changes what past numbers meant. That gap is ticketed, not absorbed.
    """
    cases = [
        ("outer 504, inner 422", _attempt(504, failure={"code": "x", "httpStatus": 422}),
         (True, False), "unprocessable_422"),
        ("outer 504, inner 413", _attempt(504, failure={"code": "x", "httpStatus": 413}),
         (True, True), "overrun_413"),
        ("bare 413, no failure", _attempt(413), (False, False), "overrun_413"),
        ("upstream 413 only", _attempt(200, failure={"code": "x", "httpStatus": 413}),
         (False, True), "overrun_413"),
        ("bare 504", _attempt(504), (True, False), "upstream_504"),
        ("served", _served(), (False, False), "ok_200"),
    ]
    for name, a, want, want_class in cases:
        got = (AC.is_upstream_504(a), AC.is_overrun_413(a))
        assert got == want, f"{name}: frozen counters {got}, want {want}"
        assert AC.classify(a) == want_class, f"{name}: class {AC.classify(a)}"
    # NEGATIVE CONTROL: reading the counters OFF the exclusive class -- the retired
    # shape -- must lose the outer-504 deadline, or these cases do not discriminate.
    lost = [n for n, a, _w, cls in cases
            if AC.is_upstream_504(a) and cls != "upstream_504"]
    assert lost, "no case distinguishes an independent predicate from the exclusive class"


def test_the_frozen_counters_match_the_original_over_the_whole_walk():
    """TOTAL form of the pin above, and the one that would have caught defect 7 without
    anyone thinking of the outer-504/inner-422 shape.

    The ORIGINAL `run_shard.attempt_diagnostics` is read out of git and RUN on the same
    generated file sets, and its two frozen scalars must equal the new walk's, cell for
    cell. Same mechanism as the ladder pin: derive the reference, never describe it.
    """
    orig = _load_original("scripts/corpus/run_shard.py", "run_shard_original")
    mismatches, cells = [], 0
    for status, failure, a in _cells():
        with tempfile.TemporaryDirectory() as tmp:
            out, _ = _write_turns(tmp, {1: [a]})
            orig.UNSEQUENCED.clear()
            want = orig.attempt_diagnostics(out, "q-a", 1)
            RS.UNSEQUENCED.clear()
            got = RS.attempt_diagnostics(out, "q-a", 1)
        cells += 1
        pair = ("attempt_upstream_504_n", "attempt_overrun_413_n")
        if tuple(want[k] for k in pair) != tuple(got[k] for k in pair):
            mismatches.append((status, failure,
                               tuple(want[k] for k in pair), tuple(got[k] for k in pair)))
    assert cells == len(A1_STATUS) * len(A345_FAILURE), cells
    assert not mismatches, (
        f"{len(mismatches)} of {cells} cells move a FROZEN counter; first five: "
        f"{mismatches[:5]}")


# ================================================= B. the row's file set, as it is on disk
def _diagnose(out, qid="q-a", rep=1, harness_attempts=None):
    """One walk, with the module-global UNSEQUENCED cleared first.

    `run_shard.UNSEQUENCED` is a MODULE GLOBAL keyed by corpus id alone -- not by rep,
    and never cleared. `main()` is invoked once per rep so cross-rep bleed is unreachable
    today, but the pins must not depend on that, and neither must a reader: the property
    is pinned below rather than assumed away.
    """
    RS.UNSEQUENCED.clear()
    return RS.attempt_diagnostics(out, qid, rep, harness_attempts=harness_attempts)


def test_row_carries_the_attempt_sequence_not_only_a_count():
    """ACCEPTANCE (a). `attempts` says 3; it cannot say 504, then 422, then served."""
    with tempfile.TemporaryDirectory() as tmp:
        out, _ = _write_turns(tmp, {1: [
            _attempt(504),
            _attempt(200, failure={"httpStatus": 422, "code": "acr_answer_rejected"}),
            _served(),
        ]})
        diag = _diagnose(out)
    seq = diag["attempt_outcomes"]
    assert [a["class"] for a in seq] == ["upstream_504", "unprocessable_422", "ok_200"], seq
    assert [a["attempt"] for a in seq] == [1, 2, 3], seq
    assert [a["turn"] for a in seq] == [1, 1, 1], seq
    assert diag["attempts_total"] == 3 and diag["attempts_retried"] == 2, diag
    with tempfile.TemporaryDirectory() as tmp:
        out, _ = _write_turns(tmp, {1: [_served()]})
        solo = _diagnose(out)
    assert [a["class"] for a in solo["attempt_outcomes"]] == ["ok_200"], solo
    assert solo["attempts_retried"] == 0, "an unretried row must report an EXPLICIT zero"


def test_every_class_is_counted_with_explicit_zeros():
    """422 and 400 have never been carried per row -- §6 (:214) names four 422s and one
    400 among the eight sequential non-200 attempts. All keys always present: an absent
    measurement and a measured zero must never look alike."""
    with tempfile.TemporaryDirectory() as tmp:
        out, _ = _write_turns(tmp, {1: [
            _attempt(200, failure={"httpStatus": 422, "code": "acr_answer_rejected"}),
            _attempt(200, failure={"httpStatus": 400, "code": "acr_rejected_request"}),
            _attempt(200, failure={"httpStatus": 413, "measuredItems": 33, "maxItems": 30}),
            _attempt(504),
            _served(),
        ]})
        diag = _diagnose(out)
    counts = diag["attempt_class_n"]
    assert set(counts) == set(AC.CLASSES), "the class counter is not the closed vocabulary"
    for name in ("unprocessable_422", "rejected_400", "overrun_413", "upstream_504", "ok_200"):
        assert counts[name] == 1, (name, counts)
    assert counts["other_5xx"] == 0, "an unobserved class must be an EXPLICIT zero"
    assert diag["attempt_upstream_504_n"] == 1 and diag["attempt_overrun_413_n"] == 1, diag


def test_a_follow_up_TURN_is_not_a_retry():
    """DEFECT 2, live: `attempts_retried = len(outcomes) - 1` over a walk that spans
    TURNS called 32 of 36 rows retried and reported 69 retries against 6 real ones. Every
    fixture of the previous attempt had put its attempts under `t1`, so the pins agreed
    with the bug. Repro: two turns, one attempt each, must be ZERO."""
    with tempfile.TemporaryDirectory() as tmp:
        out, _ = _write_turns(tmp, {1: [_attempt(200, failure={"httpStatus": 422,
                                                               "code": "x"})],
                                    2: [_attempt(502)]})
        diag = _diagnose(out)
    assert diag["attempts_total"] == 2, diag
    assert diag["attempts_retried"] == 0, \
        "two TURNS of one attempt each is not a retry -- nothing was retried"
    # NEGATIVE CONTROL: the same two attempts INSIDE one turn IS one retry.
    with tempfile.TemporaryDirectory() as tmp:
        out, _ = _write_turns(tmp, {1: [_attempt(200, failure={"httpStatus": 422,
                                                               "code": "x"}),
                                        _attempt(502)]})
        same = _diagnose(out)
    assert same["attempts_retried"] == 1, same


def test_retries_are_counted_per_turn_over_the_enumerated_turn_shapes():
    """TOTAL form: every B1 turn shape, with the expected count computed from the AXIS
    VALUES -- sum(n - 1) over the turns -- never by re-running the code under test."""
    for turns in B1_TURNS:
        want_retried = sum(n - 1 for n in turns.values())
        want_total = sum(turns.values())
        with tempfile.TemporaryDirectory() as tmp:
            out, _ = _write_turns(tmp, turns)
            diag = _diagnose(out)
        assert diag["attempts_total"] == want_total, (turns, diag["attempts_total"])
        assert diag["attempts_retried"] == want_retried, (turns, diag["attempts_retried"])
    # The enumeration must contain a shape that DISCRIMINATES, or the loop is vacuous:
    # at least one multi-turn shape whose naive `len - 1` answer differs from the truth.
    naive_differs = [t for t in B1_TURNS
                     if sum(t.values()) and sum(t.values()) - 1 != sum(n - 1 for n in t.values())]
    assert naive_differs, "no enumerated shape distinguishes per-turn from len-1"


def test_the_sequence_comes_from_the_filename_not_the_listing_position():
    """Lexicographic order puts t10 before t9. The sequence is read from the RECORDED
    name via attempt_order, never from a directory listing."""
    with tempfile.TemporaryDirectory() as tmp:
        out, _ = _write_turns(tmp, {1: [_attempt(504)], 9: [_attempt(422, failure={
            "code": "x", "httpStatus": 422})], 10: [_served()]})
        diag = _diagnose(out)
    assert [a["turn"] for a in diag["attempt_outcomes"]] == [1, 9, 10], diag["attempt_outcomes"]
    assert [a["class"] for a in diag["attempt_outcomes"]] == [
        "upstream_504", "unprocessable_422", "ok_200"], diag["attempt_outcomes"]


def test_an_unreadable_attempt_is_recorded_never_skipped():
    """An artefact we cannot read is REPORTED. A scanner that drops what it cannot parse
    describes a smaller, cleaner run than the one that happened."""
    with tempfile.TemporaryDirectory() as tmp:
        out, _ = _write_turns(tmp, {1: [_attempt(504), _served()]})
        (out / "q-a-rep1-t1-a2.json").write_text("{ not json")
        diag = _diagnose(out)
    assert diag["attempts_total"] == 2, diag
    classes = [a["class"] for a in diag["attempt_outcomes"]]
    assert classes == ["upstream_504", "unreadable"], classes
    assert diag["attempt_class_n"]["unreadable"] == 1, diag["attempt_class_n"]
    assert diag["attempt_outcomes"][1].get("detail"), "the loader's message must travel with the record"
    assert diag["attempt_outcomes"][1]["unreadable_reason"] == AC.UNREADABLE_PARSE_FAILED, \
        diag["attempt_outcomes"][1]


def test_every_file_on_disk_is_accounted_for():
    """DEFECT 5, in its TOTAL form. `attempt_files` silently skipped a filename that
    matched the glob but could not be sequenced; it landed in a module global nothing
    downstream read, so a row published fewer attempts than happened and the 504 that
    attempt carried simply vanished -- while the class table stayed structurally
    complete, so the merge's exact-key guard passed it.

    The property is accounting, not the one filename that was noticed: EVERY file the
    glob matches appears either in the sequence or in `unsequenced_files`. Nothing may
    fall between the two.
    """
    for extra in ("q-a-rep1-t?-a2.json", "q-a-rep1-tX-a1.json",
                  "q-a-rep1-t1-a1-extra.json", "q-a-rep1-t1-a.json"):
        with tempfile.TemporaryDirectory() as tmp:
            out, written = _write_turns(tmp, {1: [_attempt(504)], 2: [_served()]})
            (out / extra).write_text(json.dumps(_attempt(504)))
            on_disk = sorted(p.name for p in out.glob("q-a-rep1-t*-a*.json"))
            diag = _diagnose(out, harness_attempts=3)
        seen = len(diag["attempt_outcomes"]) + len(diag["unsequenced_files"])
        assert seen == len(on_disk), (
            f"{extra}: {len(on_disk)} files on disk, {seen} accounted for "
            f"({diag['attempts_total']} sequenced, {diag['unsequenced_files']})")
        assert extra in diag["unsequenced_files"], (extra, diag["unsequenced_files"])
        assert diag["attempts_reconciled"] is False, \
            "a row that lost an artefact must not read as measured"


def test_a_dropped_artefact_makes_the_row_unmeasured():
    """DEFECT 5, named, end to end through the merge: the row is refused rather than
    published with a smaller, cleaner story than the run had."""
    with tempfile.TemporaryDirectory() as tmp:
        out, _ = _write_turns(tmp, {1: [_attempt(504)]})
        (out / "q-a-rep1-t?-a2.json").write_text(json.dumps(_attempt(504)))
        diag = _diagnose(out, harness_attempts=2)
    assert diag["attempts_total"] == 1 and diag["harness_attempts"] == 2, diag
    assert diag["attempts_reconciled"] is False, diag
    assert diag["unsequenced_files"] == ["q-a-rep1-t?-a2.json"], diag
    row = {"corpus_id": "q-a", **diag}
    out_totals = MC.attempt_class_totals([row])
    assert out_totals["attempt_classes_unavailable"] == 1, out_totals
    assert out_totals["attempt_class_totals"] is None, "a lost artefact must WITHHOLD totals"
    assert out_totals["attempt_classes_unreconciled"][0]["unsequenced_files"] == [
        "q-a-rep1-t?-a2.json"], out_totals
    # NEGATIVE CONTROL: the same row WITHOUT the stray file reconciles and publishes.
    with tempfile.TemporaryDirectory() as tmp:
        out, _ = _write_turns(tmp, {1: [_attempt(504)]})
        clean = _diagnose(out, harness_attempts=1)
    assert clean["attempts_reconciled"] is True, clean
    assert MC.attempt_class_totals([{"corpus_id": "q-a", **clean}])[
        "attempt_class_totals"]["upstream_504"] == 1


def test_a_row_with_no_artefacts_at_all_is_unmeasured():
    """B1's ZERO-FILES cell, on its own because it is the degenerate one and degenerate
    cells are where a guard gets written as "or empty". A row whose walk found NOTHING
    while the harness says it made attempts is the emptiest possible version of the
    dropped-artefact defect, and it must refuse exactly like the others.

    NOT RED-FIRST, and it is labelled rather than counted: this behaviour is already
    correct in the first commit of this branch, so the pin PASSES there. It is a
    coverage pin for a cell nothing exercised, and its power to discriminate is proven
    by its mutant arm (the reconciliation guard widened with `or not outcomes`), never
    by a red run it never had.
    """
    with tempfile.TemporaryDirectory() as tmp:
        out, _ = _write_turns(tmp, {})
        empty = _diagnose(out, harness_attempts=2)
        # ...and the same empty walk with NO harness count is reconciled: the check is
        # about disagreement, not about emptiness.
        silent = _diagnose(out, harness_attempts=None)
    assert empty["attempts_total"] == 0 and empty["attempt_outcomes"] == [], empty
    assert empty["attempts_retried"] == 0, empty
    assert empty["attempts_reconciled"] is False, \
        "a row with no artefacts against a harness that made 2 attempts is NOT measured"
    assert set(empty["attempt_class_n"]) == set(AC.CLASSES), empty
    assert silent["attempts_reconciled"] is True, silent
    # ...and the merge refuses the unreconciled one rather than summing its zeros.
    refused = MC.attempt_class_totals([{"corpus_id": "q-a", **empty}])
    assert refused["attempt_classes_unavailable"] == 1, refused
    assert refused["attempt_class_totals"] is None, \
        "an all-zero class table from a walk that found nothing is not a measurement"


def test_reconciliation_over_the_enumerated_harness_counts():
    """B4 in full, INCLUDING the direction nobody tested: the harness counting FEWER
    attempts than the walk found. `reconciled` is `harness_attempts is None or equal`,
    and it must hold in both directions of inequality."""
    with tempfile.TemporaryDirectory() as tmp:
        out, _ = _write_turns(tmp, {1: [_attempt(504), _served()]})
        for harness_n, want in [(None, True), (2, True), (1, False), (3, False), (0, False)]:
            diag = _diagnose(out, harness_attempts=harness_n)
            assert diag["attempts_total"] == 2, diag
            assert diag["attempts_reconciled"] is want, (harness_n, diag["attempts_reconciled"])
            assert diag["harness_attempts"] == harness_n, diag


def test_detail_for_reconciles_against_the_harness_count():
    """RE-AIMED. The previous pin asserted a keyword NAMED `harness_attempts` existed --
    it proved nothing about what that keyword receives, and it was carried as evidence
    for three rounds. This one drives `detail_for` itself and requires the flag to FLIP
    with the harness's own count, so a decorative parameter cannot satisfy it.
    """
    with tempfile.TemporaryDirectory() as tmp:
        out, _ = _write_turns(tmp, {1: [_attempt(504), _served()]})
        base_r = {"final_http": 200, "final_payload_status": "complete",
                  "chain": ["t1=complete"], "wrong_kind_flag": False,
                  "wrong_subject_flag": False, "subject_kind_mismatch_flag": False}
        row = {"note": "", "family": "f"}
        RS.UNSEQUENCED.clear()
        agreeing = RS.detail_for(out, "q-a", row, {**base_r, "attempts": 2}, 1.0, 1)
        RS.UNSEQUENCED.clear()
        disagreeing = RS.detail_for(out, "q-a", row, {**base_r, "attempts": 5}, 1.0, 1)
    assert agreeing["attempts_reconciled"] is True, agreeing
    assert agreeing["harness_attempts"] == 2, agreeing
    assert disagreeing["attempts_reconciled"] is False, \
        "detail_for does not pass the harness's own count through to the walk"
    assert disagreeing["harness_attempts"] == 5, disagreeing


def test_the_unsequenced_register_is_scoped_to_the_row_it_reports():
    """B6, disclosed rather than assumed away. `run_shard.UNSEQUENCED` is a module global
    keyed by corpus id ALONE -- not by rep -- and it is never cleared. `main()` runs once
    per rep, so a stray file from one rep cannot reach another rep's row today. That is a
    property of the CALLER, not of the register, so it is pinned here: a second corpus id
    must not inherit the first one's stray filenames.
    """
    with tempfile.TemporaryDirectory() as tmp:
        out, _ = _write_turns(tmp, {1: [_attempt(504)]}, qid="q-a")
        _write_turns(tmp, {1: [_served()]}, qid="q-b")
        (out / "q-a-rep1-t?-a2.json").write_text(json.dumps(_attempt(504)))
        RS.UNSEQUENCED.clear()
        first = RS.attempt_diagnostics(out, "q-a", 1, harness_attempts=2)
        second = RS.attempt_diagnostics(out, "q-b", 1, harness_attempts=1)
    assert first["unsequenced_files"] == ["q-a-rep1-t?-a2.json"], first
    assert second["unsequenced_files"] == [], \
        "a stray file from one row leaked into another row's report"
    assert second["attempts_reconciled"] is True, second


# ================================================== C. the row table at the merge
def _measured_row(cid, **counts):
    return {"corpus_id": cid, "attempts_reconciled": True,
            "attempt_class_n": dict(AC.zero_counts(), **counts)}


def test_merge_refuses_to_report_an_absent_measurement_as_zero():
    """REPRODUCES THE 09-06 FALSE ZERO. Rows written by an instrument that never ran the
    attempt walk carry no counters; summing them with `or 0` prints a confident zero,
    which is exactly the report that said ZERO deadline failures against five logged
    504s. §5 (:200) rules the other way: report incomplete capture rather than treat an
    absent event as a measured zero."""
    unmeasured = [{"corpus_id": "q-a", "attempts": 3}, {"corpus_id": "q-b", "attempts": 1}]
    totals = MC.attempt_class_totals(unmeasured)
    assert totals["attempt_classes_unavailable"] == 2, totals
    assert totals.get("attempt_class_totals") is None, \
        "a total was published for rows that were never measured"
    assert totals.get("rows_with") is None, \
        "a zeroed denominator still reads as a measurement to anything that plots it"

    # POSITIVE CONTROL: measured rows DO publish, and the unavailable count is an
    # EXPLICIT zero -- so the refusal above is a measurement, not a constant.
    measured = [_measured_row("q-a", upstream_504=2, ok_200=1),
                _measured_row("q-b", unprocessable_422=1, ok_200=1)]
    totals = MC.attempt_class_totals(measured)
    assert totals["attempt_classes_unavailable"] == 0, totals
    assert totals["attempt_class_totals"]["upstream_504"] == 2, totals
    assert totals["rows_with"]["upstream_504"] == 1, totals
    assert totals["attempt_class_totals"]["other_4xx"] == 0, "explicit zero expected"

    # MIXED: one measured, one not. A partial run must not silently under-report.
    assert MC.attempt_class_totals(measured + unmeasured)["attempt_classes_unavailable"] == 2


def test_the_merge_refusal_is_total_over_the_row_table_space():
    """C1 x C2 in full. DEFECT 3 lived in one cell of this space -- a syntactically valid
    PARTIAL dict, accepted because `isinstance(x, dict)` was the whole guard, so every
    class it omitted was published as a measured zero. The lesson was not "add a fixture
    for a partial table": it was that the guard has to be an EXACT key-set equality, in
    both directions, and that a structurally complete table is not by itself proof the
    row was walked (defect 5 came through exactly that gap).

    Expectation per cell is computed from the AXIS VALUES, never by re-running the code.
    """
    c1 = [
        ("absent", None, False),
        ("non-dict", "not a dict", False),
        ("empty dict", {}, False),
        ("missing a key", {"upstream_504": 1}, False),
        ("extra unknown key", dict(AC.zero_counts(), invented_class=3), False),
        ("exact key set", dict(AC.zero_counts(), upstream_504=1), True),
    ]
    c2 = [("absent", None, False), ("False", False, False), ("True", True, True)]
    for tname, table, table_ok in c1:
        for fname, flag, flag_ok in c2:
            row = {"corpus_id": f"{tname}/{fname}"}
            if table is not None:
                row["attempt_class_n"] = table
            if flag is not None:
                row["attempts_reconciled"] = flag
            want_measured = table_ok and flag_ok
            got = MC.attempt_class_totals([row])
            assert got["attempt_classes_unavailable"] == (0 if want_measured else 1), \
                f"{tname}/{fname}: unavailable={got['attempt_classes_unavailable']}"
            assert (got["attempt_class_totals"] is not None) is want_measured, \
                f"{tname}/{fname}: totals={got['attempt_class_totals']}"
            if not want_measured:
                assert got["attempt_classes_unavailable_ids"] == [row["corpus_id"]], got
    # The space must contain both verdicts, or the loop above is one-sided.
    assert any(t for _n, _t, t in c1) and any(not t for _n, _t, t in c1)


def test_every_refused_row_is_counted_AND_named():
    """C4, ruled by team-lead: a row with no corpus id is named `<no corpus_id>` rather
    than dropped from the list.

    An earlier version filtered falsy ids out, so an unnamed row appeared in the count
    and in nothing else -- and a list shorter than the count beside it reads as a
    reporting bug rather than as the unnamed row it actually is. There is now no shape
    that is counted and unnamed, and the two lengths are asserted EQUAL.
    """
    rows = [{"corpus_id": "named", "attempts": 1},
            {"corpus_id": "", "attempts": 1},
            {"attempts": 1}]
    got = MC.attempt_class_totals(rows)
    assert got["attempt_classes_unavailable"] == 3, got
    assert got["attempt_class_totals"] is None, got
    assert got["attempt_classes_unavailable_ids"] == [
        MC.NO_CORPUS_ID, MC.NO_CORPUS_ID, "named"], got
    assert len(got["attempt_classes_unavailable_ids"]) == got["attempt_classes_unavailable"], \
        "every counted row must also be named"
    # The unreconciled detail list names it the same way, so one run never spells the
    # same missing id two ways.
    unrec = MC.attempt_class_totals(
        [{"attempts_reconciled": False, "attempt_class_n": AC.zero_counts(),
          "harness_attempts": 2, "attempts_total": 1}])
    assert unrec["attempt_classes_unreconciled"][0]["corpus_id"] == MC.NO_CORPUS_ID, unrec
    # NEGATIVE CONTROL: a named row keeps its own id; the stand-in is not applied to all.
    named = MC.attempt_class_totals([{"corpus_id": "q-a", "attempts": 1}])
    assert named["attempt_classes_unavailable_ids"] == ["q-a"], named


def test_merged_row_carries_the_sequence_beside_the_bucket():
    """ACCEPTANCE (a), at the artefact boundary: run the REAL merge over a real shard and
    read the verdict it wrote. An earlier draft asserted on a dict literal it had built
    itself -- the inert-control shape. An instrument must READ the result, never RESTATE
    the expectation."""
    from corpus import CORPUS
    qid = "example-serve-named-project"
    with tempfile.TemporaryDirectory() as tmp:
        indir = Path(tmp) / "seq"
        shard = indir / "shard-00"
        (shard / "replicate").mkdir(parents=True)
        rows = []
        # Every corpus id runs: the merge REFUSES a partial run as inadmissible evidence,
        # which is itself the behaviour that stops a short run reading as a complete one.
        for entry in CORPUS:
            cid = entry["id"]
            attempts = ([_attempt(504)] if cid == qid else []) + [_served()]
            for i, a in enumerate(attempts, start=1):
                (shard / "replicate" / f"{cid}-rep1-t1-a{i}.json").write_text(json.dumps(a))
            row = {"corpus_id": cid, "family": entry.get("family"), "section_note": "",
                   "final_http": 200, "final_payload_status": "complete",
                   "chain": "t1=complete", "attempts": len(attempts),
                   "wrong_kind_flag": False, "wrong_subject_flag": False,
                   "subject_kind_mismatch_flag": False, "wall_seconds": 1.0,
                   "claimed_facts_n": 1, "failure_code": None}
            RS.UNSEQUENCED.clear()
            row.update(RS.attempt_diagnostics(shard / "replicate", cid, 1,
                                              harness_attempts=len(attempts)))
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
    assert merged["bucket"] == "served_with_data", merged["bucket"]
    assert [a["class"] for a in merged["attempt_outcomes"]] == ["upstream_504", "ok_200"], merged
    assert merged["attempts_total"] == 2 and merged["attempts_retried"] == 1, merged
    assert merged["attempt_class_n"]["upstream_504"] == 1, merged
    diagnostics = verdict["rig_diagnostics"]
    assert diagnostics["attempt_classes_unavailable"] == 0, diagnostics
    assert diagnostics["attempt_class_totals"]["upstream_504"] == 1, diagnostics
    # NEGATIVE CONTROL: the terminal fields alone say NOTHING about that 504 -- the exact
    # statistic the 09-06 smoke report published as a zero.
    assert merged["final_http"] == 200 and merged["failure_code"] is None, merged


# ============================================== structural: one classifier, one artefact
def test_one_classifier_serves_both_counters():
    """STRUCTURAL ONLY, and labelled as such. Two modules counted attempts with two
    ladders and agreed by accident; every instrument defect on this seam has been two
    counters disagreeing about one artefact.

    This walks the AST for a real CALL, because the source-TEXT form it replaced passed
    on a comment, on a docstring, and on an unused import. It is still weaker than the
    behavioural pins above -- it proves calls EXIST, not that their VALUES feed the
    counters -- so the equivalence and totality pins are what actually hold the property;
    this one exists to catch a second ladder growing back.
    """
    def calls_into(path, module):
        tree = _ast.parse((HERE / path).read_text())
        return {n.func.attr for n in _ast.walk(tree)
                if isinstance(n, _ast.Call) and isinstance(n.func, _ast.Attribute)
                and isinstance(n.func.value, _ast.Name) and n.func.value.id == module}

    shard = calls_into("run_shard.py", "attempt_classes")
    assert {"classify", "outcome", "zero_counts"} <= shard, \
        f"run_shard does not CALL the shared classifier: {shard}"
    assert {"is_upstream_504", "is_overrun_413"} <= shard, \
        f"run_shard does not CALL the shared frozen predicates: {shard}"
    assert "legacy_engine_failure_kind" in calls_into("engine_failures.py", "attempt_classes"), \
        "engine_failures does not CALL the shared ladder"
    assert "is_success_status" in calls_into("merge_corpus.py", "attempt_classes"), \
        "merge_corpus does not CALL the shared success predicate"
    # NEGATIVE CONTROL: the same walk over a module that genuinely does not use it must
    # come back empty, so the assertion is not satisfied by the walk itself.
    assert calls_into("attempt_order.py", "attempt_classes") == set()
    # And no second ladder: neither consumer may compare a status to a sentinel the
    # classifier owns.
    for path in ("run_shard.py", "engine_failures.py"):
        literals = {n.value for n in _ast.walk(_ast.parse((HERE / path).read_text()))
                    if isinstance(n, _ast.Constant) and isinstance(n.value, int)}
        assert not ({502, 503, 504, 413, 422} & literals), \
            f"{path} carries HTTP status literals -- a second ladder is growing back"


def test_the_committed_shape_space_is_regenerable_and_shows_no_divergence():
    """The sweep is COMMITTED, not left untracked: a review round was pointed at an
    untracked copy of this artefact and correctly reported it absent, because untracked
    files do not reach a fresh worktree of the commit.

    Its 364 cells are a DIFFERENT enumeration from the equivalence pin's own, and from
    this file's A axes. Conflating two of those numbers was itself a finding, so each is
    stated with the enumeration it belongs to and none of them is a coverage figure.
    """
    committed = HERE / "shape_space.json"
    assert committed.exists(), "the shape space is not committed"
    before = committed.read_text()
    proc = subprocess.run([sys.executable, str(HERE / "shape_space.py")],
                          capture_output=True, text=True, cwd=str(HERE))
    assert proc.returncode == 0, proc.stdout + proc.stderr
    assert "legacy-ladder divergences=0" in proc.stdout, proc.stdout
    assert committed.read_text() == before, \
        "the committed shape space is not what its generator produces"
    rows = json.loads(before)
    assert len(rows) == 364, f"the sweep is {len(rows)} cells, not the 364 it reports"
    assert all(r["frozen"] for r in rows), "a cell of the committed sweep is not frozen"


TESTS = [v for k, v in sorted(globals().items()) if k.startswith("test_")]

if __name__ == "__main__":
    failures = []
    for fn in TESTS:
        try:
            fn()
            print(f"PASS  {fn.__name__}")
        except Exception as exc:                       # noqa: BLE001 -- a pin runner
            failures.append((fn.__name__, exc))
            print(f"FAIL  {fn.__name__}: {type(exc).__name__}: {exc}")
    print(f"\n{len(TESTS) - len(failures)}/{len(TESTS)} pins pass")
    raise SystemExit(1 if failures else 0)
