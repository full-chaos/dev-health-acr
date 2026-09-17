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

CHAOS-5826: sampling alone can UNDER-type a field the canonical contract already settles.
`cohort.members[].drivers[].value` is a genuine 0..1 ratio by design (declared `number` in
contracts/jsonschema/v1, enforced 0..1 by acr's own write-path validator) -- every artefact
this file had samples from at generation time happened to carry a whole-number value there,
so pure observation locked it to `int`, and a later contractually-valid fraction then reads
as a boundary violation instead of the served answer it is. `canonical_field_types` below
walks the canonical schema itself for exactly the fields it declares, and `main` lets that
walk override a sample-inferred type wherever the two disagree -- sampling stays the only
authority for a field the canonical schema leaves open (oneOf, or not in the contract).
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


_JSON_SCHEMA_TYPE_MAP = {"integer": "int", "number": "number", "string": "string",
                          "boolean": "bool", "object": "object", "array": "array"}


def _json_schema_scalar_type(schema):
    """(type_or_None, nullable), reading ONLY the schema's own `type` keyword.

    Never allOf/if/then/oneOf refinements: those narrow a VALUE under a condition (one
    signal's fixed `weight`), not the type every instance of the field carries. A schema
    with no single resolvable scalar type (oneOf, or no `type` at all) returns None, the
    same "leave it to measurement" contract merge_types already keeps for polymorphic data.
    """
    t = schema.get("type")
    if t is None:
        return None, False
    if isinstance(t, list):
        nonnull = [x for x in t if x != "null"]
        if len(nonnull) != 1:
            return None, "null" in t
        return _JSON_SCHEMA_TYPE_MAP.get(nonnull[0]), "null" in t
    return _JSON_SCHEMA_TYPE_MAP.get(t), False


def _resolve_json_schema_ref(ref, base_dir, doc, file_cache):
    """(schema, doc, base_dir) a `$ref` points at -- same-file `#/$defs/X` or cross-file
    `other.json#/$defs/X`, contracts/jsonschema/v1's own cross-reference shape (e.g.
    CohortMemberDriver is defined once, in context_fabric_common.v1.schema.json, and every
    investigation-result schema version reaches it by file ref rather than a second copy)."""
    file_part, _, pointer = ref.partition("#")
    if file_part:
        key = str((base_dir / file_part).resolve())
        if key not in file_cache:
            file_cache[key] = json.loads(Path(key).read_text())
        doc = file_cache[key]
        base_dir = Path(key).parent
    node = doc
    for part in pointer.strip("/").split("/"):
        if part:
            node = node[part]
    return node, doc, base_dir


def canonical_field_types(entry_path, root_node_name):
    """Walk a canonical contracts/jsonschema/v1 schema into the SAME
    {node: {key: {"type":..., "minimum":..., "maximum":...}}} shape merge_types() infers
    from artefact samples, keyed by the identical dotted node names walk() produces, so the
    two merge field for field in main().

    Scalar leaf `type` declarations are emitted for every field the schema settles. An
    array-of-objects or nested-object field is also emitted, but only its LINK to the child
    node the walk recurses into (`items`/`element_node` or `node`, the identical keys an
    observed field carries) -- never the child's own key set or element counts, which stay
    entirely sample-derived. Wiring the link is what keeps every child node this walk
    reaches attached to the tree main() emits: an unwired child is a node no validator can
    reach from the root, however precisely its own fields are typed. The canonical schema is
    walked THROUGH every array/object property to reach the scalars nested inside it
    (cohort.members[].drivers[].value is four levels down).
    """
    entry_path = Path(entry_path)
    root_key = str(entry_path.resolve())
    file_cache = {root_key: json.loads(entry_path.read_text())}
    out = defaultdict(dict)
    walked_nodes = set()

    def walk_schema(schema, doc, base_dir, node_name):
        if "$ref" in schema:
            resolved, doc, base_dir = _resolve_json_schema_ref(
                schema["$ref"], base_dir, doc, file_cache)
            walk_schema(resolved, doc, base_dir, node_name)
            return
        props = schema.get("properties")
        if not isinstance(props, dict) or node_name in walked_nodes:
            return
        walked_nodes.add(node_name)
        for key, sub in props.items():
            resolved, sub_doc, sub_base = sub, doc, base_dir
            if "$ref" in sub:
                resolved, sub_doc, sub_base = _resolve_json_schema_ref(
                    sub["$ref"], base_dir, doc, file_cache)
            t, nullable = _json_schema_scalar_type(resolved)
            if t == "array":
                items = resolved.get("items", {})
                item_resolved, item_doc, item_base = items, sub_doc, sub_base
                if "$ref" in items:
                    item_resolved, item_doc, item_base = _resolve_json_schema_ref(
                        items["$ref"], sub_base, sub_doc, file_cache)
                if isinstance(item_resolved.get("properties"), dict):
                    child = f"{node_name}.{key}[]"
                    # The parent key is wired to its child exactly as an observed
                    # array-of-objects field is wired -- setdefault-merged in main(), so a
                    # sample-derived link this same field already carries always wins.
                    out[node_name][key] = {"type": "array", "items": "object",
                                           "element_node": child}
                    walk_schema(item_resolved, item_doc, item_base, child)
            elif t == "object":
                if isinstance(resolved.get("properties"), dict):
                    child = f"{node_name}.{key}"
                    out[node_name][key] = {"type": "object", "node": child}
                    walk_schema(resolved, sub_doc, sub_base, child)
            elif t:
                field = {"type": t}
                if nullable:
                    field["nullable"] = True
                if "minimum" in resolved:
                    field["minimum"] = resolved["minimum"]
                if "maximum" in resolved:
                    field["maximum"] = resolved["maximum"]
                out[node_name][key] = field
            # else: no single resolvable scalar type (oneOf, absent) -- sampling governs.

    walk_schema(file_cache[root_key], file_cache[root_key], entry_path.parent, root_node_name)
    return dict(out)


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
    ap.add_argument("--canonical-entry", default=None,
                    help="a canonical contracts/jsonschema/v1 schema file "
                         "whose own declared scalar types and numeric bounds govern over a "
                         "sample-inferred type wherever it covers a field -- see "
                         "canonical_field_types' own docstring.")
    ap.add_argument("--canonical-node-prefix", default="attempt.response.result",
                    help="the measured node --canonical-entry's root object corresponds to.")
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

    # The canonical contract wins over a sample-inferred type wherever it
    # covers a field, applied AFTER declared-paths so it overrides a stale "measurement
    # wins" declared type too, not just a fresh sample-derived one. Only scalar `type`
    # (and minimum/maximum) are ever touched here -- see canonical_field_types.
    canonical_overridden = []
    if args.canonical_entry:
        canonical = canonical_field_types(args.canonical_entry, args.canonical_node_prefix)
        for node, keys in canonical.items():
            for key, decl in keys.items():
                entry = nodes[node].setdefault(key, {})
                if entry.get("type") != decl["type"]:
                    canonical_overridden.append(
                        f"{node}.{key} {entry.get('type')!r}->{decl['type']!r}")
                entry["type"] = decl["type"]
                entry["canonical_type"] = True
                if "minimum" in decl:
                    entry["minimum"] = decl["minimum"]
                if "maximum" in decl:
                    entry["maximum"] = decl["maximum"]
                # Additive only, like every other canonical override here: a field the
                # contract admits null on gains that admission if sampling never granted
                # it (fact_scope_census[].authorized_population_count, absent from every
                # sample the schema was generated from); a field sampling already saw
                # null on keeps that, whatever the contract's own type list says.
                if decl.get("nullable") and not entry.get("nullable"):
                    entry["nullable"] = True
                # A container field's own LINK to its child node is wired only where no
                # sample ever wired one -- setdefault, never overwrite, so an observed
                # field's own element_node/node keeps governing its own shape.
                if decl["type"] == "array":
                    entry.setdefault("items", decl.get("items", "object"))
                    if decl.get("element_node"):
                        entry.setdefault("element_node", decl["element_node"])
                elif decl["type"] == "object" and decl.get("node"):
                    entry.setdefault("node", decl["node"])
                entry.pop("unmeasured", None)
                unchecked = [u for u in unchecked if not u.startswith(f"{node}.{key} ")]

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
                            "--canonical-entry", args.canonical_entry or "",
                            "--canonical-node-prefix", args.canonical_node_prefix,
                            "--consumers", *args.consumers,
                            "--roots", *args.roots],
        "_declared_unmeasured": sorted(declared),
        "_declared_required": sorted(declared_required),
        "_canonical_type_overrides": sorted(canonical_overridden),
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
