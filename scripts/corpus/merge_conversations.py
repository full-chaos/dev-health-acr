#!/usr/bin/env python3
"""Merge conversation replicates into their OWN verdict file and report table.

Never touches merge_corpus.py, golden_verdicts.json or the 36-row SWD denominator. The
denominator here is scored CONVERSATION TURNS (unscored and not_measured are counted apart,
never hidden inside it).

Fails loudly (exit 2, output still written) when: no summaries exist; a required rep is
missing; a rep lacks a conversation another rep has; any turn is `not_measured`.

Usage: merge_conversations.py --in DIR --out FILE [--reps N]
"""
import argparse
import json
import re
import sys
from pathlib import Path

HERE = Path(__file__).parent
sys.path.insert(0, str(HERE))

import conversation as C  # noqa: E402


def _shape_code(shape):
    m = re.match(r"\(([a-z])\)", shape or "")
    return m.group(1) if m else "?"


def merge(root, convs, reps):
    by_id = {c["id"]: c for c in convs}
    problems, rows = [], []
    for rep in range(1, reps + 1):
        f = Path(root) / f"rep{rep}" / "conversation-summary.json"
        if not f.is_file():
            problems.append(f"missing summary for rep{rep}")
            continue
        summ = json.loads(f.read_text())
        got = {r["id"]: r for r in summ["records"]}
        for cid in by_id:
            if cid not in got:
                problems.append(f"rep{rep}: no record for {cid}")
                continue
            res = C.score_conversation(by_id[cid], got[cid]["turns"])
            rows.append({"id": cid, "shape": _shape_code(by_id[cid].get("shape")),
                         "rep": rep, "turns": res,
                         "redeemed": [r["redeemed"] for r in res if r["redeemed"] is not None]})
    tally = {k: 0 for k in (C.AGREE, C.AGREE_WEAK, C.DISAGREE, C.UNSCORED, C.NOT_MEASURED)}
    identity = {C.WRONG_SUBJECT_CARRIED: 0, C.SILENT_SUBSTITUTION: 0}
    for r in rows:
        for t in r["turns"]:
            tally[t["verdict"]] += 1
            if t["identity"] in identity:
                identity[t["identity"]] += 1
    scored = tally[C.AGREE] + tally[C.AGREE_WEAK] + tally[C.DISAGREE]
    if tally[C.NOT_MEASURED]:
        problems.append(f"{tally[C.NOT_MEASURED]} turn(s) not measured")
    if not rows:
        problems.append("no conversation replicates merged")
    return {"series": "conversation", "reps": reps, "denominator_scored_turns": scored,
            "tally": tally, "identity": identity, "rows": rows, "problems": problems}


def render(out):
    lines = ["| conversation | shape | rep | per-turn verdict | redeemed |", "|---|---|---|---|---|"]
    for r in out["rows"]:
        per = " / ".join(f"t{t['n']}:{t['verdict']}" for t in r["turns"])
        red = ",".join("yes" if x else "no" for x in r["redeemed"]) or "-"
        lines.append(f"| {r['id']} | {r['shape']} | {r['rep']} | {per} | {red} |")
    t = out["tally"]
    lines.append(f"\nscored turns (own denominator): {out['denominator_scored_turns']}  "
                 f"agree={t['agree']} agree_weak={t['agree_weak']} disagree={t['disagree']}  "
                 f"unscored={t['unscored']} not_measured={t['not_measured']}  "
                 f"identity={out['identity']}")
    return "\n".join(lines)


def main(argv):
    ap = argparse.ArgumentParser()
    ap.add_argument("--in", dest="inp", required=True)
    ap.add_argument("--out", required=True)
    ap.add_argument("--reps", type=int, default=3)
    a = ap.parse_args(argv)
    import harness  # noqa: E402  (needs CORPUS_BASE only at post time)
    try:
        convs = harness.load_conversations()
    except harness.MissingConversations as e:
        sys.exit(str(e))
    out = merge(a.inp, convs, a.reps)
    Path(a.out).write_text(json.dumps(out, indent=2))
    print(render(out))
    if out["problems"]:
        print("PROBLEMS: " + "; ".join(out["problems"]), file=sys.stderr)
        sys.exit(2)


if __name__ == "__main__":
    main(sys.argv[1:])
