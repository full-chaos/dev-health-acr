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

    # r1 #3. Three separate defects, each fixed by being explicit rather than clever:
    #   (a) only the exact phrases "expect refuse"/"expect decline" were recognised, so
    #       "expected to decline" and "expect refusal" fell through to UNSCORED;
    #   (b) SERVABLE was a bare substring test, so "not SERVABLE" read as expect_serve;
    #   (c) the unservable guard was `"unservable" not in low` over the WHOLE note, so a
    #       row that merely mentioned unservable kinds lost its own SERVABLE declaration.
    #       The guard now only rejects "unservable" as the SERVABLE token itself.
    # r2 #3. The r1 regex covered exactly the two spellings the first reviewer named, so
    # "un-SERVABLE", "not currently SERVABLE", "expecting to decline" and "expected a
    # refusal" all still misread. These match the FAMILY: any inflection of expect/expects/
    # expecting/expected, optionally separated from the verb by a few words, and a negation
    # window before SERVABLE rather than only the immediately preceding token.
    _EXPECT = r"expect(?:s|ing|ed)?\b(?:\s+\w+){0,3}?\s+"
    _REFUSE_RE = re.compile(_EXPECT + r"(?:to\s+)?(?:be\s+)?(?:refuse|refused|refusal)\b")
    _DECLINE_RE = re.compile(_EXPECT + r"(?:to\s+)?(?:be\s+)?(?:decline|declined|declining|declination)\b")
    # also accept the noun-first forms: "declination expected", "refusal expected"
    _REFUSE_RE2 = re.compile(r"\brefusal\s+(?:is\s+)?expect(?:ed)?\b")
    _DECLINE_RE2 = re.compile(r"\bdeclination\s+(?:is\s+)?expect(?:ed)?\b")
    # SERVABLE as its own token: not part of "unservable", not hyphen-negated ("un-SERVABLE"),
    # and not preceded within a short window by a negation word.
    _SERVE_RE = re.compile(r"(?<![a-z-])servable\b")
    _NEGATED_SERVE_RE = re.compile(
        r"\b(?:not|non|never|no)\b[\s-]*(?:\w+[\s-]+){0,2}servable\b|\bun[-\s]servable\b")

    if _REFUSE_RE.search(low) or _REFUSE_RE2.search(low):
        cls = REFUSE
        basis = "member_kind_unservable" if "member_kind_unservable" in low else None
    elif _DECLINE_RE.search(low) or _DECLINE_RE2.search(low):
        cls = DECLINE
        basis = "named_basis" if "named basis" in low else None
    elif _SERVE_RE.search(low) and not _NEGATED_SERVE_RE.search(low):
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


# r1 #2. An identity record that could not be read is NOT evidence of a clean row.
# These states must never reach an `agree`.
UNTRUSTED_IDENTITY_STATES = {"no_artefact", "unreadable_artefact"}

# r1 #4. classify() collapses no_match and refused into `unserved`, so a bucket alone
# cannot tell a named-basis decline from a bare no-match. A row declaring
# basis=named_basis needs the terminal status to distinguish them.
NAMED_BASIS_TERMINALS = {"refused"}


def score(expectation, bucket, subject_substitution=False,
          identity_state="read", terminal_status=None):
    """Agreement of an observed bucket with the declared expectation.

    An unverifiable identity CAPS the verdict at `agree_weak`; it never improves one.
    Getting that backwards was a defect in the first fix for r1 #2: routing untrusted
    identity through an early return turned a legitimate `disagree` (a row that plainly
    failed its declared expectation) into `agree_weak` merely because its artefact was
    unreadable. Doubt about the subject cannot make a failed row look better.
    """
    verdict, why = _score_core(expectation, bucket, subject_substitution,
                               identity_state, terminal_status)
    if verdict == "agree" and identity_state in UNTRUSTED_IDENTITY_STATES:
        return "agree_weak", (
            f"{why}; but identity could not be checked ({identity_state}), so the "
            "committed subject is unverified and this is not counted as agreement")
    return verdict, why


def _score_core(expectation, bucket, subject_substitution=False,
                identity_state="read", terminal_status=None):
    """Agreement of an observed bucket with the declared expectation.

    A confirmed subject substitution is `disagree` unconditionally: serving a
    different real subject than the one asked about is never agreement with any
    declared expectation, including a "serve" one.
    """
    cls = expectation["expectation"]
    if subject_substitution:
        return "disagree", "subject substitution: a subject was committed that the row did not name"
    untrusted = identity_state in UNTRUSTED_IDENTITY_STATES
    if cls == REFUSE:
        if bucket == "unserved":
            return "agree", "refused/no_match as declared"
        if bucket == "clarification_needed":
            return "agree_weak", "did not serve, but never terminated either -- looped to MAX_TURNS instead of refusing"
        return "disagree", f"expected a refusal, observed {bucket}"
    if cls == DECLINE:
        if bucket == "unserved":
            if expectation.get("expectation_basis") == "named_basis":
                # r2 #4. This branch used to FAIL OPEN: an absent terminal status returned
                # `agree`, so a row we could not measure was scored as if it had passed. A
                # missing measurement is not a measured zero. It is now `unscored` with the
                # reason stated, and a bare no_match -- which is not a decline carrying a
                # named basis -- is a plain disagreement rather than a soft one.
                if terminal_status is None:
                    return "unscored", (
                        "row declares a named-basis decline, but the terminal status is "
                        "absent from the artefact -- the basis cannot be checked, so this "
                        "row is NOT scored rather than assumed to agree")
                if terminal_status not in NAMED_BASIS_TERMINALS:
                    return "disagree", (
                        f"row declares a decline with a named basis; terminated as "
                        f"{terminal_status}, which carries no named basis")
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


def table(rows_by_id, buckets_by_id, subs_by_id,
          states_by_id=None, terminals_by_id=None):
    """Build the expectation table for a whole run."""
    out = []
    for cid in sorted(rows_by_id):
        e = expectation_for(rows_by_id[cid])
        sub = bool(subs_by_id.get(cid))
        st = (states_by_id or {}).get(cid, "read")
        term = (terminals_by_id or {}).get(cid)
        # r1 #1. The UNSCORED short-circuit ran BEFORE the substitution check, so a
        # substituted row with no declared expectation reported `unscored` in this table
        # while its row record said `disagree` -- the verdict disagreed with itself.
        # A substitution, and an unverifiable identity, are scored on every row.
        if sub or st in UNTRUSTED_IDENTITY_STATES:
            verdict, why = score(e, buckets_by_id.get(cid), sub,
                                 identity_state=st, terminal_status=term)
        elif e["expectation"] == UNSCORED:
            verdict, why = "unscored", "row declares no expectation"
        else:
            verdict, why = score(e, buckets_by_id.get(cid), sub,
                                 identity_state=st, terminal_status=term)
        out.append({
            "corpus_id": cid,
            "family": rows_by_id[cid].get("family") or "_none",
            "expectation": e["expectation"],
            "bucket": buckets_by_id.get(cid),
            "verdict": verdict,
            "why": why,
            "subject_substitution": bool(subs_by_id.get(cid)),
            "identity_state": (states_by_id or {}).get(cid, "read"),
        })
    return out
