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

CHAOS-5722: the ask-dev scorer's `persisted_semantic_state(result_id)`
adapter parameter (semantic_verdict.build_verdict/score/score_branch) is
built and supplied FROM HERE, never from ask-dev -- ask-dev has no
connection of its own to acr's trial store, by design (see
semantic_verdict.py's own module docstring). `make_persisted_semantic_state_adapter`
reads the SAME trial-postgres env recipe scripts/trial/common.sh already
establishes (ACR_TEST_TRIAL_PG_HOST/PORT/USER/PASSWORD/DB) and shells out to
`psql`, the same read-only-query mechanism scripts/trial/common.sh itself
already uses for a trial-store check (AGENTS.md's Python anti-pattern is why
this is `psql` via subprocess, never a new Python postgres driver
dependency -- there is no Python package manifest in this repo to declare
one in). When the env recipe is not fully present (any hosted CI run that
does not carry a trial-store binding) the adapter is not built at all --
`merge_corpus.py` passes `persisted_semantic_state=None`, the exact
adapter-omitted path semantic_verdict.py already defines, so a run with no
trial-store binding scores `unscored`/`semantic_state_absent` on every
`any_of` serve branch, exactly as it did before this ticket, never a merge
abort.
"""
import hashlib
import inspect
import json
import os
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
        # a usable value -- `SCORER_VERSION = None`, `""`, or a whitespace-only
        # string all raise nothing, so a companion carrying one would
        # otherwise publish `available=True` with invalid version metadata,
        # the exact masquerade CHAOS-5632 exists to catch. Every version
        # field this pin promises callers ("every version string a published
        # verdict record must carry", this function's own docstring) must be
        # a string with non-whitespace content.
        bad = {k: v for k, v in version_fields.items() if not isinstance(v, str) or not v.strip()}
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
    tracked-file edits, staged changes, AND untracked files all count. A
    tracked module can import a NEW, not-yet-committed sibling module, and
    that sibling is then very much part of what Python actually loads even
    though it is untracked -- so an untracked-files exclusion here would
    miss exactly the checkouts most likely to differ from HEAD. A stray
    unrelated untracked file elsewhere in the checkout also counts under
    this rule, trading a false positive there for never a false negative on
    the code path that matters -- the same direction CHAOS-5633 already
    picked for `ask_dev_sha` staying visible over a false "trustworthy"
    clean read.

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


# CHAOS-5722: the standing trial-postgres env recipe scripts/trial/common.sh
# already establishes and exports (see that file's `_kiac_env_PG_*`
# handling and deploy/local/trial-data.sh's `dsn --env` output) -- reused
# here verbatim, never a second env-var naming for the same fact.
_TRIAL_PG_ENV_VARS = (
    "ACR_TEST_TRIAL_PG_HOST", "ACR_TEST_TRIAL_PG_PORT", "ACR_TEST_TRIAL_PG_USER",
    "ACR_TEST_TRIAL_PG_PASSWORD", "ACR_TEST_TRIAL_PG_DB",
)

# acr internal/contextfabric/semantic_state.go: SemanticStateMaxEncodedBytes.
# A persisted row over this bound is something the engine's OWN writer would
# have refused at capture (Save refuses an oversized snapshot -- see that
# file's "HOW IT IS BOUNDED" doc comment) -- if this query ever returns more
# than that, the row cannot be a real accepted snapshot and this adapter
# must not hand it to the scorer as if it were one.
_MAX_PERSISTED_STATE_BYTES = 65536


def trial_postgres_env_present():
    """Whether the FULL standing trial-postgres env recipe is present --
    partial credentials (e.g. host+port but no password) are refused the
    same as none at all, never a best-effort connection attempt with
    whatever happens to be set."""
    return all(os.environ.get(name) for name in _TRIAL_PG_ENV_VARS)


def make_persisted_semantic_state_adapter(semantic_verdict_module, run=subprocess.run):
    """Build the `persisted_semantic_state(result_id) -> dict | None` adapter
    `semantic_verdict.build_verdict()` takes (CHAOS-5722), reading acr's OWN
    trial-postgres row for `result_id` via a read-only `psql` query -- see
    this module's own docstring for why `psql` (subprocess), never a new
    Python postgres driver dependency.

    Returns `None` (never partially configured) when
    `trial_postgres_env_present()` is false -- the caller
    (merge_corpus.py) is expected to pass that `None` straight through to
    `semantic_verdict.build_verdict(persisted_semantic_state=...)`, which
    already treats "no adapter" as "no persisted row", never a crash.

    The returned adapter callable NEVER raises anything but
    `semantic_verdict_module.PersistedSemanticStateUnreadable` -- a `psql`
    invocation failure, a non-zero exit, an oversized result, or a body
    that does not decode as a JSON object are all "this row's persisted
    state cannot be trusted right now", the identical bucket the row being
    genuinely corrupt falls into, per this module's own "fails CLOSED,
    never raises past its caller" discipline. `result_id` itself is passed
    to `psql` ONLY via a `-v` bind variable substituted through `:'name'`
    (`psql`'s own literal-quoting substitution, not string interpolation),
    so it is never concatenated into the SQL text.
    """
    if not trial_postgres_env_present():
        return None

    host = os.environ["ACR_TEST_TRIAL_PG_HOST"]
    port = os.environ["ACR_TEST_TRIAL_PG_PORT"]
    user = os.environ["ACR_TEST_TRIAL_PG_USER"]
    password = os.environ["ACR_TEST_TRIAL_PG_PASSWORD"]
    db = os.environ["ACR_TEST_TRIAL_PG_DB"]

    def persisted_semantic_state(result_id):
        if not isinstance(result_id, str) or not result_id:
            # The scorer itself already guards this (see
            # semantic_verdict._score_persisted_family_confirmation) -- this
            # is defense in depth, never relied on as the only guard.
            return None
        env = {**os.environ, "PGPASSWORD": password,
               "PGCONNECT_TIMEOUT": os.environ.get("PGCONNECT_TIMEOUT", "15")}
        query = ("select semantic_state::text from acr.context_fabric_investigation_results "
                 "where result_id = :'result_id'")
        try:
            proc = run(
                ["psql", "-h", host, "-p", port, "-U", user, "-d", db,
                 "-v", f"result_id={result_id}", "-At", "-c", query],
                capture_output=True, text=True, timeout=15, env=env,
            )
        except (OSError, subprocess.TimeoutExpired) as exc:
            raise semantic_verdict_module.PersistedSemanticStateUnreadable(
                f"{result_id}: psql invocation failed: {exc}") from exc
        if proc.returncode != 0:
            raise semantic_verdict_module.PersistedSemanticStateUnreadable(
                f"{result_id}: psql exited {proc.returncode}: {proc.stderr.strip()}")
        raw = proc.stdout.strip()
        if not raw:
            # NULL semantic_state (the common case -- most rows predate M2,
            # or ended before interpretation) and "no row for this
            # result_id at all" both read the same way through `-At`: both
            # are ABSENT, never unreadable.
            return None
        if len(raw.encode("utf-8")) > _MAX_PERSISTED_STATE_BYTES:
            raise semantic_verdict_module.PersistedSemanticStateUnreadable(
                f"{result_id}: persisted semantic_state is {len(raw.encode('utf-8'))} bytes, "
                f"over the {_MAX_PERSISTED_STATE_BYTES}-byte bound")
        try:
            decoded = json.loads(raw)
        except json.JSONDecodeError as exc:
            raise semantic_verdict_module.PersistedSemanticStateUnreadable(
                f"{result_id}: semantic_state did not decode as JSON: {exc}") from exc
        if not isinstance(decoded, dict):
            raise semantic_verdict_module.PersistedSemanticStateUnreadable(
                f"{result_id}: semantic_state decoded to {type(decoded).__name__}, not an object")
        return decoded

    return persisted_semantic_state


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
                     disclosed_basis=None, persisted_semantic_state=None):
    """The published semantic_verdict record for one row/rep. NEVER RAISES:
    a scan-level gap (no artefact, unreadable file) is folded into the
    SAME `unscored`/named-reason shape semantic_verdict.py's own audit uses
    for a malformed exchange, so a caller can publish it exactly like any
    other row without a second branch for "the scan itself failed".

    `persisted_semantic_state` (CHAOS-5722, optional) is forwarded to
    `semantic_verdict.build_verdict()` unchanged -- the caller
    (merge_corpus.py) builds it once per run via
    `make_persisted_semantic_state_adapter` and passes the SAME adapter (or
    `None`) to every row, never a new one per row.
    """
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

    build_kwargs = dict(
        subject_substitution=subject_substitution,
        identity_state=identity_state,
        disclosed_basis=disclosed_basis,
    )
    # CHAOS-5722: forward the adapter ONLY when the resolved ask-dev pin's
    # own `build_verdict` actually declares the parameter. A pin from
    # before this ticket has no named `persisted_semantic_state` parameter
    # to catch it -- it would fall into that `build_verdict`'s own
    # `**identity` catch-all instead and be forwarded straight into
    # `legacy_score`, which does not accept it either (a mixed-version pin
    # otherwise crashes every row, not just degrades this one feature).
    if "persisted_semantic_state" in inspect.signature(semantic_verdict.build_verdict).parameters:
        build_kwargs["persisted_semantic_state"] = persisted_semantic_state
    return semantic_verdict.build_verdict(
        row, bucket, status, final, audit, legacy_score,
        corpus_version, pin["legacy_scorer_version"],
        **build_kwargs,
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
