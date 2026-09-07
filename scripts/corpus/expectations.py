#!/usr/bin/env python3
"""INSTRUMENT V2 — per-row DECLARED EXPECTATION, derived from the row's OWN fields.

WHY THIS EXISTS:
arm 3B scored 12 of 36 rows as "improved" by bucket rank while 7 of those rows had
STOPPED doing the thing they exist to test. Bucket rank is not a quality ordering
for this corpus, because roughly a third of the rows are negative or refuse-expected
controls where SERVING is the failure. This module makes the declared expectation a
first-class, scored axis so a disagreement is a regression regardless of bucket.

SOURCE OF TRUTH: corpus.py's own `note`, `id`, `anchor_kind` and `variant` fields.
Question text (`text`) is NEVER read here and never reaches an artefact — the
standing rule is rows by id only.

EXPECTATION CLASSES, each keyed off a literal phrase the corpus author wrote:
  EXPECT_REFUSE   note contains "expect refuse"   (the member_kind_unservable class)
  EXPECT_DECLINE  note contains "expect decline"  (the nonexistent-entity and I6-illegal negatives)
  EXPECT_SERVE    note contains "SERVABLE"
  UNSCORED        no declared expectation -- reported, never scored

THREE-VALUED VERDICT, and why it is not the two values that were asked for:
agree / disagree alone cannot express the difference between "declined cleanly" and
"never fabricated but also never terminated". That distinction is exactly the arm-3B
finding (8 rows stopped terminating and looped to MAX_TURNS), so collapsing it would
hide the defect this module was built to catch. `agree_weak` is therefore a third
value, always reported separately and never counted as agreement.
"""
import re

REFUSE, DECLINE, SERVE, UNSCORED = "expect_refuse", "expect_decline", "expect_serve", "unscored"

# anchor=<NAME>/<kind> appears in the note of every row that names a resolvable anchor.
_ANCHOR_RE = re.compile(r"anchor=([^/;]+)/([a-z_]+)", re.I)


def expectation_for(row):
    """Classify one corpus row. Returns a dict; never raises on an unknown shape."""
    note = row.get("note") or ""
    low = note.lower()
    if "expect refuse" in low:
        cls, basis = REFUSE, "member_kind_unservable" if "member_kind_unservable" in low else None
    elif "expect decline" in low:
        cls, basis = DECLINE, "named_basis" if "named basis" in low else None
    elif "servable" in low and "unservable" not in low:
        cls, basis = SERVE, None
    else:
        cls, basis = UNSCORED, None

    m = _ANCHOR_RE.search(note)
    anchor_name, anchor_kind = (m.group(1).strip(), m.group(2).strip().lower()) if m else (None, None)

    return {
        "corpus_id": row["id"],
        "expectation": cls,
        "expectation_basis": basis,
        # A row whose note says the named entity does not exist. Committing ANY
        # subject on such a row is a substitution by definition -- there is nothing
        # correct to commit to.
        "declares_nonexistent": "nonexistent" in low,
        "is_negative_control": low.startswith("negative") or "negative" in low or row["id"].startswith("neg-"),
        "declared_anchor_name": anchor_name,
        "declared_anchor_kind": anchor_kind or (row.get("anchor_kind") or None),
        "forbids_fabrication": "never a fabricated answer" in low,
        "note": note,
    }


def score(expectation, bucket, subject_substitution=False):
    """Agreement of an observed bucket with the declared expectation.

    A confirmed subject substitution is `disagree` unconditionally: serving a
    different real subject than the one asked about is never agreement with any
    declared expectation, including a "serve" one.
    """
    cls = expectation["expectation"]
    if subject_substitution:
        return "disagree", "subject substitution: a subject was committed that the row did not name"
    if cls == REFUSE:
        if bucket == "unserved":
            return "agree", "refused/no_match as declared"
        if bucket == "clarification_needed":
            return "agree_weak", "did not serve, but never terminated either -- looped to MAX_TURNS instead of refusing"
        return "disagree", f"expected a refusal, observed {bucket}"
    if cls == DECLINE:
        if bucket == "unserved":
            return "agree", "declined as declared"
        if bucket == "clarification_needed":
            return "agree_weak", "no fabricated answer, but no named-basis decline either"
        if bucket == "error":
            return "disagree", "engine error, not a decline"
        return "disagree", f"expected a decline, observed {bucket}"
    if cls == SERVE:
        if bucket == "served_with_data":
            return "agree", "served with facts as declared"
        if bucket == "served_degraded":
            return "agree_weak", "served but with zero claimed facts (hollow serve)"
        return "disagree", f"expected a serve, observed {bucket}"
    return "unscored", "row declares no expectation"


def table(rows_by_id, buckets_by_id, subs_by_id):
    """Build the expectation table for a whole run."""
    out = []
    for cid in sorted(rows_by_id):
        e = expectation_for(rows_by_id[cid])
        if e["expectation"] == UNSCORED:
            verdict, why = "unscored", "row declares no expectation"
        else:
            verdict, why = score(e, buckets_by_id.get(cid), bool(subs_by_id.get(cid)))
        out.append({
            "corpus_id": cid,
            "family": rows_by_id[cid].get("family") or "_none",
            "expectation": e["expectation"],
            "bucket": buckets_by_id.get(cid),
            "verdict": verdict,
            "why": why,
            "subject_substitution": bool(subs_by_id.get(cid)),
        })
    return out
