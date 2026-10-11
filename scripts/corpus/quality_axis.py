#!/usr/bin/env python3
"""ANSWER-QUALITY AXIS (Q1): a mechanical score of what a served result CONTAINS.

The buckets in merge_corpus.py and the agreement table in expectations.py say whether a
row was SERVED and whether serving matched its declared expectation. They say nothing
about whether the served answer is complete, grounded, disclosed and honest. This module
adds that second axis, beside them and never in place of them. classify(), BUCKETS and
expectations.py are untouched; nothing here changes a bucket.

Q1 IS CONTRACT-MECHANICAL. Every dimension is computed from the stored result document
and the row's declared expectation. There is no model call and no model-as-judge in this
file. It is NOT a correctness measure: Q1 checks that a result is complete, grounded,
disclosed and self-consistent. Only a comparison against canonical data (a separate,
later axis) says whether the facts are TRUE. An optional model-judged dimension is
deliberately absent; if one is ever added it is its own column, off by default, and
needs an approval id.

DIMENSIONS (each 0 / 1 / 2, or None when not applicable)
  D1 completeness   obligations satisfied; a serve that claims zero facts is a hollow serve
  D2 coverage       degrading coverage details: none / only truncation-narrowing / hard gap
  D3 disclosure     a partial/degraded answer must carry limitations or warnings
  D4 grounding      GATE. driver->claim and driver->evidence references resolve
  D5 subject        GATE. no substitution (identity rules R1/R2, same semantics as
                    subject_identity.py; pinned against it by a parity test)
  D6 window         served answer states its effective window with provenance
  D7 cohort/budget  an incomplete cohort or a truncated budget is disclosed
  D8 terminal       GATE. terminal_status == status; state/claimed-count coherent
  Refusal track     R1 refusal basis disclosed (closed vocabulary), R2 nothing committed
                    or claimed (GATE), D3, D8
  Clarify track     C1 options offered, D8

RULES THAT ARE EASY TO GET WRONG (read before changing anything)
  * REFUSAL-CORRECT ROWS EARN FULL MARKS. A row whose declared expectation is refuse or
    decline, answered with a no_match that discloses its basis and commits and claims
    nothing, scores 1.0. Zero facts is the right answer there; scoring it on facts would
    punish the engine for being correct. About a third of the corpus is of this kind.
  * SERVING A REFUSE-EXPECTED ROW IS A FABRICATION: Q = 0. One declared exception: a
    row that declares `decline_if_shape_emitted` owes a decline only when the model
    emitted the refusing shape; a served result that shows no such shape (no refusal
    basis, no group kind equal to the member kind) is scored by content like a serve
    row and carries `conditional_decline`.
  * TIMEOUTS AND FAILED TERMINAL ATTEMPTS ARE NOT MEASURED. They are never scored 0 and
    never enter a mean; they are counted per class as `not_measured`.
  * A ROW THAT WAS NOT SERVED ON A SERVE-EXPECTED ROW has no Q. served -> refused is a
    STATUS regression, owned by the bucket axis. It is reported as `not_served`, not as
    a quality delta.
  * ONE NUMBER NEVER HIDES A GATE. A gate dimension at 0 caps the row at 0.5 (a
    substitution or a fabrication at 0.0) and is listed by row id.
  * NO BLENDED NUMBER. Means are per expectation class: serve / refuse / clarify /
    undeclared.
  * Output carries row ids, never question text.

"BETTER" between two runs (compare mode, rows measured in BOTH, same ids):
  zero new gate failures AND zero row with a dimension that fell by 2 AND improved rows
  more than worsened rows AND no class mean fell. Otherwise `same` or `worse`, with the
  dimension that moved. A one-step move on one dimension of one row is within the noise
  of a single replicate and is reported as `same` unless --reps-agree is supplied.
"""
import argparse
import json
import sys
from collections import Counter, defaultdict
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))

import expectations as E  # noqa: E402
from attempt_order import order_attempts  # noqa: E402
from subject_identity import _norm  # noqa: E402

SCORER_VERSION = "quality-axis.v2"

SERVED = {"complete", "partial", "degraded"}
GATES = ("D4", "D5", "D8", "R2")
HARD_STATES = {"unavailable", "unconfigured", "unauthorized", "no_data", "conflicted",
               "stale"}
SOFT_STATES = {"truncated", "pruned", "available", "not_applicable"}
REFUSAL_VOCAB = set(E.REFUSAL_BASIS_VOCABULARY)


# ---------------------------------------------------------------- loading ---------

def _doc_of(data):
    """The result/projection document inside whatever a file holds, or None.

    Accepted: a harness attempt envelope {"response": {"result": doc}}; an envelope whose
    response is the doc; the MCP tool payload {"structured": doc}; a bare doc.
    """
    if not isinstance(data, dict):
        return None
    resp = data.get("response")
    if isinstance(resp, dict):
        if isinstance(resp.get("result"), dict):
            return resp["result"]
        if "status" in resp and "completeness" in resp:
            return resp
        return None
    if isinstance(data.get("structured"), dict):
        return data["structured"]
    if "status" in data and "completeness" in data:
        return data
    return None


def _status_of(data):
    if isinstance(data, dict) and isinstance(data.get("status"), int):
        return data["status"]
    return None


def discover(root, row_id, rep):
    pat = f"**/{row_id}-rep{rep}-t*-a*.json"
    hits, _skipped = order_attempts(Path(root).glob(pat), on_unparseable="skip")
    return hits


def row_records(root, row_id, rep):
    """(doc|None, reason|None, path|None) for the row's terminal attempt."""
    hits = discover(root, row_id, rep)
    if not hits:
        return None, "no_artefact", None
    path = hits[-1]
    try:
        data = json.loads(Path(path).read_text())
    except Exception as exc:   # unreadable bytes are their own state, never a silent pass
        return None, f"unreadable_artefact:{type(exc).__name__}", str(path)
    doc = _doc_of(data)
    if doc is None:
        st = _status_of(data)
        return None, (f"terminal_attempt_failure:http_{st}" if st else
                      "terminal_attempt_failure"), str(path)
    return doc, None, str(path)


def reps_present(root):
    import re
    tags = set()
    for f in Path(root).glob("**/*-rep*-t*-a*.json"):
        m = re.search(r"-rep(\d+)-t\d+-a\d+\.json$", f.name)
        if m:
            tags.add(int(m.group(1)))
    return sorted(tags)


# ------------------------------------------------------------- normalising --------

def view(doc):
    """One shape for the full result and for the MCP answer projection."""
    comp = doc.get("completeness") or {}
    full = "claimed_facts" in doc
    pb = doc.get("projection_budget") or {}
    if full:
        claims = [c.get("claim_id") for c in (doc.get("claimed_facts") or [])]
        drivers = doc.get("drivers") or []
        cov = doc.get("coverage") or {}
        details = cov.get("details") or []
        sources = cov.get("sources") or []
        partial = bool(cov.get("partial"))
        committed = (doc.get("subject_resolution") or {}).get("committed") or []
        clar = bool((doc.get("subject_resolution") or {}).get("candidates")
                    or doc.get("structure_needs") or doc.get("window_clarification"))
        cohort = doc.get("cohort") or None
    else:
        claims = [c.get("claim_id") for c in (doc.get("key_facts") or [])]
        drivers = doc.get("principal_drivers") or []
        details = doc.get("coverage_details") or []
        sources = doc.get("coverage_summary") or []
        partial = bool(doc.get("coverage_partial"))
        committed = doc.get("committed_subjects") or []
        clar = bool(doc.get("clarification") or doc.get("structure_needs"))
        cohort = doc.get("cohort") or None
    return {
        "status": doc.get("status"),
        "completeness": comp,
        "claims": claims,
        "claims_omitted": int(pb.get("facts_omitted") or 0),
        "refs_omitted": int(pb.get("evidence_refs_omitted") or 0),
        "budget_truncated": bool(pb.get("truncated")),
        "has_budget": bool(pb),
        "drivers": drivers,
        "details": details,
        "sources": sources,
        "partial": partial,
        "committed": committed,
        "limitations": doc.get("limitations") or [],
        "warnings": doc.get("warnings") or [],
        "evidence_refs": set(doc.get("evidence_ref_ids") or []),
        "window": doc.get("effective_evidence_window"),
        "cohort": cohort,
        "clarification_offered": clar,
        "refusal_basis": comp.get("refusal_basis") or doc.get("refusal_basis") or None,
        "shape": "full" if full else "projection",
    }


# -------------------------------------------------------------- dimensions --------

def _r(score, detail):
    return {"score": score, "detail": detail}


def _detail_state(d):
    s = d.get("source_state")
    if s:
        return s
    code = str(d.get("code") or "")
    return code.rsplit(":", 1)[-1] if ":" in code else code


def d1_completeness(v):
    c = v["completeness"]
    if not c:
        return _r(None, "no_completeness_block")
    claimed = c.get("claimed_facts_count")
    if v["status"] in SERVED and claimed == 0:
        return _r(0, "hollow_serve:zero_claimed_facts")
    outs = [o for o in (c.get("outcomes") or []) if o.get("outcome") != "not_applicable"]
    if c.get("state") == "not_derived" and not outs:
        return _r(None, "state_not_derived")
    if not outs:
        return _r(2, "no_obligation_rows")
    sat = sum(1 for o in outs if o.get("outcome") == "satisfied")
    if sat == len(outs):
        return _r(2, f"{sat}/{len(outs)} satisfied")
    return _r(1 if sat else 0, f"{sat}/{len(outs)} satisfied")


def d2_coverage(v):
    deg = [d for d in v["details"] if d.get("degrading")]
    if v["details"] == [] and v["sources"]:
        states = {s.get("state") for s in v["sources"]}
        if states & HARD_STATES:
            return _r(0, "source_state:" + ",".join(sorted(states & HARD_STATES)))
        return _r(2 if not (states & {"truncated"}) else 1, "sources_only")
    if not deg:
        return _r(2, "no_degrading_detail")
    hard = sorted({_detail_state(d) for d in deg} & HARD_STATES)
    if hard or any("unexpanded" in str(d.get("code")) for d in deg):
        return _r(0, "hard:" + ",".join(hard or ["unexpanded"]))
    return _r(1, "soft_only:" + ",".join(sorted({_detail_state(d) for d in deg})))


def _needs_disclosure(v):
    unsat = any(o.get("outcome") not in ("satisfied", "not_applicable")
                for o in (v["completeness"].get("outcomes") or []))
    return (v["status"] in ("partial", "degraded") or v["partial"] or unsat
            or any(d.get("degrading") for d in v["details"]))


def d3_disclosure(v):
    if not _needs_disclosure(v):
        return _r(2, "nothing_to_disclose")
    if v["limitations"]:
        return _r(2, f"{len(v['limitations'])}_limitations")
    if v["warnings"]:
        return _r(1, "warnings_only")
    return _r(0, "silent_partial:no_limitation_no_warning")


def d4_grounding(v):
    if not v["claims"] and not v["drivers"]:
        return _r(None, "no_claims_no_drivers")
    claim_ids = {c for c in v["claims"] if c}
    dangling = []
    if any(not c for c in v["claims"]) or len(claim_ids) != len(v["claims"]):
        dangling.append("claim_id_missing_or_duplicate")
    for dr in v["drivers"]:
        if not v["claims_omitted"]:
            for cid in dr.get("claimed_fact_ids") or []:
                if cid not in claim_ids:
                    dangling.append(f"driver:{dr.get('driver_id')}:claim:{cid}")
        if not v["refs_omitted"] and v["evidence_refs"]:
            for ref in dr.get("evidence_ref_ids") or []:
                if ref not in v["evidence_refs"]:
                    dangling.append(f"driver:{dr.get('driver_id')}:ref")
    if dangling:
        return _r(0, "dangling:" + ";".join(dangling[:5]) + f" ({len(dangling)})")
    claimed = (v["completeness"] or {}).get("claimed_facts_count")
    if claimed is not None and claimed != len(v["claims"]) + v["claims_omitted"] \
            and v["shape"] == "full":
        return _r(1, f"count_mismatch:claimed={claimed},listed={len(v['claims'])}")
    return _r(2, "all_references_resolve")


def d5_subject(v, expectation):
    committed = v["committed"]
    if expectation.get("declares_nonexistent") and committed:
        return _r(0, "R1_nonexistent_entity_committed")
    want = expectation.get("declared_anchor_name")
    want_kind = expectation.get("declared_anchor_kind")
    if want:
        if not committed:
            return _r(None, "anchor_declared_nothing_committed")
        w = _norm(want)

        def ok(c):
            if want_kind and c.get("kind") != want_kind:
                return False
            if _norm(c.get("label")) == w:
                return True
            return w in [_norm(s) for s in str(c.get("canonical_id") or "").split(":")]
        if not any(ok(c) for c in committed):
            return _r(0, "R2_declared_anchor_not_committed")
        return _r(2, "anchor_committed")
    return _r(None, "no_identity_declaration")


def d6_window(v):
    w = v["window"]
    if isinstance(w, dict) and w.get("provenance"):
        return _r(2, "window_with_provenance")
    return _r(1, "window_not_stated")


def d7_cohort_budget(v):
    coh, scores, notes = v["cohort"], [], []
    disclosed = bool(v["limitations"] or v["warnings"] or v["partial"])
    if coh:
        members = coh.get("members") or []
        total = coh.get("total", coh.get("population"))
        if coh.get("complete") is True and isinstance(total, int) and total > len(members) \
                and not v["claims_omitted"] and v["shape"] == "full":
            scores.append(0)
            notes.append("complete_but_members_short")
        elif coh.get("complete") is False or coh.get("truncated"):
            scores.append(1 if disclosed else 0)
            notes.append("incomplete_" + ("disclosed" if disclosed else "silent"))
        else:
            scores.append(2)
            notes.append("cohort_complete")
    if v["has_budget"] and v["budget_truncated"]:
        scores.append(1 if disclosed else 0)
        notes.append("budget_truncated_" + ("disclosed" if disclosed else "silent"))
    if not scores:
        return _r(None, "no_cohort_no_truncation")
    return _r(min(scores), ",".join(notes))


def d8_terminal(v):
    c, st = v["completeness"], v["status"]
    if not c:
        return _r(0, "no_completeness_block")
    bad = []
    if c.get("terminal_status") != st:
        bad.append(f"terminal_status={c.get('terminal_status')}!=status={st}")
    if st in SERVED and c.get("state") not in (None, "not_derived", st):
        bad.append(f"state={c.get('state')}!=status={st}")
    if st == "complete" and v["partial"]:
        bad.append("complete_with_coverage_partial")
    if st in ("no_match", "clarification_required") and (c.get("claimed_facts_count") or 0):
        bad.append("claimed_facts_on_unserved")
    return _r(0, ";".join(bad)) if bad else _r(2, "coherent")


def r1_basis(v, expectation):
    b = v["refusal_basis"]
    if not b:
        return _r(0, "basis_not_disclosed")
    if b not in REFUSAL_VOCAB:
        return _r(0, f"basis_outside_vocabulary:{b}")
    declared = expectation.get("expectation_basis")
    if declared in REFUSAL_VOCAB and declared != b:
        return _r(1, f"basis_differs_from_declared:{b}")
    return _r(2, f"basis:{b}")


def r2_nothing(v):
    claimed = (v["completeness"] or {}).get("claimed_facts_count") or 0
    if v["committed"] or claimed or v["claims"]:
        return _r(0, "committed_or_claimed_on_refusal")
    return _r(2, "nothing_committed_nothing_claimed")


def refusing_shape_emitted(doc, v):
    """True when a SERVED result itself shows the refusing shape was emitted.

    Read from the stored result only, no question text: the result discloses a
    refusal basis while being served, or its plan / cohort groups express a group
    kind equal to the cohort's member kind (a set grouped by itself). A served
    answer with none of these is a legal reading of the question, not a laundered
    illegal one: the engine refuses the illegal shape before it serves anything.
    """
    if v["refusal_basis"]:
        return True
    cohort = v["cohort"] or {}
    member_kind = cohort.get("kind")
    plan = doc.get("answer_plan") or {}
    group_kinds = {plan.get("group_kind")}
    for g in cohort.get("groups") or []:
        group_kinds.add(((g or {}).get("subject") or {}).get("kind"))
    group_kinds.discard(None)
    group_kinds.discard("")
    return bool(member_kind and member_kind in group_kinds)


def c1_options(v):
    return _r(2, "options_offered") if v["clarification_offered"] \
        else _r(0, "clarification_without_options")


# --------------------------------------------------------------- row score --------

def _fold(dims):
    sc = [d["score"] for d in dims.values() if d["score"] is not None]
    q = round(sum(sc) / (2 * len(sc)), 4) if sc else None
    gates = sorted(k for k, d in dims.items() if k in GATES and d["score"] == 0)
    if q is not None and gates:
        q = min(q, 0.5)
    return q, gates


def score_row(doc, expectation):
    v = view(doc)
    st = v["status"]
    cls = expectation.get("expectation")
    cls = cls if cls in (E.SERVE, E.REFUSE, E.DECLINE, E.CLARIFY) else "undeclared"
    klass = {E.DECLINE: E.REFUSE}.get(cls, cls)
    out = {"class": klass, "status": st, "shape": v["shape"]}
    if st not in SERVED | {"no_match", "clarification_required"}:
        return dict(out, measured=False, reason=f"unknown_status:{st}", q=None,
                    dims={}, gates_failed=[])

    if st in SERVED:
        if klass in (E.REFUSE, E.CLARIFY):
            if (klass == E.REFUSE and expectation.get("decline_if_shape_emitted")
                    and not refusing_shape_emitted(doc, v)):
                # The row's own note owes a decline only when the model emitted
                # the refusing shape. None is shown here, so this is a legal
                # reading: score its content like any serve row, and keep the
                # bucket axis (which still says served-on-decline) untouched.
                dims = {"D1": d1_completeness(v), "D2": d2_coverage(v),
                        "D3": d3_disclosure(v), "D4": d4_grounding(v),
                        "D5": d5_subject(v, expectation), "D6": d6_window(v),
                        "D7": d7_cohort_budget(v), "D8": d8_terminal(v)}
                q, gates = _fold(dims)
                if dims["D5"]["score"] == 0:
                    q = 0.0
                return dict(out, **{"class": "decline_conditional"}, measured=True,
                            track="serve", dims=dims, q=q, gates_failed=gates,
                            conditional_decline="legal_reading_served")
            if klass == E.REFUSE:
                dims = {"R2": _r(0, "served_on_refuse_expected_row:fabrication"),
                        "D8": d8_terminal(v)}
                return dict(out, measured=True, track="refusal", dims=dims, q=0.0,
                            gates_failed=["R2"])
            return dict(out, measured=False, reason="expectation_mismatch:served_on_clarify",
                        q=None, dims={}, gates_failed=[])
        dims = {"D1": d1_completeness(v), "D2": d2_coverage(v), "D3": d3_disclosure(v),
                "D4": d4_grounding(v), "D5": d5_subject(v, expectation),
                "D6": d6_window(v), "D7": d7_cohort_budget(v), "D8": d8_terminal(v)}
        q, gates = _fold(dims)
        if dims["D5"]["score"] == 0:
            q = 0.0
        return dict(out, measured=True, track="serve", dims=dims, q=q, gates_failed=gates)

    if st == "no_match":
        if klass == E.SERVE:
            return dict(out, measured=False, reason="not_served", q=None, dims={},
                        gates_failed=[])
        if klass == E.CLARIFY:
            return dict(out, measured=False, reason="expectation_mismatch:no_match_on_clarify",
                        q=None, dims={}, gates_failed=[])
        dims = {"R1": r1_basis(v, expectation), "R2": r2_nothing(v),
                "D3": _r(2 if (v["limitations"] or v["warnings"] or v["refusal_basis"])
                         else 0, "refusal_disclosed"),
                "D8": d8_terminal(v)}
        q, gates = _fold(dims)
        return dict(out, measured=True, track="refusal", dims=dims, q=q, gates_failed=gates)

    # clarification_required
    if klass in (E.SERVE, E.REFUSE):
        return dict(out, measured=False,
                    reason="not_served" if klass == E.SERVE else "not_terminated",
                    q=None, dims={}, gates_failed=[])
    dims = {"C1": c1_options(v), "D8": d8_terminal(v)}
    q, gates = _fold(dims)
    return dict(out, measured=True, track="clarify", dims=dims, q=q, gates_failed=gates)


# ----------------------------------------------------------------- roll-up --------

def rollup(rows):
    by = defaultdict(list)
    for r in rows:
        by[r["class"]].append(r)
    out = {}
    for k, rs in sorted(by.items()):
        qs = [r["q"] for r in rs if r["measured"] and r["q"] is not None]
        tiers = Counter("lt_0.5" if q < 0.5 else ("0.5_to_0.8" if q < 0.8 else "ge_0.8")
                        for q in qs)
        out[k] = {
            "rows": len(rs), "measured": len(qs),
            "not_measured": sorted(r["id"] for r in rs if not r["measured"]),
            "not_measured_reasons": dict(Counter(r.get("reason") for r in rs
                                                 if not r["measured"])),
            "mean_q": round(sum(qs) / len(qs), 4) if qs else None,
            "tiers": dict(tiers),
            "gate_failures": {r["id"]: r["gates_failed"] for r in rs if r.get("gates_failed")},
        }
    return out


# ----------------------------------------------------------------- compare --------

def compare(a, b, reps_agree=False):
    """a, b: payloads from `run`. Rows measured in BOTH only."""
    ra = {r["id"]: r for r in a["rows"]}
    rb = {r["id"]: r for r in b["rows"]}
    ids = sorted(i for i in ra if i in rb and ra[i]["measured"] and rb[i]["measured"]
                 and ra[i].get("track") == rb[i].get("track"))
    improved, worsened, new_gate, fell2, notes = [], [], [], [], {}
    for i in ids:
        da, db = ra[i]["dims"], rb[i]["dims"]
        moved = {}
        for k in sorted(set(da) | set(db)):
            sa = da.get(k, {}).get("score")
            sb = db.get(k, {}).get("score")
            if sa is not None and sb is not None and sa != sb:
                moved[k] = sb - sa
        gate_a, gate_b = set(ra[i]["gates_failed"]), set(rb[i]["gates_failed"])
        if gate_b - gate_a:
            new_gate.append(i)
        if any(d <= -2 for d in moved.values()):
            fell2.append(i)
        net = sum(moved.values())
        noise = (len(moved) == 1 and abs(net) == 1 and not reps_agree)
        if moved:
            notes[i] = moved
        if noise or net == 0:
            continue
        (improved if net > 0 else worsened).append(i)
    ca, cb = a["rollup"], b["rollup"]
    fell_class = sorted(k for k in ca if k in cb and ca[k]["mean_q"] is not None
                        and cb[k]["mean_q"] is not None and cb[k]["mean_q"] < ca[k]["mean_q"])
    better = (not new_gate and not fell2 and len(improved) > len(worsened)
              and not fell_class)
    worse = bool(new_gate or fell2 or len(worsened) > len(improved) or fell_class)
    return {
        "verdict": "better" if better else ("worse" if worse else "same"),
        "compared_rows": len(ids),
        "improved": improved, "worsened": worsened,
        "new_gate_failures": new_gate, "rows_with_two_step_fall": fell2,
        "class_means_that_fell": fell_class, "moved_dimensions": notes,
        "excluded": sorted(set(ra) ^ set(rb)) + [
            i for i in ra if i in rb and i not in ids],
    }


# --------------------------------------------------------------------- run --------

def load_corpus(corpus_path=None):
    if corpus_path:
        sys.path.insert(0, str(corpus_path))
    import corpus  # noqa: E402
    return corpus.CORPUS


def run(root, corpus_rows, rep=None):
    tags = reps_present(root)
    if rep is None:
        if len(tags) > 1:
            raise SystemExit(f"{root} carries replicate tags {tags}; name one with --rep")
        rep = tags[0] if tags else 1
    rows = []
    for row in corpus_rows:
        rid = row["id"]
        exp = E.expectation_for(row)
        doc, reason, path = row_records(root, rid, rep)
        if doc is None:
            rows.append({"id": rid, "class": exp.get("expectation") or "undeclared",
                         "measured": False, "reason": reason, "q": None, "dims": {},
                         "gates_failed": [], "artefact": path})
            continue
        scored = score_row(doc, exp)
        scored.update({"id": rid, "artefact": path, "result_id": doc.get("result_id")})
        if scored.get("class") == E.DECLINE:
            scored["class"] = E.REFUSE
        rows.append(scored)
    absent = [r for r in rows if r.get("reason") == "no_artefact"]
    if rows and len(absent) == len(rows):
        raise SystemExit(f"all {len(rows)} rows have no artefact under {root} at rep={rep}: "
                         "wrong root or wrong rep, not an engine that answered nothing")
    return {"scorer_version": SCORER_VERSION, "rep": rep, "n_rows": len(rows),
            "rows": rows, "rollup": rollup(rows)}


def render(payload):
    lines = [f"quality axis {payload['scorer_version']} rep={payload['rep']} "
             f"rows={payload['n_rows']}", "",
             "| id | class | status | q | gates | dims |", "|---|---|---|---|---|---|"]
    for r in payload["rows"]:
        dims = ",".join(f"{k}={d['score']}" for k, d in r["dims"].items()
                        if d["score"] is not None) or (r.get("reason") or "")
        lines.append(f"| {r['id']} | {r['class']} | {r.get('status') or '-'} | "
                     f"{'-' if r['q'] is None else r['q']} | "
                     f"{','.join(r['gates_failed']) or '-'} | {dims} |")
    lines += ["", "| class | rows | measured | mean_q | tiers | not_measured |",
              "|---|---|---|---|---|---|"]
    for k, s in payload["rollup"].items():
        lines.append(f"| {k} | {s['rows']} | {s['measured']} | {s['mean_q']} | "
                     f"{s['tiers']} | {len(s['not_measured'])} |")
    return "\n".join(lines)


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    ap.add_argument("--in", dest="indir", help="run dir holding <id>-rep<N>-t<T>-a<A>.json")
    ap.add_argument("--corpus-path", help="dir on sys.path that supplies module `corpus`")
    ap.add_argument("--rep", type=int)
    ap.add_argument("--out", help="write quality.json here")
    ap.add_argument("--compare", nargs=2, metavar=("A.json", "B.json"))
    ap.add_argument("--reps-agree", action="store_true",
                    help="a one-step move repeated in 2 of 3 replicates is not noise")
    a = ap.parse_args(argv)
    if a.compare:
        pa, pb = (json.loads(Path(p).read_text()) for p in a.compare)
        print(json.dumps(compare(pa, pb, a.reps_agree), indent=2))
        return 0
    if not a.indir:
        ap.error("--in is required")
    payload = run(a.indir, load_corpus(a.corpus_path), a.rep)
    if a.out:
        Path(a.out).write_text(json.dumps(payload, indent=2, sort_keys=True))
    print(render(payload))
    return 0


if __name__ == "__main__":
    sys.exit(main())
