#!/usr/bin/env python3
"""Per-row DECLARED EXPECTATION and its agreement scoring.

WHY THIS IS A TABLE AND NOT A PARSER
------------------------------------
Expectations used to be inferred by matching phrases inside the row's free-text `note`.
Three consecutive review rounds found the same class of defect in that parsing, each time
a neighbour of the case fixed the round before: "not only SERVABLE" scored nothing,
"not expected to be SERVABLE" scored as a serve, "expect not to decline" scored as a
decline, "unexpected refusal" scored as a refusal. Patching predicates could not converge,
because free text is not a schema.

The expectation is now a STRUCTURED FIELD the corpus declares (`expect`, `basis`,
`anchor`, `nonexistent`) and this module reads it directly. There is no regex. A row that
declares nothing is `unscored` with the reason `no_expectation` -- never inferred.

WHY SCORING IS A TOTAL FUNCTION
-------------------------------
The same rounds found the verdict FAILING OPEN three times: an absent terminal status, and
then two branches that still returned `agree` for it. A per-branch guard is the wrong
shape -- it is only ever as complete as the last review. VERDICTS is a table keyed by
(expect, terminal_status) over the harness's whole terminal vocabulary. Anything the table
does not name is `unscored` with a reason. Agreement therefore has to be declared to be
granted; it cannot be reached by falling off the end of a chain of ifs.
"""

from validators import validate_corpus_row  # noqa: E402

SERVE, REFUSE, DECLINE, CLARIFY = "serve", "refuse", "decline", "clarify"
UNSCORED = "unscored"
INVALID = "invalid_expectation"

# Back-compat aliases: earlier callers spelled these expect_* .
EXPECT_SERVE, EXPECT_REFUSE, EXPECT_DECLINE = SERVE, REFUSE, DECLINE

# Every terminal payload status the harness can record, plus the buckets classify() emits.
# A status outside this set is unknown to the table and therefore unscored.
# r5: INSTANCES, not prefixes. `startswith("clarification_required")` scored
# `clarification_required(future)` as agree -- an unknown terminal reaching agreement, the
# same class already removed for http_*. A terminal not named here is unscored until
# someone authors it.
CLARIFICATION_TERMINALS = {
    "clarification_required(max_turns_exhausted)",
}
# Everything that counts as "the engine asked for clarification", including the bare form
# a turn chain can end on. The bucketer imports this so one vocabulary serves both.
CLARIFICATION_VALUES = CLARIFICATION_TERMINALS | {"clarification_required"}
ERROR_TERMINALS = {
    "http_502:acr_investigation_failed",
    "http_400:acr_rejected_request",
}

# The COHERENCE relation: which buckets can accompany which terminal status. An
# (incoherent) pair means the two halves of the row disagree about what happened, and a
# scorer that trusts one half over the other is guessing. Authored to match
# golden_verdicts.json `_coherence`; a pin asserts the two are identical.
COHERENCE = {
    "complete": {"served_with_data", "served_degraded"},
    "answered": {"served_with_data", "served_degraded"},
    "partial": {"served_with_data", "served_degraded"},
    "degraded": {"served_with_data", "served_degraded"},
    "no_match": {"unserved"},
    "refused": {"unserved"},
    "clarification_required(max_turns_exhausted)": {"clarification_needed"},
    "http_502:acr_investigation_failed": {"error"},
    "http_400:acr_rejected_request": {"error"},
}

TERMINAL_STATUSES = {
    "complete", "partial", "degraded", "answered",          # served
    "no_match", "refused",                                  # not served
} | CLARIFICATION_TERMINALS | ERROR_TERMINALS

_A = ("agree", None)
_W = ("agree_weak", None)
_D = ("disagree", None)


def terminal_key(terminal_status, bucket=None):
    """Map a terminal status to a table key.

    A SERVED status splits on whether the row actually claimed facts: classify() already
    makes that call (served_with_data vs served_degraded, the "hollow serve"), so the
    bucket is authoritative for that split when it is supplied. Collapsing the two lost
    the hollow-serve distinction and turned three agree_weaks into agrees.
    """
    if not isinstance(terminal_status, str) or not terminal_status:
        return None
    if terminal_status in ("complete", "partial", "degraded", "answered"):
        # The hollow-serve split is a property of the ANSWER (did it claim facts), which
        # classify() already decided. The bucket is consulted only to choose between two
        # SERVED keys -- never to rescue an absent or unknown status.
        if bucket in ("served_degraded", "served_with_data"):
            return bucket
        return "served_degraded" if terminal_status == "degraded" else "served_with_data"
    if terminal_status in ERROR_TERMINALS:
        return "error"
    if terminal_status in ("no_match", "refused"):
        return terminal_status
    if terminal_status in CLARIFICATION_TERMINALS:
        return "clarification"
    return None


# (expect, bucket-or-status) -> verdict. EXHAUSTIVE BY CONSTRUCTION: an agreement exists
# only where this table names one.
VERDICTS = {
    (SERVE, "served_with_data"): ("agree", "served with facts as declared"),
    (SERVE, "served_degraded"): ("agree_weak", "served, but with zero claimed facts "
                                               "(hollow serve)"),
    (SERVE, "no_match"): ("disagree", "expected a serve, terminated as no_match"),
    (SERVE, "refused"): ("disagree", "expected a serve, was refused"),
    (SERVE, "clarification"): ("disagree", "expected a serve, exhausted turns instead"),

    (REFUSE, "refused"): ("agree", "refused as declared"),
    (REFUSE, "no_match"): ("agree", "no_match: did not serve, as declared"),
    (REFUSE, "served_with_data"): ("disagree", "expected a refusal, served instead"),
    (REFUSE, "served_degraded"): ("disagree", "expected a refusal, served instead"),
    (REFUSE, "clarification"): ("agree_weak", "did not serve, but never terminated -- "
                                              "exhausted turns instead of refusing"),

    (DECLINE, "refused"): ("agree", "declined as declared"),
    (DECLINE, "no_match"): ("agree", "no_match: declined, as declared"),
    (DECLINE, "served_with_data"): ("disagree", "expected a decline, served instead"),
    (DECLINE, "served_degraded"): ("disagree", "expected a decline, served instead"),
    (DECLINE, "clarification"): ("agree_weak", "no fabricated answer, but no decline either"),

    (CLARIFY, "clarification"): ("agree", "asked for clarification as declared"),
    (CLARIFY, "served_with_data"): ("disagree", "expected a clarification, served instead"),
    (CLARIFY, "served_degraded"): ("disagree", "expected a clarification, served instead"),
    (CLARIFY, "no_match"): ("disagree", "expected a clarification, terminated as no_match"),
    (CLARIFY, "refused"): ("disagree", "expected a clarification, was refused"),
}

# A declared BASIS narrows one cell: a decline that must carry a named basis is satisfied
# only by an explicit refusal, not by a bare no_match.
NAMED_BASIS_OVERRIDES = {
    (DECLINE, "no_match"): ("disagree", "a named-basis decline is not satisfied by a bare "
                                        "no_match"),
    # Ruling 2: a refusal that declares a basis must STATE it. A bare no_match did not
    # fabricate anything, but it did not disclose the declared basis either -- reported as
    # its own weak kind rather than folded into agreement.
    (REFUSE, "no_match"): ("agree_weak", "nothing fabricated, but the declared basis was "
                                         "not stated"),
}

UNTRUSTED_IDENTITY_STATES = {"no_artefact", "unreadable_artefact"}

# agree_weak is REPORTED SPLIT: three distinct behaviours share the verdict and folding
# them lost the finding the instrument exists to produce. Keyed off the reason the scorer
# already returns, so the kind cannot drift from the explanation beside it.
WEAK_KINDS = {
    "nothing fabricated, but the declared basis was not stated": "weak_basis_unstated",
    "did not serve, but never terminated -- exhausted turns instead of refusing":
        "weak_never_terminated",
    "no fabricated answer, but no decline either": "weak_never_terminated",
    "served, but with zero claimed facts (hollow serve)": "weak_hollow_serve",
}


def weak_kind_for(verdict, why):
    if verdict != "agree_weak":
        return None
    for phrase, kind in WEAK_KINDS.items():
        if phrase in why:
            return kind
    for phrase, kind in (("declared basis was not stated", "weak_basis_unstated"),
                         ("never terminated", "weak_never_terminated"),
                         ("no decline", "weak_never_terminated"),
                         ("zero claimed facts", "weak_hollow_serve"),
                         ("hollow", "weak_hollow_serve"),
                         ("identity could not be checked", "weak_identity_unverified")):
        if phrase in why:
            return kind
    return "weak_unclassified"


def expectation_for(row):
    """Read the row's declared expectation. No inference, no text parsing.

    The declaration is VALIDATED here (r4): a malformed one is `invalid_expectation` and
    is never scored. Removing the parser stopped the guessing but left the field trusted
    absolutely, so a list where a string belonged crashed the scorer and a string
    "false" read as True.
    """
    ok, reason = validate_corpus_row(row)
    if not ok:
        return {
            "corpus_id": (row or {}).get("id") if isinstance(row, dict) else None,
            "expectation": INVALID,
            "expectation_basis": None,
            "declares_nonexistent": False,
            "declared_anchor_name": None,
            "declared_anchor_kind": None,
            "invalid_reason": reason,
            "note": "",
        }
    anchor = row.get("anchor") or None
    return {
        "corpus_id": row.get("id"),
        "expectation": row.get("expect") or UNSCORED,
        "expectation_basis": row.get("basis"),
        "declares_nonexistent": bool(row.get("nonexistent")),
        "declared_anchor_name": (anchor or {}).get("label"),
        "declared_anchor_kind": (anchor or {}).get("kind"),
        "note": row.get("note") or "",
    }


def score(expectation, bucket, subject_substitution=False,
          identity_state="read", terminal_status=None):
    """Total function. Agreement is granted only where VERDICTS names it.

    `bucket` is accepted for callers that only have the classify() bucket; when
    `terminal_status` is supplied it is authoritative, because classify() collapses
    no_match and refused into one bucket and a declared basis needs them apart.
    """
    cls = expectation.get("expectation") or UNSCORED
    # r5: the INVALID check runs FIRST. It used to sit after the substitution branch, so a
    # row whose declaration was malformed still produced `disagree` -- a verdict derived
    # from a declaration we had already judged unreadable. An invalid row is not scored,
    # whatever else is true of it.
    if cls == INVALID:
        return "unscored", (
            f"invalid_expectation: the row's declaration is malformed "
            f"({expectation.get('invalid_reason')}) -- not scored")
    if subject_substitution:
        return "disagree", ("subject substitution: a subject was committed that the row "
                            "did not name")
    if cls == UNSCORED:
        return "unscored", "no_expectation: the row declares none"

    # r4: the bucket fallback is DELETED. It was the fail-open path that survived four
    # rounds -- an absent or unknown terminal status reached `agree` through the bucket,
    # and the domain pin could not see it because the pin recomputed this same fallback.
    # An unrecognised terminal status is now unscored, full stop.
    if not isinstance(terminal_status, str):
        return "unscored", (
            f"terminal status is absent or not a string ({type(terminal_status).__name__})"
            " -- the expectation cannot be checked, so this row is NOT scored")
    if terminal_status not in COHERENCE or bucket not in COHERENCE.get(terminal_status, ()):
        if terminal_status in COHERENCE:
            return "unscored", (
                f"incoherent_row: terminal status {terminal_status!r} cannot occur with "
                f"bucket {bucket!r}; the row's two halves disagree, so it is not scored")

    key = terminal_key(terminal_status, bucket)
    if key is None:
        return "unscored", (
            f"terminal status is absent or unrecognised ({terminal_status!r}) -- the "
            "expectation cannot be checked, so this row is NOT scored rather than "
            "assumed to agree")

    if key == "error":
        return "disagree", "engine error; the declared expectation was not reached"

    if expectation.get("expectation_basis") and (cls, key) in NAMED_BASIS_OVERRIDES:
        verdict, why = NAMED_BASIS_OVERRIDES[(cls, key)]
    else:
        hit = VERDICTS.get((cls, key))
        if hit is None:
            return "unscored", (
                f"no verdict is declared for expect={cls!r} against {key!r}; unscored "
                "rather than assumed")
        verdict, why = hit

    if verdict == "agree" and identity_state in UNTRUSTED_IDENTITY_STATES:
        return "agree_weak", (
            f"{why}; but identity could not be checked ({identity_state}), so the "
            "committed subject is unverified and this is not counted as agreement")
    return verdict, why


def table(rows_by_id, buckets_by_id, subs_by_id, states_by_id=None, terminals_by_id=None):
    out = []
    for cid in sorted(rows_by_id):
        e = expectation_for(rows_by_id[cid])
        verdict, why = score(e, buckets_by_id.get(cid), bool(subs_by_id.get(cid)),
                             identity_state=(states_by_id or {}).get(cid, "read"),
                             terminal_status=(terminals_by_id or {}).get(cid))
        out.append({
            "corpus_id": cid,
            "family": rows_by_id[cid].get("family") or "_none",
            "expectation": e["expectation"],
            "bucket": buckets_by_id.get(cid),
            "verdict": verdict,
            "why": why,
            "subject_substitution": bool(subs_by_id.get(cid)),
            "identity_state": (states_by_id or {}).get(cid, "read"),
            "weak_kind": weak_kind_for(verdict, why),
        })
    return out
