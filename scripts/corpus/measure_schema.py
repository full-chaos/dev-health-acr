#!/usr/bin/env python3
"""Derive the artefact boundary schema FROM THE ARTEFACTS. CHAOS-5430.

The hand-written schema was an approximation of the data, and three separate review rounds
found the same defect in it: the boundary stopped one level above the code that read across
it (r5 nested result data, r9 array elements and the live body, r10 nested live shapes and
committed element fields). Each round pushed it one level deeper by hand and the next round
found the next level. Patching a predicate could not end that, because the defect was never
in a predicate -- it was that a human decided which paths existed.

So the schema is no longer written. It is MEASURED: every path, at every depth, in every
artefact, with the types that actually occur there. A path the engine emits cannot be
missing from the boundary, because the boundary is a report about what the engine emits.

NULLABILITY IS PART OF THE MEASUREMENT, and it is the reason this can be strict without
breaking scoring. `committed[].kind` occurs as a string and, in a malformed artefact, as
null. Typing it "string" rejects the artefact, and the row then scores agree_weak through
the unreadable-artefact cap instead of the disagree it earns as a subject substitution --
doubt making a failed row look BETTER. Typing it "string, nullable" rejects `kind: []` and
`kind: 1` while letting an explicit null through to substitution scoring, which is what
that row needs. Round 10's reviewer supplied that resolution.

`number` vs `int` is measured too, and bool is never either: Python's bool is an int
subclass, so a JSON `true` passes an `int` check and a `number` check unless both are
guarded. The guard belongs here, where the type is decided, not at each call site.
"""
import argparse
import json
import sys
from collections import Counter, defaultdict
from pathlib import Path

# Paths are recorded as node/key pairs so the emitted schema stays a MAPPING OF NODES, the
# shape validators.py already walks -- a measured schema that needed a new walker would be
# two changes at once, and the walker is not what was wrong.
SCALARS = {str: "string", bool: "bool", int: "int", float: "number"}


def type_of(v):
    if v is None:
        return "null"
    if isinstance(v, bool):          # BEFORE int: bool is an int subclass
        return "bool"
    if isinstance(v, dict):
        return "object"
    if isinstance(v, list):
        return "array"
    return SCALARS.get(type(v), "unknown")


def walk(node, node_name, seen, sample_of):
    """Record every key of every mapping, under the node it belongs to."""
    if not isinstance(node, dict):
        return
    for key, val in node.items():
        seen[(node_name, key)][type_of(val)] += 1
        t = type_of(val)
        if t == "object":
            child = f"{node_name}.{key}"
            sample_of[(node_name, key)] = child
            walk(val, child, seen, sample_of)
        elif t == "array":
            for item in val:
                seen[(node_name, key + "[]")][type_of(item)] += 1
                if isinstance(item, dict):
                    child = f"{node_name}.{key}[]"
                    sample_of[(node_name, key + "[]")] = child
                    walk(item, child, seen, sample_of)


def merge_types(counts):
    """A measured type plus whether null was ever observed there.

    An int-and-float column is `number`: the engine emitted both, so both are legal, and
    typing it `int` would reject real data. This is exactly the mistake an earlier draft of
    the hand-written schema made in the other direction.
    """
    nullable = "null" in counts
    kinds = {k for k in counts if k != "null"}
    if not kinds:
        return None, nullable, dict(counts)
    if kinds == {"int", "number"}:
        return "number", nullable, dict(counts)
    if len(kinds) == 1:
        return next(iter(kinds)), nullable, dict(counts)
    # Genuinely polymorphic: record it and type it as unchecked rather than pick a winner.
    # Guessing here is how a boundary starts rejecting real artefacts.
    return None, nullable, dict(counts)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--roots", nargs="+", required=True,
                    help="run roots; every */replicate/*.json beneath each is measured")
    ap.add_argument("--out", required=True)
    ap.add_argument("--min-count", type=int, default=1)
    ap.add_argument("--declared-paths", default=None,
                    help="paths consumers dereference that the artefacts never contained "
                         "(see schema_declared_paths.json). Marked unmeasured in the output.")
    ap.add_argument("--null-policy", default=None,
                    help="declared paths that ADMIT an explicit null (see "
                         "schema_null_policy.json). Nullability the engine never emits and "
                         "measurement therefore cannot supply.")
    args = ap.parse_args()

    seen = defaultdict(Counter)
    sample_of = {}
    files = polymorphic = 0
    for root in args.roots:
        for f in sorted(Path(root).glob("**/replicate/*.json")):
            try:
                data = json.loads(f.read_text())
            except Exception:
                continue                      # unreadable input is not evidence of a type
            files += 1
            walk(data, "attempt", seen, sample_of)

    if not files:
        sys.exit("no artefacts matched --roots; refusing to emit a schema from nothing")

    policy = {}
    if args.null_policy:
        doc = json.loads(Path(args.null_policy).read_text())
        policy = {e["path"]: e["consumer"] for e in doc.get("admit_null", [])}

    nodes = defaultdict(dict)
    unchecked = []
    applied_policy = []
    for (node, key), counts in sorted(seen.items()):
        if sum(counts.values()) < args.min_count:
            continue
        t, nullable, observed = merge_types(counts)
        base = key[:-2] if key.endswith("[]") else key
        full = f"{node}.{key}"
        if full in policy:
            nullable = True
            applied_policy.append({"path": full, "consumer": policy[full],
                                   "observed_nulls": observed.get("null", 0)})
        entry = nodes[node].setdefault(base, {})
        if key.endswith("[]"):
            if t is None:
                unchecked.append(f"{node}.{key} {observed}")
            else:
                entry["items"] = t
                if nullable:
                    entry["items_nullable"] = True
                if t == "object":
                    entry["element_node"] = sample_of.get((node, key))
        else:
            if t is None:
                unchecked.append(f"{node}.{key} {observed}")
                entry.pop("type", None)
            else:
                entry["type"] = t
                if nullable:
                    entry["nullable"] = True
                if t == "object":
                    entry["node"] = sample_of.get((node, key))
        entry["observed"] = observed

    declared = []
    if args.declared_paths:
        doc = json.loads(Path(args.declared_paths).read_text())
        for e in doc.get("declared", []):
            entry = nodes[e["node"]].setdefault(e["key"], {})
            if "type" in entry:
                continue          # measurement wins: the declaration is now obsolete
            entry["type"] = e["type"]
            entry["unmeasured"] = True
            entry["consumer"] = e["consumer"]
            entry["observed"] = {}
            declared.append(f'{e["node"]}.{e["key"]}')

    out = {
        "_generated_by": "scripts/corpus/measure_schema.py",
        "_why": __doc__.strip(),
        "_artefacts_measured": files,
        "_roots": list(args.roots),
        "_unchecked_polymorphic": sorted(unchecked),
        "_declared_unmeasured": sorted(declared),
        "_null_policy_applied": applied_policy,
        "_null_policy_unused": sorted(set(policy) - {a["path"] for a in applied_policy}),
        "nodes": {k: dict(sorted(v.items())) for k, v in sorted(nodes.items())},
    }
    Path(args.out).write_text(json.dumps(out, indent=1, sort_keys=False) + "\n")
    print(f"measured {files} artefacts -> {len(nodes)} nodes, "
          f"{sum(len(v) for v in nodes.values())} typed keys, "
          f"{len(unchecked)} polymorphic left unchecked")


if __name__ == "__main__":
    main()
