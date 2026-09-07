#!/usr/bin/env python3
"""THE ordering of attempt artefacts. One helper, used everywhere attempts are sequenced.

Attempts must be ordered by their RECORDED SEQUENCE, never by path. Two separate defects
came from path sorting: rooting replays under the shard tree put `reclassify/...` before
the original it replaced, and lexicographic order puts `t10` before `t9`, so "the last
attempt" was the wrong file in both cases. The fix lived in one module and the identical
defect stayed in the other, so it lives here now and both import it.
"""
import re
from pathlib import Path

# The directory a deadline replay is written under. A replay always sorts AFTER the
# original attempt it supersedes, whatever its indices are.
REPLAY_DIRNAME = "reclassify"

_SEQ = re.compile(r"-rep(\d+)-t(\d+)-a(\d+)\.json$")


def attempt_sort_key(path):
    name = Path(path).name
    m = _SEQ.search(name)
    rep, turn, att = (int(m.group(1)), int(m.group(2)), int(m.group(3))) if m else (0, 0, 0)
    is_replay = 1 if f"/{REPLAY_DIRNAME}/" in str(path).replace("\\", "/") else 0
    return (is_replay, rep, turn, att, str(path))


def order_attempts(paths):
    """Sequence order. Deduplicates, because the same file can match several globs."""
    return sorted(set(str(p) for p in paths), key=attempt_sort_key)
