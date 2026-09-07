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

# The directory name reclassify_deadlines writes replays into. Kept here so ordering can
# put replays after originals without importing that module (which needs a corpus).
REPLAY_HINT = "reclassify"


# r1 #11. reclassify_deadlines re-runs a deadlined row and writes its raw attempts
# under <root>/<REPLAY_DIRNAME>/<id>/replicate/, but rewrites the row inside the
# ORIGINAL shard summary. The identity scan only globbed the shard trees, so it kept
# reading pre-reclassification artefacts while the summary described the re-run --
# the two halves of one verdict describing different attempts. These globs are the
# single place that knows where attempts can live.
EXTRA_ATTEMPT_GLOBS = (
    "reclassify/*/replicate/{cid}-rep{rep}-t*-a*.json",
    "*/reclassify/*/replicate/{cid}-rep{rep}-t*-a*.json",
)
ATTEMPT_GLOBS = (
    "*/replicate/{cid}-rep{rep}-t*-a*.json",
    "replicate/{cid}-rep{rep}-t*-a*.json",
) + EXTRA_ATTEMPT_GLOBS


def _norm(s):
    return re.sub(r"[^a-z0-9]", "", (s or "").lower())


def _last_attempt(replicate_dir, corpus_id, rep=1):
    files = sorted(glob.glob(str(Path(replicate_dir) / f"{corpus_id}-rep{rep}-t*-a*.json")))
    return files[-1] if files else None


RESULT, FAILURE, UNPARSEABLE = "result", "failure", "unparseable"


def classify_attempt(attempt):
    """The SINGLE definition of what an attempt is. Used by identity, the failure
    scanner and the merge, so the three can never disagree about the same file.

    result       a parsed attempt carrying a result document
    failure      a parsed attempt carrying a failure envelope, or an HTTP failure status
                 -- a NORMAL artefact; the engine answered, unsuccessfully
    unparseable  the bytes could not be read as an attempt at all

    Rounds 2 and 3 both landed here. First `no result` was treated as unreadable, which
    swept in every retried rejection. Then the narrowing let `{"response": {}}` count as
    readable while a terminal `response.failure` was still called unreadable. Both ends
    were wrong because "readable" was being decided in two places; it is decided here.
    """
    if not isinstance(attempt, dict):
        return UNPARSEABLE, None
    resp = attempt.get("response")
    if isinstance(resp, dict):
        if resp.get("result"):
            return RESULT, resp["result"]
        if resp.get("failure"):
            return FAILURE, None
    status = attempt.get("status")
    if isinstance(status, int) and status >= 400:
        return FAILURE, None
    if resp in (None, {}) and status is None:
        return UNPARSEABLE, None
    if isinstance(resp, dict) and not resp:
        # a parsed attempt with an EMPTY response envelope carries no outcome at all;
        # we cannot say what it committed, so it is not evidence of a clean row.
        return UNPARSEABLE, None
    return UNPARSEABLE, None


def _read_attempt(path):
    """(parsed_ok, result). A well-formed attempt that carries a FAILURE instead of a
    result is parsed_ok=True with result=None.

    The first fix for r2 #2 tainted a row whenever any attempt yielded no `result`, which
    swept in every legitimate 4xx/5xx attempt -- a 422 the harness later retried into a
    200 is a normal, fully readable artefact, not an unreadable one. Conflating "the
    engine returned a failure" with "we could not read the file" would have marked a
    large share of rows unverifiable and quietly capped their verdicts.
    """
    try:
        with open(path) as fh:
            d = json.load(fh)
    except Exception:
        return UNPARSEABLE, None
    return classify_attempt(d)


def _result_of(path):
    return _read_attempt(path)[1]


def _attempt_order(path):
    """Order attempts by RECORDED SEQUENCE, never by path.

    Round 3: moving replays under the shards root made `sorted()` place
    `reclassify/<id>/...` before `shard-00/...`, so `hits[-1]` selected the ORIGINAL
    attempt and identity scored the pre-reclassification result -- the defect the replay
    move existed to fix, reintroduced through sort order. Sequence comes from the turn and
    attempt indices in the filename; a replay sorts after every original by construction.
    """
    name = Path(path).name
    m = re.search(r"-rep(\d+)-t(\d+)-a(\d+)\.json$", name)
    rep, turn, att = (int(m.group(1)), int(m.group(2)), int(m.group(3))) if m else (0, 0, 0)
    is_replay = 1 if f"/{REPLAY_HINT}/" in str(path).replace("\\", "/") else 0
    return (is_replay, rep, turn, att, str(path))


def inspect(root, corpus_id, expectation, rep=1):
    """Returns the identity record for one row, or None if no artefact is readable.

    An unreadable artefact is reported as its own state, never as a silent pass --
    same discipline engine_failures.scan uses for UNREADABLE_ARTEFACT.
    """
    hits = []
    for g in ATTEMPT_GLOBS:
        hits.extend(Path(root).glob(g.format(cid=corpus_id, rep=rep)))
    hits = sorted(set(hits), key=_attempt_order)
    if not hits:
        return {"corpus_id": corpus_id, "state": "no_artefact",
                "subject_substitution": False, "committed": [], "match_mechanisms": []}
    # r1 #6. Only the lexically LAST attempt was inspected, so a wrong subject committed
    # on an earlier turn vanished when a later turn ended without committing anything.
    # Every attempt is now examined; the terminal attempt still supplies the row's
    # headline fields, but a substitution ANYWHERE in the chain is a substitution.
    path = str(hits[-1])
    terminal_class, result = _read_attempt(path)
    if terminal_class == UNPARSEABLE:
        return {"corpus_id": corpus_id, "state": "unreadable_artefact",
                "subject_substitution": False, "committed": [], "match_mechanisms": [],
                "artefact": path}

    # A terminal FAILURE attempt is readable and carries no result document: the engine
    # answered, unsuccessfully. It has no subject resolution, which is not the same as an
    # unreadable artefact. (Found by the round-3 history matrix, which crashed here.)
    result = result or {}
    sr = result.get("subject_resolution") or {}
    committed = list(sr.get("committed") or [])
    mechs, matched_terms = [], []
    seen = {(c.get("kind"), c.get("canonical_id")) for c in committed}
    # r2 #2. The r1 fix only reported `unreadable_artefact` when the TERMINAL attempt was
    # malformed; an unreadable EARLIER attempt was silently skipped and a readable terminal
    # attempt set state="read", so the cap never fired on a mixed history and a genuine
    # `agree` escaped. Any unreadable attempt in the chain taints the whole record: we do
    # not know what that attempt committed.
    unreadable = []
    for f in hits:
        cls, r = _read_attempt(str(f))
        if cls == UNPARSEABLE:
            unreadable.append(str(f))
            continue
        if not r:
            continue   # a FAILURE attempt: normal, carries no subject resolution
        s = r.get("subject_resolution") or {}
        for c in (s.get("committed") or []):
            k = (c.get("kind"), c.get("canonical_id"))
            if k not in seen:
                seen.add(k)
                committed.append(c)
        for c in (s.get("candidates") or []):
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

    if unreadable:
        rec["state"] = "unreadable_artefact"
        rec["unreadable_attempts"] = unreadable

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

    # R2 -- the row declares a specific anchor by name AND kind.
    # r1 #5. The old test was a substring match in BOTH directions and ignored kind, so
    # anchor "Platform" accepted a committed "Platform Engineering", and anchor
    # "Alpha"/team accepted a repository called "Alpha". Both are substitutions. The
    # match is now exact on the normalised label, or an exact segment of the canonical
    # id, and the declared kind must match when the row declares one.
    want = expectation.get("declared_anchor_name")
    want_kind = expectation.get("declared_anchor_kind")
    if want and committed:
        w = _norm(want)

        def _matches(c):
            # r2 #5. This used to read `if want_kind and c.get("kind") and ...`, so a
            # committed subject with kind None or "" skipped the check entirely and
            # satisfied a declared anchor kind. An absent kind is not a matching kind:
            # when the row declares one, the commit must state it and it must agree.
            if want_kind and c.get("kind") != want_kind:
                return False
            if _norm(c.get("label")) == w:
                return True
            # canonical ids are colon-delimited; accept an exact SEGMENT, never a substring
            return w in [_norm(seg) for seg in str(c.get("canonical_id") or "").split(":")]

        ok = any(_matches(c) for c in committed)
        if not ok:
            rec["subject_substitution"] = True
            rec["substitution_rule"] = "R2_declared_anchor_not_committed"
            labels = [c.get("label") or c.get("canonical_id") for c in committed]
            rec["substitution_detail"] = (
                f"row declares anchor '{want}'"
                + (f" of kind '{want_kind}'" if want_kind else "")
                + ", committed subject(s) "
                + ", ".join(f"{x.get('label')!r}({x.get('kind')})" for x in committed)
                + " do not match it")
    return rec


def scan(root, corpus_rows, expectations_for, rep=1):
    """Identity records for every corpus row present under `root`."""
    out = {}
    for r in corpus_rows:
        out[r["id"]] = inspect(root, r["id"], expectations_for(r), rep=rep)
    return out


def kind_observations(row, committed):
    """r1 #7. R3 is an OBSERVATION, and it must record every wrong-kind commitment.

    The old condition only fired when the requested kind was absent from the entire
    committed set, so an extra wrong-kind subject was dropped whenever one committed
    subject happened to match. R3 never scores a row; under-recording it silently
    removed evidence that nothing else carried.
    """
    want = (row or {}).get("requested_kind")
    if not want or not committed:
        return []
    return [{"kind": c.get("kind"), "canonical_id": c.get("canonical_id"),
             "label": c.get("label")}
            for c in committed if c.get("kind") and c.get("kind") != want]
