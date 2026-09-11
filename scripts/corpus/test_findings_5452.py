"""RED-FIRST pins for CHAOS-5452: score a refusal's basis from the DISCLOSED
refusal_basis token (completeness.refusal_basis, CHAOS-5442), never from the row's own
declaration alone.

Before this fix, `NAMED_BASIS_OVERRIDES[(REFUSE, "no_match")]` fired unconditionally for
every named-basis refuse row that terminated no_match -- always agree_weak/
weak_basis_unstated, whether or not the engine ever disclosed a matching basis. The only
"agree" cell for a declared-basis refusal was (REFUSE, "refused"), a terminal_status the
wire's ContextFabricInvestigationStatus enum (complete/partial/degraded/
clarification_required/no_match) cannot emit -- so weak_basis_unstated could never resolve
to agreement no matter what actually happened.
"""
import json
import sys
from pathlib import Path

HERE = Path(__file__).parent
sys.path.insert(0, str(HERE))

import corpus_example              # noqa: E402
import expectations as E           # noqa: E402

BY_ID = {r["id"]: r for r in corpus_example.CORPUS}
REFUSE_ROW = BY_ID["example-refuse-unservable-kind"]           # basis="member_kind_unservable"
DECLINE_ROW = BY_ID["example-decline-nonexistent-team"]         # basis="named_basis"


def _e(row):
    return E.expectation_for(row)


# --------------------------------------------------------------------- the fixed cell
def test_disclosed_basis_matching_the_declaration_is_agree():
    """The whole point of CHAOS-5452: a refusal whose disclosed basis MATCHES the row's
    declaration now scores agree, not agree_weak."""
    e = _e(REFUSE_ROW)
    v, why = E.score(e, "unserved", terminal_status="no_match",
                      disclosed_basis="member_kind_unservable")
    assert v == "agree", (v, why)
    assert E.weak_kind_for(v, why) is None


def test_missing_disclosure_is_unchanged_weak_basis_unstated():
    """The yardstick of record (main-0ad85ef4): field absent -- not empty, MISSING -- must
    still read weak_basis_unstated exactly as before this fix."""
    e = _e(REFUSE_ROW)
    v, why = E.score(e, "unserved", terminal_status="no_match", disclosed_basis=None)
    assert (v, E.weak_kind_for(v, why)) == ("agree_weak", "weak_basis_unstated")
    # an explicit empty string is ALSO "not stated" -- missing is not none, but neither
    # is an empty disclosure a token.
    v2, why2 = E.score(e, "unserved", terminal_status="no_match", disclosed_basis="")
    assert (v2, E.weak_kind_for(v2, why2)) == ("agree_weak", "weak_basis_unstated")


def test_disclosed_basis_outside_the_closed_vocabulary_is_never_agreement():
    """An unauthored token is its own outcome -- unscored, named by the token -- and must
    never silently fold into agreement (or agree_weak, or disagree already in the table)."""
    e = _e(REFUSE_ROW)
    v, why = E.score(e, "unserved", terminal_status="no_match",
                      disclosed_basis="member_kind_unservable_ish")
    assert v == "unscored", (v, why)
    assert why == "unauthored_basis:member_kind_unservable_ish"
    assert E.weak_kind_for(v, why) is None


def test_disclosed_basis_a_valid_but_different_member_is_disagree():
    """The engine refused, but for a DIFFERENT reason than the row declared -- a real
    disagreement, not a weak agreement and not a silent unscored."""
    e = _e(REFUSE_ROW)
    v, why = E.score(e, "unserved", terminal_status="no_match",
                      disclosed_basis="frame_invariant_violated")
    assert v == "disagree", (v, why)
    assert "member_kind_unservable" in why and "frame_invariant_violated" in why


def test_a_decline_with_a_named_but_non_vocabulary_basis_is_unaffected():
    """NEGATIVE CONTROL: a `decline` row's declared basis ("named_basis") is a marker, not
    a wire vocabulary token -- disclosure never enters this cell. It must stay the
    unconditional disagree it was before 5442 existed, regardless of what -- if anything --
    is disclosed."""
    e = _e(DECLINE_ROW)
    for disclosed in (None, "", "member_kind_unservable", "unspecified", "garbage"):
        v, why = E.score(e, "unserved", terminal_status="no_match", disclosed_basis=disclosed)
        assert v == "disagree", (disclosed, v, why)
        assert "named_basis" not in E.REFUSAL_BASIS_VOCABULARY  # the marker is not a member


def test_a_serve_row_ignores_disclosed_basis_entirely():
    """NEGATIVE CONTROL: disclosed_basis is consulted ONLY for (REFUSE, no_match) with a
    declared basis. A serve row passing a disclosed_basis (a caller error, or a served row
    that also happens to carry a refusal_basis by accident) must not change its verdict."""
    serve_row = BY_ID["example-serve-named-project"]
    e = _e(serve_row)
    with_basis = E.score(e, "served_with_data", terminal_status="partial",
                          disclosed_basis="member_kind_unservable")
    without_basis = E.score(e, "served_with_data", terminal_status="partial")
    assert with_basis == without_basis == ("agree", "served with facts as declared")


def test_a_refuse_row_with_no_declared_basis_ignores_disclosure_too():
    """NEGATIVE CONTROL: a refuse row that never DECLARED a basis stays the plain
    (REFUSE, no_match) agree cell -- disclosure narrows an existing declaration, it does
    not invent one."""
    e = {"expectation": E.REFUSE, "expectation_basis": None}
    v, why = E.score(e, "unserved", terminal_status="no_match",
                      disclosed_basis="member_kind_unservable")
    assert (v, why) == ("agree", "no_match: did not serve, as declared")


# ------------------------------------------------------------- the removed dead cell
def test_the_wire_status_enum_cannot_emit_refused():
    """Pin the CONTRACT, not just the scorer: internal/contracts/v1/context_fabric_types.go
    declares ContextFabricInvestigationStatus with exactly 5 members, and "refused" is not
    one of them. This is why (REFUSE, "refused") was dead code, not merely unused."""
    go_src = (HERE.parent.parent / "internal" / "contracts" / "v1"
              / "context_fabric_types.go").read_text()
    start = go_src.index("type ContextFabricInvestigationStatus string")
    block = go_src[start:go_src.index(")", start)]
    import re
    members = set(re.findall(r'ContextFabricInvestigationStatus\s*=\s*"([^"]+)"', block))
    assert members == {"complete", "partial", "degraded",
                        "clarification_required", "no_match"}
    assert "refused" not in members


def test_terminal_status_refused_is_never_agreement_for_a_declared_basis_refuse():
    """The cell is REMOVED: even if a caller somehow produced terminal_status="refused"
    (which the enum pin above proves the wire cannot), scoring it must never be agree."""
    e = _e(REFUSE_ROW)
    v, why = E.score(e, "unserved", terminal_status="refused")
    assert v != "agree", (v, why)
    assert v == "unscored", (v, why)
    assert ("refuse", "refused") not in E.VERDICTS


# --------------------------------------------------------------- measured vocabulary
def test_refusal_basis_vocabulary_matches_the_go_contract():
    """The override vocabulary is MIRRORED from the Go source (CHAOS-5430 pattern: derived,
    never hand-typed) -- this pin is what stops the two drifting apart."""
    go_src = (HERE.parent.parent / "internal" / "contracts" / "v1"
              / "context_fabric_refusal_basis.go").read_text()
    import re
    members = set(re.findall(r'ContextFabricRefusalBasis\w+\s+ContextFabricRefusalBasis\s*='
                              r'\s*"([^"]+)"', go_src))
    assert members == E.REFUSAL_BASIS_VOCABULARY == {
        "member_kind_unservable", "frame_invariant_violated", "unspecified",
        "continuation_context_unverifiable"}


def test_refusal_basis_observed_on_the_wire_is_a_subset_of_the_vocabulary():
    """MEASURED, not assumed: every completeness.refusal_basis value actually observed in
    the CHAOS-5442 rig archive is a member of the closed vocabulary -- if the wire ever
    emitted something outside it, that is the unauthored_basis case this fix names, and
    this pin is where a corpus-side surprise would first show up."""
    import glob
    root = Path.home() / ".cache/acr-kiac-askdev/proofs/2026-09-07-basis-5442"
    observed = set()
    for f in glob.glob(str(root / "**" / "replicate" / "*.json"), recursive=True):
        try:
            doc = json.loads(Path(f).read_text())
        except Exception:
            continue
        comp = ((doc.get("response") or {}).get("result") or {}).get("completeness") or {}
        rb = comp.get("refusal_basis")
        if rb:
            observed.add(rb)
    assert observed, "no refusal_basis values observed -- the archive path or shape moved"
    assert observed <= E.REFUSAL_BASIS_VOCABULARY, observed


# ------------------------------------------------------------------- table()-level wiring
def test_table_threads_disclosed_basis_by_id():
    rows_by_id = {"r1": REFUSE_ROW}
    out = E.table(rows_by_id, {"r1": "unserved"}, {}, terminals_by_id={"r1": "no_match"},
                  disclosed_basis_by_id={"r1": "member_kind_unservable"})
    assert len(out) == 1
    row = out[0]
    assert row["verdict"] == "agree"
    assert row["disclosed_basis"] == "member_kind_unservable"
    assert row["weak_kind"] is None


def test_table_without_disclosed_basis_by_id_reproduces_pre_5452_weak_basis_unstated():
    """Backward compatibility: a caller that never learned about disclosed_basis_by_id
    (the parameter defaults None) gets EXACTLY the pre-5452 reading."""
    rows_by_id = {"r1": REFUSE_ROW}
    out = E.table(rows_by_id, {"r1": "unserved"}, {}, terminals_by_id={"r1": "no_match"})
    row = out[0]
    assert row["verdict"] == "agree_weak"
    assert row["weak_kind"] == "weak_basis_unstated"
    assert row["disclosed_basis"] is None


if __name__ == "__main__":
    fails = 0
    for name, fn in sorted(globals().items()):
        if name.startswith("test_") and callable(fn):
            try:
                fn()
                print(f"PASS  {name}")
            except AssertionError as exc:
                fails += 1
                print(f"FAIL  {name}: {exc}")
    raise SystemExit(1 if fails else 0)
