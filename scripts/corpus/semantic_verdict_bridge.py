#!/usr/bin/env python3
"""CHAOS-5625: publish CHAOS-5620's versioned semantic verdict beside acr's
own legacy buckets, from THIS corpus runner (harness.py / run_shard.py /
merge_corpus.py) -- see corpus/README.md's Ownership section and ask-dev's
docs/chaos-5620-semantic-verdict.md for why the scoring MACHINERY lives in
ask-dev, not here.

AGENTS.md's anti-pattern ("do not add Python runtime or contract-checking
code to this repository") is why every acceptance decision below is made by
ask-dev's `expect_schema.py` / `semantic_verdict.py`, imported at the pinned
ask-dev sha, never re-implemented here. This module is WIRING ONLY: it
locates that module (the identical convention `corpus.py` itself already
uses), reads the raw per-attempt artefacts this harness already writes, and
adapts acr's OWN scalar scorer (`expectations.py`) into the `legacy_score`
callable `semantic_verdict.build_verdict()` requires.

WHY NO NEW PIN MECHANISM. `corpus.py` (the real corpus rows) is already
supplied at run time by putting an ask-dev checkout's `corpus/` directory
ahead of this one on `sys.path` (see corpus/README.md "## Supplying a
corpus", and every proof of record's own PROVENANCE.json, which names
`ask_dev.sha` and `corpus_new_bar.source: "ask-dev main corpus/"`).
`expect_schema.py`, `schema_shim.py` and `semantic_verdict.py` (CHAOS-5620)
live in that EXACT SAME ask-dev `corpus/` directory, so the identical
`sys.path` entry that already supplies `corpus.py` supplies these for free.
A second, differently-named pin mechanism pointing at the same directory
would be two sources of truth for one fact -- see harness.py/run_shard.py's
own CORPUS_BASE precedent (CHAOS-5562: no silent default, refuse and name
what is missing).

Every function below fails CLOSED, never raises past its caller: a single
row's unreadable/absent attempt artefacts publish that row as `unscored`
with a named reason, exactly the discipline semantic_verdict.py's own audit
already uses for a malformed exchange -- never a crash that takes the
legacy buckets down with it. `resolve_ask_dev` DOES raise (`AskDevUnavailable`,
never a bare ImportError) when the pin is missing -- its caller
(merge_corpus.py) decides what that means for the run; it is NOT a MERGE
ABORT there, because the five legacy buckets are already complete and
admissible without this feature, and every caller that predates CHAOS-5625
must keep merging exactly as it always has when it carries no pin.
"""
import hashlib
import subprocess
from collections import Counter
from pathlib import Path

import expectations
import subject_identity
from attempt_order import order_attempts
from validators import load_attempt

# Bumped by hand, same discipline as semantic_verdict.SCORER_VERSION /
# expect_schema.SCHEMA_VERSION: identifies THIS adapter's own behavior
# (never expectations.py's scoring TABLE, which is identified by acr's own
# git sha, recorded separately in the published provenance).
LEGACY_SCORER_ADAPTER_VERSION = "acr-expectations-adapter-v1"


class AskDevUnavailable(Exception):
    """`expect_schema`/`semantic_verdict` could not be imported from wherever
    `corpus.py` itself already resolves from -- the identical failure mode
    `from corpus import CORPUS` already has, reported the same named way
    (see harness.py/run_shard.py's CORPUS_BASE refusal messages) rather
    than a raw ModuleNotFoundError three frames from the real cause."""


def resolve_ask_dev():
    """Import ask-dev's CHAOS-5620 modules and identify the pin actually in
    use. Raises AskDevUnavailable when they are not importable, OR when they
    import but are not a coherent, usable checkout -- a PRESENT but BROKEN
    companion must degrade to legacy-only exactly like a MISSING one (see
    merge_corpus.py's own try/except around this call): a merge that aborts
    on a broken companion instead of degrading is the one thing this
    function exists to prevent.

    Returns (expect_schema, semantic_verdict, pin) where `pin` carries
    `ask_dev_root`/`ask_dev_sha` (read from the checkout's OWN git metadata,
    never retyped by a human -- None when the checkout carries none, e.g. a
    bare extract) plus every version string a published verdict record must
    carry.
    """
    try:
        import expect_schema
        import semantic_verdict
    except ImportError as exc:
        raise AskDevUnavailable(
            "corpus/expect_schema.py and corpus/semantic_verdict.py (CHAOS-5620) "
            "are not importable. They live beside the real corpus.py in ask-dev's "
            "own corpus/ directory -- the SAME sys.path entry that supplies the "
            "real corpus.py must point at a FULL ask-dev checkout (not a copy of "
            "corpus/ alone: schema_shim.py resolves scripts/validate_json_schema.mjs "
            "and expect_schema.py resolves src/contracts/schemas/, both relative to "
            "the ask-dev repo root). See corpus/README.md \"## Supplying a corpus\"."
        ) from exc

    # A MIXED companion -- expect_schema resolved from one checkout and
    # semantic_verdict from another -- is possible whenever sys.path carries
    # more than one candidate corpus/ directory (each module name resolves
    # independently), and would otherwise publish a pin naming ONE root while
    # actually scoring with code from two. Both modules must resolve under
    # the SAME corpus/ directory before either is trusted.
    schema_dir = Path(expect_schema.__file__).resolve().parent
    verdict_dir = Path(semantic_verdict.__file__).resolve().parent
    if schema_dir != verdict_dir:
        raise AskDevUnavailable(
            f"corpus/expect_schema.py resolved from {schema_dir} but "
            f"corpus/semantic_verdict.py resolved from {verdict_dir} -- a mixed "
            "companion (two different checkouts on sys.path) is not a usable pin."
        )

    try:
        root = verdict_dir.parent
        version_fields = {
            "scorer_version": semantic_verdict.SCORER_VERSION,
            "policy_version": semantic_verdict.POLICY_VERSION,
            "schema_version": expect_schema.SCHEMA_VERSION,
        }
        # Attribute access succeeding is not the same as the attribute being
        # a usable value -- `SCORER_VERSION = None` (or `""`) raises nothing,
        # so a companion carrying one would otherwise publish
        # `available=True` with invalid version metadata, the exact
        # masquerade CHAOS-5632 exists to catch (found in review). Every
        # version field this pin promises callers ("every version string a
        # published verdict record must carry", this function's own
        # docstring) must be a non-empty string.
        bad = {k: v for k, v in version_fields.items() if not isinstance(v, str) or not v}
        if bad:
            raise AskDevUnavailable(
                f"corpus/expect_schema.py and corpus/semantic_verdict.py imported from "
                f"{verdict_dir} but carry an unusable version value: {bad!r} (expected a "
                "non-empty string for each). A companion checkout that is present but "
                "broken must be treated the same as a missing one."
            )
        pin = {
            "ask_dev_root": str(root),
            "ask_dev_sha": _git_sha(root),
            "ask_dev_dirty": _git_dirty(root),
            **version_fields,
            "legacy_scorer_version": LEGACY_SCORER_ADAPTER_VERSION,
        }
    except AskDevUnavailable:
        raise
    except Exception as exc:
        # A companion that IMPORTS but is missing a required attribute (a
        # version bump landed on one side only, a partial checkout, a stub)
        # or whose attribute access itself raises is exactly as unusable as
        # one that does not import at all, and must be named and degraded
        # the same way, not left to crash merge_corpus.py's caller.
        raise AskDevUnavailable(
            f"corpus/expect_schema.py and corpus/semantic_verdict.py imported from "
            f"{verdict_dir} but are not a usable pin: {exc!r}. A companion checkout "
            "that is present but broken must be treated the same as a missing one."
        ) from exc
    return expect_schema, semantic_verdict, pin


def _git_sha(root):
    """The ask-dev checkout's own commit, read from ITS OWN git metadata --
    never retyped by a human, the same reason merge_corpus.py's own
    provenance.json is "written by the launcher ... never retyped
    afterwards". None (never a placeholder string) when the checkout
    carries no git metadata at all -- a caller that needs to KNOW the pin
    supplies a real checkout, and a silently wrong sha is worse than an
    honest None."""
    try:
        proc = subprocess.run(
            ["git", "-C", str(root), "rev-parse", "HEAD"],
            capture_output=True, text=True, timeout=10,
        )
    except OSError:
        return None
    if proc.returncode != 0:
        return None
    sha = proc.stdout.strip()
    return sha or None


def _git_dirty(root):
    """Whether the ask-dev checkout's working tree differs from `HEAD` --
    tracked-file edits, staged changes, AND untracked files all count.
    Untracked was excluded in an earlier version of this function on the
    theory that "an untracked file changes nothing that gets imported" --
    false whenever a tracked module imports a NEW, not-yet-committed sibling
    module: the untracked file is then very much part of what Python
    actually loads, and excluding it let a checkout that imports worktree-
    only code report `ask_dev_dirty=False` (found in review). A stray
    unrelated untracked file elsewhere in the checkout now also counts,
    trading a false positive there for never a false negative on the code
    path that matters -- the same direction CHAOS-5633 already picked for
    `ask_dev_sha` staying visible over a false "trustworthy" clean read.

    A dirty checkout does not stop `ask_dev_sha` from being reported --
    the sha is still a fact about the checkout -- but publishing it next to
    a clean HEAD, with no signal that the code Python actually loaded may
    differ from what that sha alone implies, is misleading in exactly the
    way CHAOS-5633 found. None (never a bare False) when this cannot be
    determined at all -- the same discipline `_git_sha` uses for a checkout
    with no git metadata."""
    try:
        proc = subprocess.run(
            ["git", "-C", str(root), "status", "--porcelain"],
            capture_output=True, text=True, timeout=10,
        )
    except OSError:
        return None
    if proc.returncode != 0:
        return None
    return bool(proc.stdout.strip())


def corpus_version_of(corpus_module):
    """sha256 of the real corpus.py actually imported -- so a rescore under a
    changed corpus is never silently compared to one under the old corpus,
    the identical reason every proof of record already hand-records
    `corpus_py_sha256` in its PROVENANCE.json (this makes that fact
    self-reported by the code, not retyped by a human)."""
    try:
        return hashlib.sha256(Path(corpus_module.__file__).read_bytes()).hexdigest()
    except (OSError, TypeError):
        return None


def legacy_score(row, bucket, status, subject_substitution=False,
                  identity_state="read", disclosed_basis=None):
    """The `legacy_score` callable `semantic_verdict.build_verdict()`
    requires, adapting acr's REAL scalar table (expectations.py) -- never a
    second implementation of it (see this module's docstring). `row` is
    exactly the shape `semantic_verdict.score_branch` passes: either the
    original corpus row (scalar `expect`) or a copy with `expect`/`basis`
    overridden to one `any_of` alternative -- both are exactly what
    `expectations.expectation_for` already reads."""
    e = expectations.expectation_for(row)
    return expectations.score(
        e, bucket,
        subject_substitution=subject_substitution,
        identity_state=identity_state,
        terminal_status=status,
        disclosed_basis=disclosed_basis,
    )


def attempts_for(root, corpus_id, rep):
    """Every recorded attempt for one row/rep under `root`, in the harness's
    own numeric attempt order -- the SAME glob/order convention
    subject_identity.inspect() already uses for the identical files, never a
    second listing of where attempts live.

    Returns (ok, attempts, reason). `ok=False` (no artefact at all, nothing
    sequenceable, or an unreadable file) never raises -- it is exactly the
    scan-level gap `verdict_for_row` publishes as `unscored`/named-reason,
    the same discipline `subject_identity.inspect` already uses for
    `no_artefact`/`unreadable_artefact`.
    """
    hits = []
    for g in subject_identity.ATTEMPT_GLOBS:
        hits.extend(Path(root).glob(g.format(cid=corpus_id, rep=rep)))
    ordered, unsequenced = order_attempts(hits, on_unparseable="skip")
    if not ordered:
        reason = f"unsequenced:{len(unsequenced)}" if unsequenced else "no_artefact"
        return False, None, reason
    attempts = []
    for f in ordered:
        ok, a, reason = load_attempt(f)
        if not ok:
            return False, None, f"unreadable_artefact:{reason}"
        attempts.append(a)
    return True, attempts, None


def verdict_for_row(row, root, corpus_id, rep, bucket, status,
                     semantic_verdict, pin, corpus_version,
                     subject_substitution=False, identity_state="read",
                     disclosed_basis=None):
    """The published semantic_verdict record for one row/rep. NEVER RAISES:
    a scan-level gap (no artefact, unreadable file) is folded into the
    SAME `unscored`/named-reason shape semantic_verdict.py's own audit uses
    for a malformed exchange, so a caller can publish it exactly like any
    other row without a second branch for "the scan itself failed"."""
    ok, attempts, reason = attempts_for(root, corpus_id, rep)
    if not ok:
        audit = {"family_relation": "unknown", "window_binding": "unknown",
                  "family_confirmation": "unavailable", "reason": reason}
        final = None
    else:
        audit = semantic_verdict.audit_window_exchange(attempts)
        # The row's own LAST recorded attempt -- the SAME "last attempt" every
        # other reader of these files uses (run_shard.last_attempt_file), never
        # a second definition of which attempt is terminal. A failure attempt
        # carries no `result`, and score_branch already treats a missing/non-
        # dict `final` as `family_unavailable`, never a crash.
        final = (attempts[-1].get("response") or {}).get("result")

    return semantic_verdict.build_verdict(
        row, bucket, status, final, audit, legacy_score,
        corpus_version, pin["legacy_scorer_version"],
        subject_substitution=subject_substitution,
        identity_state=identity_state,
        disclosed_basis=disclosed_basis,
    )


def aggregate(records):
    """The PROVENANCE aggregate columns over every published semantic_verdict
    record in the run: verdict counts, family_relation counts, the confirmed-
    family-verified count (always 0 until the wire carries a positive
    acceptance link -- see semantic_verdict.py's module docstring -- but
    published explicitly rather than omitted, so its arrival is visible the
    moment it stops being zero), and the unscored count broken out by reason
    (an unscored total alone hides whether it is one cause or ten)."""
    records = list(records)
    unscored = [r for r in records if r["verdict"] == "unscored"]
    return {
        "verdict_counts": dict(Counter(r["verdict"] for r in records)),
        "family_relation_counts": dict(Counter(r["family_relation"] for r in records)),
        "confirmed_family_verified": sum(
            1 for r in records if r["family_confirmation"] == "confirmed"),
        "unscored_count": len(unscored),
        "unscored_reasons": dict(Counter(r["reason"] for r in unscored)),
    }
