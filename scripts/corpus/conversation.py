#!/usr/bin/env python3
"""Per-turn scoring for AUTHORED multi-turn conversations. Pure: reads no file, opens no socket.

A conversation is `{id, shape, turns: [...]}`. Every turn has its own text and its own
declared `expect`; turn N>1 follows turn N-1's RESULT (a parent reference), it is never a
receipt answering an engine-posed question -- except a turn carrying `redeem`, which
selects one candidate the previous (clarify) turn offered.

SCORING RULES (the SERVE/DECLINE/CLARIFY vocabulary is expectations.py's, unchanged --
the (expect, terminal) table is imported, never copied):

  * serve   -> VERDICTS[(serve, terminal)]; when the turn declares `expected_subject`, the
               served result must COMMIT that canonical_id, else `disagree` (wrong subject).
  * decline -> VERDICTS[(decline, terminal)].
  * clarify -> PROVISIONAL. A clarification that is offered but never redeemed is a dead end,
               not a success: the turn scores `agree` only when the NEXT turn (which must be a
               `redeem` turn) selected the offered option AND the engine served an answer with
               claimed facts for the declared subject. Otherwise `disagree`.
  * no expect -> `unscored` (never inferred), reported, excluded from the denominator.

Conversation results NEVER fold into the single-turn series' counts: own table, own
denominator, own golden file (golden_conversation_verdicts.json).
"""
from expectations import CLARIFY, DECLINE, EXPECT_SERVE, VERDICTS, terminal_key
from validators import EXPECT_VALUES

SERVED = {"complete", "partial", "degraded", "answered"}
AGREE, AGREE_WEAK, DISAGREE, UNSCORED, NOT_MEASURED = (
    "agree", "agree_weak", "disagree", "unscored", "not_measured")

# conversation_identity_check verdicts
OK, WRONG_SUBJECT_CARRIED, SILENT_SUBSTITUTION = "ok", "wrong_subject_carried", "silent_substitution"


def validate_conversation(conv):
    """(ok, reason). Structural checks only; refuses a conversation that cannot be scored."""
    if not isinstance(conv, dict) or not isinstance(conv.get("id"), str) or not conv["id"]:
        return False, "conversation needs a non-empty string id"
    turns = conv.get("turns")
    if not isinstance(turns, list) or len(turns) < 2:
        return False, f"{conv['id']}: needs >=2 turns"
    for i, t in enumerate(turns, start=1):
        where = f"{conv['id']} turn{i}"
        if not isinstance(t, dict) or t.get("n") != i:
            return False, f"{where}: turn.n must equal its position"
        if not isinstance(t.get("text"), str) or not t["text"]:
            return False, f"{where}: text must be a non-empty string"
        if (t.get("parent") or None) != (None if i == 1 else f"turn{i - 1}"):
            return False, f"{where}: parent must be {'absent' if i == 1 else f'turn{i - 1}'}"
        exp = t.get("expect")
        if exp is not None and exp not in EXPECT_VALUES:
            return False, f"{where}: expect must be one of {sorted(EXPECT_VALUES)} or None"
        red = t.get("redeem")
        if red is not None:
            if i == 1 or not (isinstance(red, dict) and isinstance(red.get("canonical_id"), str)
                              and red["canonical_id"]):
                return False, f"{where}: redeem needs a canonical_id and a preceding turn"
            if turns[i - 2].get("expect") != CLARIFY:
                return False, f"{where}: redeem must follow a clarify turn"
            if t.get("expect") != EXPECT_SERVE:
                return False, f"{where}: a redemption turn must expect serve"
            es = t.get("expected_subject") or {}
            if es.get("canonical_id") != red["canonical_id"]:
                return False, f"{where}: redemption must expect the subject it redeems"
        if exp == CLARIFY:
            nxt = turns[i] if i < len(turns) else None
            if not nxt or nxt.get("redeem") is None:
                return False, f"{where}: a clarify turn needs a redemption turn after it"
    return True, None


def committed_ids(result):
    sr = (result or {}).get("subject_resolution") or {}
    return [s.get("canonical_id") for s in (sr.get("committed") or []) if isinstance(s, dict)]


def offered_ids(result):
    sr = (result or {}).get("subject_resolution") or {}
    return [((c.get("subject") or {}).get("canonical_id"))
            for c in (sr.get("candidates") or []) if isinstance(c, dict)]


def observe(http, result):
    """The scoring-relevant facts of one turn, small enough to store in a summary."""
    ok = isinstance(http, int) and 200 <= http < 300 and isinstance(result, dict) and bool(result)
    return {"http": http,
            "status": (result or {}).get("status") if ok else None,
            "claimed_facts_n": len((result or {}).get("claimed_facts") or []) if ok else 0,
            "committed": committed_ids(result) if ok else [],
            "offered": offered_ids(result) if ok else []}


def _terminal(obs):
    status = obs.get("status")
    if not isinstance(status, str):
        return None
    bucket = None
    if status in SERVED:
        bucket = "served_with_data" if obs.get("claimed_facts_n") else "served_degraded"
    if status == "clarification_required":
        status = "clarification"
        return status
    return terminal_key(status, bucket)


def score_turn(turn, obs):
    """One turn's own verdict from its own expectation. Total: anything unnamed is
    `unscored` with a reason, never a guessed agreement. A clarify turn's `agree` here is
    PROVISIONAL -- `score_conversation` settles it from the redemption."""
    expect = turn.get("expect")
    if expect is None:
        return {"verdict": UNSCORED, "why": "no_expectation"}
    key = _terminal(obs)
    if key is None:
        return {"verdict": DISAGREE, "why": f"no_terminal(http={obs.get('http')})"}
    cell = VERDICTS.get((expect, key))
    if cell is None:
        return {"verdict": UNSCORED, "why": f"no_table_cell({expect},{key})"}
    verdict, why = cell
    if expect == EXPECT_SERVE and verdict in (AGREE, AGREE_WEAK):
        want = (turn.get("expected_subject") or {}).get("canonical_id")
        if want and want not in obs.get("committed", []):
            return {"verdict": DISAGREE, "why": "wrong_subject"}
    if expect == CLARIFY and verdict == AGREE:
        want = [c["canonical_id"] for c in (turn.get("clarify_candidates") or [])]
        missing = [w for w in want if w not in obs.get("offered", [])]
        if missing:
            return {"verdict": DISAGREE, "why": f"expected_candidates_not_offered({len(missing)})"}
        remembered = [c["canonical_id"] for c in (turn.get("clarify_candidates") or [])
                      if c.get("remembered")]
        if remembered and obs.get("offered") and obs["offered"][0] != remembered[0]:
            return {"verdict": DISAGREE, "why": "remembered_subject_not_offered_first"}
    return {"verdict": verdict, "why": why}


def conversation_identity_check(turns, observations):
    """Cross-turn identity verdict per turn (additive to score_turn, never replaces it).

    wrong_subject_carried  a continuation turn (declares no anchor of its own) was served
                           bound to a DIFFERENT subject than the nearest preceding turn that
                           committed one.
    silent_substitution    a turn declaring a subject other than the nearest preceding committed
                           subject was served without the previous turn being a clarification.
    """
    out, prior = [], None
    for i, (turn, obs) in enumerate(zip(turns, observations)):
        verdict = OK
        got = obs.get("committed") or []
        served = obs.get("status") in SERVED
        if i > 0 and served and got and prior is not None:
            declared = (turn.get("expected_subject") or {}).get("canonical_id")
            continuation = turn.get("anchor_kind") in (None, "") and not turn.get("redeem")
            prev_clarified = observations[i - 1].get("status") == "clarification_required"
            if continuation and set(got) != set(prior):
                verdict = WRONG_SUBJECT_CARRIED
            elif declared and declared not in prior and not prev_clarified:
                verdict = SILENT_SUBSTITUTION
        out.append(verdict)
        if got:
            prior = got
    return out


def score_conversation(conv, recorded):
    """`recorded` = per-turn dicts `{obs, ran, redeem_unavailable}` in turn order (one
    replicate). Returns per-turn results plus `redeemed` per clarify turn."""
    turns = conv["turns"]
    if len(recorded) != len(turns):
        raise ValueError(f"{conv['id']}: {len(recorded)} recorded turns for {len(turns)} declared")
    res = []
    for t, rec in zip(turns, recorded):
        if not rec.get("ran"):
            res.append({"n": t["n"], "verdict": NOT_MEASURED,
                        "why": rec.get("why") or "turn_not_run", "redeemed": None})
        else:
            s = score_turn(t, rec["obs"])
            res.append({"n": t["n"], **s, "redeemed": None})
    for i, t in enumerate(turns):
        if t.get("expect") == CLARIFY and res[i]["verdict"] == AGREE:
            nxt = res[i + 1]
            served_data = nxt["verdict"] == AGREE
            res[i]["redeemed"] = served_data
            if not served_data:
                res[i]["verdict"], res[i]["why"] = DISAGREE, f"clarify_not_redeemed({nxt['why']})"
        elif t.get("expect") == CLARIFY:
            res[i]["redeemed"] = False
    ident = conversation_identity_check(turns, [r["obs"] if r.get("ran") else {} for r in recorded])
    for r, v in zip(res, ident):
        r["identity"] = v
    return res
