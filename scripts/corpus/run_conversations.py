#!/usr/bin/env python3
"""Run ONE replicate of every authored conversation against the live rig.

Sibling entry point of run_shard.py: the 36-row single-turn series and its scripts are not
touched. Writes `<CORPUS_CONV_DIR>/rep<N>/conversation-summary.json` (scoring inputs) and one
attempt artefact per POST under `<CORPUS_CONV_DIR>/rep<N>/replicate/`. Exits non-zero when the
corpus has no CONVERSATIONS section or any planned conversation produced no record -- a run
that measured nothing must not look clean.

Usage: run_conversations.py <rep> [conversation-id ...]
"""
import json
import os
import sys
from pathlib import Path

HERE = Path(__file__).parent
sys.path.insert(0, str(HERE))
if __name__ == "__main__" and not os.environ.get("CORPUS_BASE"):
    sys.exit("CORPUS_BASE is not set -- refusing to start. There is no default rig leg.")

import harness  # noqa: E402


def main(argv):
    if not argv:
        sys.exit("usage: run_conversations.py <rep> [conversation-id ...]")
    try:
        harness.require_base()
        rep = int(argv[0])
        convs = harness.load_conversations()
    except (harness.MissingCorpusBase, harness.MissingConversations, ValueError) as e:
        sys.exit(str(e))
    want = argv[1:]
    unknown = [w for w in want if w not in {c["id"] for c in convs}]
    if unknown:
        sys.exit(f"unknown conversation id(s): {unknown}")
    planned = [c for c in convs if not want or c["id"] in want]
    root = Path(os.environ.get("CORPUS_CONV_DIR") or (HERE / "conv")) / f"rep{rep}"
    records = []
    try:
        for i, conv in enumerate(planned, 1):
            print(f"=== conversation [{i}/{len(planned)}] {conv['id']} rep{rep} ===", flush=True)
            records.append(harness.run_conversation(conv, rep, root / "replicate"))
    except harness.ServedBuildMismatch as e:
        sys.exit(str(e))
    root.mkdir(parents=True, exist_ok=True)
    with open(root / "conversation-summary.json", "w") as f:
        json.dump({"rep": rep, "planned_ids": [c["id"] for c in planned], "records": records},
                  f, indent=2)
    got = {r["id"] for r in records}
    missing = [c["id"] for c in planned if c["id"] not in got]
    if missing or not records:
        print(f"INCOMPLETE: {len(missing)} conversation(s) without a record", file=sys.stderr)
        sys.exit(2)
    print(f"DONE rep{rep}: {len(records)} conversations -> {root}/conversation-summary.json")


if __name__ == "__main__":
    main(sys.argv[1:])
