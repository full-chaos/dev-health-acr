#!/usr/bin/env python3
"""Report the boundary's required-ness decision for a class of keys. CHAOS-5430.

The CLASS PROOF. Round 2 found eleven `receipt_id` fields observed on every visit and not
required, because round 1's finding had been closed at its one named example instead of at
its class. A hand-written list of the twelve is not proof of anything -- it is the same
mistake with more rows. This reads the GENERATED schema and shows, per key, the two
measured facts and the decision they produce, so a key that ought to be required and is not
shows up as a row rather than as an incident three rounds later.
"""
import argparse
import json
from pathlib import Path


def rows(schema_path, pattern):
    doc = json.loads(Path(schema_path).read_text())
    reached = set(doc.get("_nodes_reached_unconditionally") or [])
    out = []
    for node, keys in sorted(doc["nodes"].items()):
        for key, rule in sorted(keys.items()):
            if pattern not in key:
                continue
            observed = rule.get("observed") or {}
            visits = rule.get("node_visits") or 0
            seen = sum(observed.values())
            out.append({
                "node": node,
                "key": key,
                "observed_always": bool(visits and seen == visits),
                "seen": seen,
                "visits": visits,
                "read_uncond": node in reached,
                "required": bool(rule.get("required")),
                "declared": bool(rule.get("required_declared")),
            })
    return out


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--schema", default=str(Path(__file__).parent / "artefact_schema.json"))
    ap.add_argument("--pattern", default="receipt_id")
    ap.add_argument("--require-all-observed-always", action="store_true",
                    help="exit non-zero if a key that is observed-always AND sits in a "
                         "node read unconditionally is not required")
    args = ap.parse_args()

    data = rows(args.schema, args.pattern)
    w = max((len(f'{r["node"]}.{r["key"]}') for r in data), default=10)
    print(f'{"path":{w}}  {"seen/visits":>13}  obs_always  read_uncond  required  declared')
    gaps = []
    for r in data:
        path = f'{r["node"]}.{r["key"]}'
        print(f'{path:{w}}  {r["seen"]:>6}/{r["visits"]:<6}  '
              f'{str(r["observed_always"]):<10}  {str(r["read_uncond"]):<11}  '
              f'{str(r["required"]):<8}  {r["declared"]}')
        # THE GAP IS BOTH CONDITIONS, not presence alone. A key nothing dereferences
        # unconditionally is correctly optional: requiring it would be the presence-only
        # rule that marked 359 keys required and broke the live path.
        if r["observed_always"] and r["read_uncond"] and not r["required"]:
            gaps.append(path)
    print(f'\n{len(data)} keys matching {args.pattern!r}; '
          f'{sum(1 for r in data if r["observed_always"])} observed-always; '
          f'{sum(1 for r in data if r["read_uncond"])} in a node read unconditionally; '
          f'{sum(1 for r in data if r["required"])} required; '
          f'{sum(1 for r in data if r["declared"])} declared by hand')
    if gaps:
        print("OBSERVED-ALWAYS AND UNCONDITIONALLY READ BUT NOT REQUIRED:")
        for g in gaps:
            print("   ", g)
    if args.require_all_observed_always and gaps:
        raise SystemExit(1)


if __name__ == "__main__":
    main()
