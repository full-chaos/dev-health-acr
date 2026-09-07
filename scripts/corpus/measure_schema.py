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


def walk(node, node_name, seen, sample_of, visits=None):
    """Record every key of every mapping, under the node it belongs to.

    `visits` counts how many times each node was SEEN, so presence is measurable: a key
    that occurs on every visit to its node is REQUIRED. That count was already being
    collected and thrown away, and discarding it is what let `receipt_id` -- observed 1597
    times and dereferenced unconditionally -- be typed but never required, so an artefact
    missing it passed the boundary and crashed the consumer.
    """
    if not isinstance(node, dict):
        return
    if visits is not None:
        visits[node_name] += 1
    for key, val in node.items():
        seen[(node_name, key)][type_of(val)] += 1
        t = type_of(val)
        if t == "object":
            child = f"{node_name}.{key}"
            sample_of[(node_name, key)] = child
            walk(val, child, seen, sample_of, visits)
        elif t == "array":
            for item in val:
                seen[(node_name, key + "[]")][type_of(item)] += 1
                if isinstance(item, dict):
                    child = f"{node_name}.{key}[]"
                    sample_of[(node_name, key + "[]")] = child
                    walk(item, child, seen, sample_of, visits)


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
    ap.add_argument("--consumers", nargs="+", required=True,
                    help="consumer modules. MANDATORY: required-ness is derived from "
                         "them, so a schema generated without them is silently weaker "
                         "than one generated with them, and the two are hard to tell "
                         "apart by looking.")
    ap.add_argument("--declared-paths", default=None,
                    help="paths consumers dereference that the artefacts never contained "
                         "(see schema_declared_paths.json). Marked unmeasured in the output.")
    ap.add_argument("--null-policy", default=None,
                    help="declared paths that ADMIT an explicit null (see "
                         "schema_null_policy.json). Nullability the engine never emits and "
                         "measurement therefore cannot supply.")
    args = ap.parse_args()

    seen = defaultdict(Counter)
    visits = Counter()
    sample_of = {}
    files = polymorphic = 0
    for root in args.roots:
        for f in sorted(Path(root).glob("**/replicate/*.json")):
            try:
                data = json.loads(f.read_text())
            except Exception:
                continue                      # unreadable input is not evidence of a type
            files += 1
            walk(data, "attempt", seen, sample_of, visits)

    if not files:
        sys.exit("no artefacts matched --roots; refusing to emit a schema from nothing")

    policy = {}
    if args.null_policy:
        doc = json.loads(Path(args.null_policy).read_text())
        policy = {e["path"]: e for e in doc.get("admit_null", [])}

    # TWO PASSES, and the order matters. The consumer sweep resolves `x["a"]["b"]` by
    # following the schema's own node links, so it needs a schema to resolve against. Pass
    # one types everything; pass two asks the consumers which of those keys they
    # dereference unconditionally; only then can `required` be decided.
    nodes = defaultdict(dict)
    unchecked = []
    applied_policy = []
    unconditional = set()
    for (node, key), counts in sorted(seen.items()):
        if sum(counts.values()) < args.min_count:
            continue
        t, nullable, observed = merge_types(counts)
        base = key[:-2] if key.endswith("[]") else key
        full = f"{node}.{key}"
        if full in policy:
            nullable = True
            pol = policy[full]
            applied_policy.append({"path": full, "consumer": pol["consumer"],
                                   "when": pol.get("when"),
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
                    if policy.get(full, {}).get("when"):
                        entry["nullable_when"] = policy[full]["when"]
                if t == "object":
                    entry["node"] = sample_of.get((node, key))
            # PRESENCE. Observed on EVERY visit to its node, AND read unconditionally by
            # a consumer. Both halves are load-bearing: "present in all 746 samples" is not
            # "required by the contract" -- marking every always-present key required
            # rejected legitimate responses that merely omit a field these runs all had.
            # What actually breaks is an unconditional `x['k']` on a key that is absent, so
            # that is what `required` means here.
            n_visits = visits.get(node, 0)
            entry["node_visits"] = n_visits
            if n_visits and sum(counts.values()) == n_visits \
                    and (node, key) in unconditional:
                entry["required"] = True
        entry["observed"] = observed

    declared = []
    declared_required = []
    if args.declared_paths:
        doc = json.loads(Path(args.declared_paths).read_text())
        for e in doc.get("required", []):
            entry = nodes[e["node"]].setdefault(e["key"], {})
            entry["required"] = True
            entry["required_declared"] = True
            entry["required_consumer"] = e["consumer"]
            declared_required.append(f'{e["node"]}.{e["key"]}')
        for e in doc.get("declared", []):
            entry = nodes[e["node"]].setdefault(e["key"], {})
            if "type" in entry:
                continue          # measurement wins: the declaration is now obsolete
            entry["type"] = e["type"]
            entry["unmeasured"] = True
            entry["consumer"] = e["consumer"]
            entry["observed"] = {}
            declared.append(f'{e["node"]}.{e["key"]}')

    # AFTER the declarations, deliberately: the sweep asks whether a field is declared
    # `unmeasured` so it can treat reads beneath it as a KNOWN gap rather than an
    # unresolvable base. Sweeping first made the generator exit on a hole it documents.
    import consumer_paths
    provisional = {n: dict(ks) for n, ks in nodes.items()}
    reached_unconditionally = set()
    for path in args.consumers:
        reads, unresolved = consumer_paths.sweep(
            Path(path).read_text(), provisional, Path(path).name)
        if unresolved:
            sys.exit("consumer reads the generator cannot resolve: "
                     + "; ".join(unresolved))
        for node, key, uncond in reads:
            if not uncond:
                continue
            unconditional.add((node, key))
            # The NODE this unconditional read leads into is reached unconditionally, and
            # so is the element node when it leads into an array. That is the CLASS:
            # `sn["kind_options"]` reaches kind_options[], so every always-present key of
            # kind_options[] is required -- including the eleven sibling `receipt_id`
            # fields that a per-path declaration missed. Round 1 named ONE of them and I
            # declared exactly that one, which is the "pin narrower than the finding"
            # failure, committed on the finding that named the class.
            rule = (provisional.get(node) or {}).get(key) or {}
            for child in (rule.get("node"), rule.get("element_node")):
                if child:
                    reached_unconditionally.add(child)

    for node, keys in nodes.items():
        node_reached = node in reached_unconditionally
        for key, entry in keys.items():
            if entry.get("type") is None:
                continue
            n_visits = entry.get("node_visits") or 0
            seen_n = sum((entry.get("observed") or {}).values())
            if not (n_visits and seen_n == n_visits):
                continue          # not always present: never required
            # Required if the KEY ITSELF is read unconditionally. NOT if merely its node
            # is: `payload["result"]` reaches the result node, and requiring every
            # always-present key there demands `answer_plan` and thirty siblings of every
            # artefact -- the presence-only rule again, one level up. What actually breaks
            # is `x["k"]` on an absent k, so that is the rule, and the sweep resolving
            # comprehensions and stashes is what makes it reach the whole class rather
            # than the one path a review happened to name.
            if (node, key) in unconditional:
                entry["required"] = True

    out = {
        "_generated_by": "scripts/corpus/measure_schema.py",
        "_why": __doc__.strip(),
        "_artefacts_measured": files,
        "_roots": list(args.roots),
        "_unchecked_polymorphic": sorted(unchecked),
        "_required_keys": sorted(f"{n}.{k}" for n, ks in nodes.items()
                                 for k, r in ks.items() if r.get("required")),
        "_nodes_reached_unconditionally": sorted(reached_unconditionally),
        "_required_rule": ("observed on every visit to its node AND its node reached by "
                           "an unconditional read (x['k'], which raises when absent) "
                           "anywhere in the consumers. Derived for the CLASS, never "
                           "declared per path. Presence alone is not a contract: a field "
                           "every one of these runs happened to carry may still be "
                           "optional, and requiring it would reject a legitimate "
                           "response, so `.get()` -- which tolerates absence and says so "
                           "-- never makes a key required."),
        "_generator_argv": ["--null-policy", args.null_policy or "",
                            "--declared-paths", args.declared_paths or "",
                            "--consumers", *args.consumers,
                            "--roots", *args.roots],
        "_declared_unmeasured": sorted(declared),
        "_declared_required": sorted(declared_required),
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
