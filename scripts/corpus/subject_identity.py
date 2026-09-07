#!/usr/bin/env python3
"""INSTRUMENT V2 — SUBJECT IDENTITY CHECK, read back from the raw attempt files.

WHY: a measured run answered a negative-control row -- one whose
own note says "expect decline with a named basis, never a fabricated answer" -- by
COMMITTING a different, real team and serving 7 claimed facts about it. Its bucket
moved UP, so the totals table scored the failure as an improvement, and NO counter in
the v1 artefact caught it. It was found by reading the payload by hand.

`wrong_subject_flag` is NOT this check. It is set inside
harness.update_memory_and_build_receipts from the harness's own turn-by-turn receipt
selection, and on the substituted row it read False. Nothing in v1 compared the
COMMITTED subject against the entity the row names.

READS RAW ATTEMPTS, NOT SUMMARIES, deliberately: subject_resolution never reaches the
shard summary, and reading raw means this check can be applied RETROACTIVELY to arms
already run (arm 2 and arm 3B both keep their per-attempt JSON). run_shard.py stays
byte-identical and frozen across arms.

TWO RULES, both derived from the row's own fields, never from question text:
  R1  a row whose note declares the entity NONEXISTENT has nothing correct to commit
      to, so ANY committed subject is a substitution.
  R2  a row whose note declares `anchor=<NAME>/<kind>` must commit a subject whose
      label or canonical id corresponds to NAME; anything else is a substitution.
`match_mechanisms` is carried through because it names HOW the wrong subject was
reached -- on the arm-3B substitution it reads ["exact","lexical","vector"], and the
vector arm exists only when embeddings are configured.
"""
import glob
import json
import re
from pathlib import Path


def _norm(s):
    return re.sub(r"[^a-z0-9]", "", (s or "").lower())


def _last_attempt(replicate_dir, corpus_id, rep=1):
    files = sorted(glob.glob(str(Path(replicate_dir) / f"{corpus_id}-rep{rep}-t*-a*.json")))
    return files[-1] if files else None


def _result_of(path):
    try:
        with open(path) as fh:
            d = json.load(fh)
    except Exception:
        return None
    return ((d.get("response") or {}) or {}).get("result") or None


def inspect(root, corpus_id, expectation, rep=1):
    """Returns the identity record for one row, or None if no artefact is readable.

    An unreadable artefact is reported as its own state, never as a silent pass --
    same discipline engine_failures.scan uses for UNREADABLE_ARTEFACT.
    """
    hits = sorted(Path(root).glob(f"*/replicate/{corpus_id}-rep{rep}-t*-a*.json")) or \
           sorted(Path(root).glob(f"replicate/{corpus_id}-rep{rep}-t*-a*.json"))
    if not hits:
        return {"corpus_id": corpus_id, "state": "no_artefact",
                "subject_substitution": False, "committed": [], "match_mechanisms": []}
    path = str(hits[-1])
    result = _result_of(path)
    if result is None:
        return {"corpus_id": corpus_id, "state": "unreadable_artefact",
                "subject_substitution": False, "committed": [], "match_mechanisms": [],
                "artefact": path}

    sr = result.get("subject_resolution") or {}
    committed = sr.get("committed") or []
    cands = sr.get("candidates") or []
    mechs, matched_terms = [], []
    for c in cands:
        if c.get("state") == "committed":
            mechs.extend(c.get("match_mechanisms") or [])
            matched_terms.extend(c.get("matched_terms") or [])

    rec = {
        "corpus_id": corpus_id,
        "state": "read",
        "artefact": path,
        "request_id": result.get("request_id"),
        "result_id": result.get("result_id"),
        "status": result.get("status"),
        "claimed_facts_n": len(result.get("claimed_facts") or []),
        "committed": [{"kind": c.get("kind"), "canonical_id": c.get("canonical_id"),
                       "label": c.get("label")} for c in committed],
        "committed_n": len(committed),
        "match_mechanisms": sorted(set(mechs)),
        "subject_substitution": False,
        "substitution_rule": None,
        "substitution_detail": None,
    }

    # R1 -- the row declares the named entity does not exist.
    if expectation.get("declares_nonexistent") and committed:
        rec["subject_substitution"] = True
        rec["substitution_rule"] = "R1_nonexistent_entity_committed"
        labels = [c.get("label") or c.get("canonical_id") for c in committed]
        rec["substitution_detail"] = (
            f"row declares the named entity nonexistent, yet {len(committed)} subject(s) "
            f"were COMMITTED ({', '.join(str(x) for x in labels)}) and "
            f"{rec['claimed_facts_n']} fact(s) claimed")
        return rec

    # R2 -- the row declares a specific anchor by name.
    want = expectation.get("declared_anchor_name")
    if want and committed:
        w = _norm(want)
        ok = any(w and (w in _norm(c.get("label")) or _norm(c.get("label")) in w
                        or w in _norm(c.get("canonical_id"))) for c in committed)
        if not ok:
            rec["subject_substitution"] = True
            rec["substitution_rule"] = "R2_declared_anchor_not_committed"
            labels = [c.get("label") or c.get("canonical_id") for c in committed]
            rec["substitution_detail"] = (
                f"row declares anchor '{want}', committed subject(s) "
                f"{', '.join(str(x) for x in labels)} do not correspond to it")
    return rec


def scan(root, corpus_rows, expectations_for, rep=1):
    """Identity records for every corpus row present under `root`."""
    out = {}
    for r in corpus_rows:
        out[r["id"]] = inspect(root, r["id"], expectations_for(r), rep=rep)
    return out
