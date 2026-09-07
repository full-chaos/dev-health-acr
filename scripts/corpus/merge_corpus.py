#!/usr/bin/env python3
"""THE merge step. Its output is the single verdict for a run.

run-two-turn-parallel.sh's SCOPE NOTE, applied here: a single shard's artefact is
never standalone evidence; the run's pass/fail signal is this tool's exit code
and the verdict artefact it writes, not any one shard's. A shard that exited
non-zero, or a layout that lost or duplicated an id, aborts the merge rather than
being averaged away.

The merged artefact SELF-LABELS its provenance (execution_shape parallel|
sequential, the shas of everything that ran, the dump id, the corpus/harness
sha256) so a reader can never mistake a parallel run for a ratified sequential
one, and never mistake one rig pin for another.

Usage:
  merge_corpus.py --shape parallel   --in shards/  --out verdict-parallel.json   [--baseline base.json]
  merge_corpus.py --shape sequential --in seq/     --out verdict-sequential.json [--baseline base.json]

Provenance is read from provenance.json beside this file (written by the launcher
so the shas are recorded at run time, never retyped afterwards).
"""
import argparse
import json
import os
import sys
from collections import Counter, OrderedDict
from pathlib import Path

HERE = Path(__file__).parent
sys.path.insert(0, str(HERE))
from corpus import CORPUS  # noqa: E402
import engine_failures  # noqa: E402  — post-hoc attempt reader, deliberately OFF the measurement path
# Both are ADDITIVE: classify() and the five
# buckets below are untouched, so re-merging arm 2's raw files still reproduces
# verdict-arm2-sequential.json exactly. The v2 findings ride in NEW fields.
import expectations  # noqa: E402
import subject_identity  # noqa: E402

SERVED_STATUSES = {"complete", "partial", "degraded", "answered"}

# The clarification vocabulary, authored once. Imported by the bucketer so that classify()
# and the scorer cannot disagree about what counts as a clarification.
from expectations import CLARIFICATION_VALUES  # noqa: E402
FAMILY_ORDER = list(OrderedDict((r.get("family") or "_none", None) for r in CORPUS))
BY_ID = {r["id"]: r for r in CORPUS}


def classify(row):
    """Four reportable buckets + error, from the fields the baseline recorded.

    served_with_data     terminal served status AND at least one claimed fact
    served_degraded      terminal served status with ZERO claimed facts (the
                         "hollow serve" — served != answered; the harness README's
                         own diagnostic signature)
    clarification_needed terminal clarification_required(max_turns_exhausted):
                         the frame never committed its operands (shape
                         for the C family)
    unserved             no_match / refused
    error                non-retryable HTTP failure as the terminal outcome
    """
    http = row.get("final_http")
    status = row.get("final_payload_status")
    if http != 200:
        return "error"
    if status in SERVED_STATUSES:
        return "served_with_data" if (row.get("claimed_facts_n") or 0) > 0 else "served_degraded"
    # `chain` is a STRING like "t1=clarification_required -> t2=no_match".
    # Two traps, both caught by the merge positive control against the 09-05
    # baseline:
    #  1. the original `" ".join(chain)` spaced out every CHARACTER, so this
    #     fallback never fired at all — it reached the right answer by accident;
    #  2. matching the WHOLE chain over-captures: a row that asked for
    #     clarification at t1 and then terminated at no_match is UNSERVED, not
    #     clarification_needed. Re-bucketing on the whole string moved 14 of the
    #     36 baseline rows and would have destroyed like-for-like comparison.
    # Only the TERMINAL segment decides the bucket.
    chain = row.get("chain") or ""
    if not isinstance(chain, str):          # defensive: older artefacts stored a list
        chain = " ".join(chain)
    chain = chain.split("->")[-1].strip()
    # r6 (b): INSTANCES, not a substring. `"clarification" in status` bucketed any string
    # containing the word -- `clarification_required(future)` and even
    # `clarification_bogus` -- so an unauthored terminal silently altered bucket totals.
    # The instances are authored in golden_verdicts.json and imported here, so the
    # bucketer and the scorer read one vocabulary. BOTH spellings occur in real runs: the
    # terminal `clarification_required(max_turns_exhausted)` and the bare
    # `clarification_required` a chain can end on, so both are authored.
    if (status or "").strip() in CLARIFICATION_VALUES or chain in CLARIFICATION_VALUES:
        return "clarification_needed"
    return "unserved"


BUCKETS = ["served_with_data", "served_degraded", "unserved", "clarification_needed", "error"]

# INSTRUMENT V2 — the failure bucket a confirmed subject substitution is forced into.
# classify() is NOT modified: `bucket` keeps its v1 value so totals stay byte-compatible
# and the positive control holds. `bucket_v2` carries the override, and totals_v2 counts
# it. A row that served facts about a subject the question did not name is not a serve.
SUBJECT_SUBSTITUTION_BUCKET = "subject_substitution"
BUCKETS_V2 = BUCKETS + [SUBJECT_SUBSTITUTION_BUCKET]


def classify_v2(row, substituted):
    """v1 bucket, except a confirmed substitution overrides to its own failure bucket."""
    return SUBJECT_SUBSTITUTION_BUCKET if substituted else classify(row)


def load_run(indir):
    files = sorted(Path(indir).glob("*/shard-summary.json"))
    if not files:
        files = sorted(Path(indir).glob("shard-summary.json"))
    if not files:
        raise SystemExit(f"MERGE ABORT: no shard-summary.json under {indir}")
    rows, shards = [], []
    for f in files:
        with open(f) as fh:
            s = json.load(fh)
        shards.append({
            "shard": s.get("shard"),
            "n_planned": len(s.get("planned_ids") or []),
            "n_rows": len(s.get("rows") or []),
            "wall_seconds": s.get("total_wall_seconds"),
            "started_unix": s.get("started_unix"),
            "finished_unix": s.get("finished_unix"),
            "path": str(f),
        })
        rows.extend(s.get("rows") or [])
    return rows, shards


def check_coverage(rows):
    ids = [r["corpus_id"] for r in rows]
    dupes = [q for q, n in Counter(ids).items() if n > 1]
    missing = [r["id"] for r in CORPUS if r["id"] not in set(ids)]
    extra = [q for q in set(ids) if q not in BY_ID]
    problems = []
    if dupes:
        problems.append(f"{len(dupes)} id(s) appear in more than one shard: {sorted(dupes)}")
    if missing:
        problems.append(f"{len(missing)} corpus id(s) never ran: {sorted(missing)}")
    if extra:
        problems.append(f"{len(extra)} id(s) not in the corpus: {sorted(extra)}")
    return problems


def _post_hoc(root):
    """Per-attempt failure classes read back from the artefacts, with the request
    ids the follow-up tickets need. Empty dict if the artefacts are unreadable —
    never a crash, and never a silent zero: an unreadable artefact is reported by
    engine_failures.scan as its own UNREADABLE_ARTEFACT class."""
    recs = engine_failures.scan(root)
    by = {}
    for r in recs:
        by.setdefault(r["kind"], {"attempts": 0, "rows": set(), "request_ids": []})
        e = by[r["kind"]]
        e["attempts"] += 1
        e["rows"].add(r["corpus_id"])
        if r.get("request_id"):
            e["request_ids"].append({"corpus_id": r["corpus_id"], "request_id": r["request_id"]})
    return {k: {"attempts": v["attempts"], "rows": sorted(v["rows"]),
                "request_ids": v["request_ids"]} for k, v in sorted(by.items())}


def per_family(rows):
    table = OrderedDict((f, Counter()) for f in FAMILY_ORDER)
    for r in rows:
        fam = r.get("family") or BY_ID[r["corpus_id"]].get("family") or "_none"
        table.setdefault(fam, Counter())[classify(r)] += 1
    return OrderedDict(
        (fam, {b: c.get(b, 0) for b in BUCKETS} | {"total": sum(c.values())})
        for fam, c in table.items()
    )


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--shape", required=True, choices=["parallel", "sequential"])
    ap.add_argument("--in", dest="indir", required=True)
    ap.add_argument("--out", required=True)
    ap.add_argument("--baseline", help="a prior verdict.json to diff against")
    args = ap.parse_args()

    rows, shards = load_run(args.indir)

    problems = check_coverage(rows)
    bad_shards = [s for s in shards if s["n_rows"] != s["n_planned"]]
    if bad_shards:
        problems.append(f"{len(bad_shards)} shard(s) produced fewer rows than planned")
    if problems:
        print("MERGE ABORT — the run is not admissible evidence:", file=sys.stderr)
        for p in problems:
            print(f"  - {p}", file=sys.stderr)
        sys.exit(1)

    # INSTRUMENT V2 — subject identity, read back from the raw attempt files under
    # the SAME --in root. Applies retroactively to any arm that kept its per-attempt
    # JSON, which is why arm 2 and arm 3B can both be re-scored without re-running.
    identity = subject_identity.scan(args.indir, CORPUS, expectations.expectation_for)
    subs_by_id = {k: v.get("subject_substitution") for k, v in identity.items()}
    unreadable = sorted(k for k, v in identity.items() if v.get("state") != "read")

    prov_path = HERE / "provenance.json"
    provenance = json.loads(prov_path.read_text()) if prov_path.exists() else {}
    provenance["execution_shape"] = args.shape
    provenance["shard_count"] = len(shards)

    # r1 #2/#4: identity state and terminal status now reach the scorer, so an
    # unverifiable identity cannot score as agreement and a named-basis decline is not
    # satisfied by a bare no_match. Built once and reused, rather than twice as before.
    _states = {k: (v or {}).get("state", "read") for k, v in identity.items()}
    _terminals = {r["corpus_id"]: r.get("final_payload_status") for r in rows}
    _exp_table = expectations.table(
        BY_ID, {r["corpus_id"]: classify(r) for r in rows}, subs_by_id,
        states_by_id=_states, terminals_by_id=_terminals)

    counts = Counter(classify(r) for r in rows)
    verdict = {
        "ticket": os.environ.get("CORPUS_TICKET", ""),
        "provenance": provenance,
        "totals": {b: counts.get(b, 0) for b in BUCKETS} | {"total": len(rows)},
        "per_family": per_family(rows),
        # Per-ATTEMPT rig diagnostics, summed over rows. NOT compared against the
        # 09-05 baseline (it recorded no counterpart) — they are disclosure, not a
        # bucket. rows_with_* is the honest denominator: one row can carry several.
        "rig_diagnostics": {
            "attempt_upstream_504_n": sum(r.get("attempt_upstream_504_n") or 0 for r in rows),
            "rows_with_upstream_504": sum(1 for r in rows if (r.get("attempt_upstream_504_n") or 0) > 0),
            "attempt_overrun_413_n": sum(r.get("attempt_overrun_413_n") or 0 for r in rows),
            "rows_with_overrun_413": sum(1 for r in rows if (r.get("attempt_overrun_413_n") or 0) > 0),
            "overrun_413_ids": sorted(r["corpus_id"] for r in rows if (r.get("attempt_overrun_413_n") or 0) > 0),
            # The 422s and the non-ceiling 400 ride in the
            # diagnostics block so their tickets get COUNTS after arm 2. Scanned
            # post-hoc from the attempt JSONs both arms already write, NOT from
            # run_shard — run_shard is frozen between the control and the
            # parallel run so that only the execution SHAPE differs. The merge is
            # the reporting step and runs identically for both, so it is the
            # right place for this.
            "post_hoc_attempt_classes": _post_hoc(args.indir),
            "overrun_413_detail": [
                {"corpus_id": r["corpus_id"], **(r.get("overrun_detail") or {})}
                for r in rows if r.get("overrun_detail")
            ],
        },
        # The same buckets with ceiling-rejected rows removed. NOT the headline
        # number — the headline stays the full 36 so it compares like-for-like with
        # the 09-05 baseline — this is the "what would prod at 45 have seen" read.
        "totals_excluding_ceiling_overruns": (
            lambda kept: {b: Counter(classify(r) for r in kept).get(b, 0) for b in BUCKETS}
                         | {"total": len(kept)}
        )([r for r in rows if (r.get("attempt_overrun_413_n") or 0) == 0]),
        # ---- INSTRUMENT V2, additive ----------------------------------------
        "subject_identity": {
            "rule_R1": "a row whose note declares the named entity NONEXISTENT has nothing correct to commit to; any committed subject is a substitution",
            "rule_R2": "a row whose note declares anchor=<NAME>/<kind> must commit a subject corresponding to NAME",
            "rule_R3_observation_only": "committed subject kind vs the row's requested_kind is RECORDED, not scored: requested_kind is the MEMBER kind on cohort rows, so a mismatch is not automatically wrong",
            "substitution_count": sum(1 for v in identity.values() if v.get("subject_substitution")),
            "substitution_rows": sorted(k for k, v in identity.items() if v.get("subject_substitution")),
            "substitution_detail": [
                {"corpus_id": k, "rule": v.get("substitution_rule"),
                 "detail": v.get("substitution_detail"),
                 "request_id": v.get("request_id"), "result_id": v.get("result_id"),
                 "claimed_facts_n": v.get("claimed_facts_n"),
                 "committed": v.get("committed"),
                 "match_mechanisms": v.get("match_mechanisms")}
                for k, v in sorted(identity.items()) if v.get("subject_substitution")
            ],
            "unreadable_artefact_rows": unreadable,
            "vector_committed_rows": sorted(
                k for k, v in identity.items() if "vector" in (v.get("match_mechanisms") or [])),
            "committed_kind_not_requested_kind": [
                {"corpus_id": k, "requested_kind": BY_ID[k].get("requested_kind"),
                 "wrong_kind_commits": subject_identity.kind_observations(BY_ID[k], v.get("committed")),
                 "committed_kinds": sorted({c.get("kind") for c in (v.get("committed") or [])}),
                 "committed_labels": [c.get("label") for c in (v.get("committed") or [])],
                 "match_mechanisms": v.get("match_mechanisms")}
                for k, v in sorted(identity.items())
                if subject_identity.kind_observations(BY_ID[k], v.get("committed"))
            ],
        },
        "expectation_scoring": _exp_table,
        "expectation_summary": {
            v: sum(1 for e in _exp_table if e["verdict"] == v)
            for v in ("agree", "agree_weak", "disagree", "unscored")
        },
        # agree_weak SPLIT by reason. Three behaviours share that verdict and the summary
        # used to fold them, which hid the contrast between a row that refused without
        # stating its basis and one that never terminated at all.
        "expectation_summary_split": (lambda tb: {
            "agree": sum(1 for e in tb if e["verdict"] == "agree"),
            "weak_basis_unstated": sum(1 for e in tb if e.get("weak_kind") == "weak_basis_unstated"),
            "weak_never_terminated": sum(1 for e in tb if e.get("weak_kind") == "weak_never_terminated"),
            "weak_hollow_serve": sum(1 for e in tb if e.get("weak_kind") == "weak_hollow_serve"),
            "weak_identity_unverified": sum(1 for e in tb if e.get("weak_kind") == "weak_identity_unverified"),
            "weak_unclassified": sum(1 for e in tb if e.get("weak_kind") == "weak_unclassified"),
            "disagree": sum(1 for e in tb if e["verdict"] == "disagree"),
            "unscored": sum(1 for e in tb if e["verdict"] == "unscored"),
        })(_exp_table),
        # v1 buckets with confirmed substitutions pulled out into their own failure
        # bucket. `totals` above is untouched and remains the like-for-like number.
        "totals_v2": {
            b: sum(1 for r in rows if classify_v2(r, subs_by_id.get(r["corpus_id"])) == b)
            for b in BUCKETS_V2
        } | {"total": len(rows)},
        # ---------------------------------------------------------------------
        "shards": shards,
        "wall_seconds_sum": round(sum(s["wall_seconds"] or 0 for s in shards), 1),
        "wall_seconds_span": (
            round(max(s["finished_unix"] for s in shards) - min(s["started_unix"] for s in shards), 1)
            if all(s.get("started_unix") and s.get("finished_unix") for s in shards) else None
        ),
        "rows": sorted(
            [{**r, "bucket": classify(r),
              "bucket_v2": classify_v2(r, subs_by_id.get(r["corpus_id"])),
              "subject_substitution": bool(subs_by_id.get(r["corpus_id"])),
              "identity_state": (identity.get(r["corpus_id"]) or {}).get("state"),
              "substitution_rule": (identity.get(r["corpus_id"]) or {}).get("substitution_rule"),
              "committed_subjects": (identity.get(r["corpus_id"]) or {}).get("committed"),
              "match_mechanisms": (identity.get(r["corpus_id"]) or {}).get("match_mechanisms"),
              "expectation": expectations.expectation_for(BY_ID[r["corpus_id"]])["expectation"],
              "expectation_verdict": next(
                  e["verdict"] for e in _exp_table if e["corpus_id"] == r["corpus_id"]),
              # Additive flag so the deliverable can show the split
              # with and without rows the RIG's 30-item ceiling rejected (prod = 45).
              # Derived here, not in run_shard — run_shard stays frozen between the
              # sequential control and the parallel run so only the SHAPE differs.
              "overrun_at_rig_ceiling": (r.get("attempt_overrun_413_n") or 0) > 0}
             for r in rows],
            key=lambda r: (FAMILY_ORDER.index(r.get("family") or "_none") if (r.get("family") or "_none") in FAMILY_ORDER else 99, r["corpus_id"]),
        ),
    }

    if args.baseline:
        with open(args.baseline) as fh:
            base = json.load(fh)
        base_bucket = {r["corpus_id"]: r.get("bucket") for r in base.get("rows", [])}
        changed = []
        for r in verdict["rows"]:
            was = base_bucket.get(r["corpus_id"])
            if was is None:
                changed.append({"corpus_id": r["corpus_id"], "from": "ABSENT_FROM_BASELINE", "to": r["bucket"]})
            elif was != r["bucket"]:
                changed.append({"corpus_id": r["corpus_id"], "from": was, "to": r["bucket"],
                                "failure_code": r.get("failure_code"),
                                "final_payload_status": r.get("final_payload_status"),
                                "claimed_facts_n": r.get("claimed_facts_n")})
        verdict["changed_vs_baseline"] = changed
        verdict["baseline_artifact"] = args.baseline

    Path(args.out).write_text(json.dumps(verdict, indent=2))
    t = verdict["totals"]
    print(f"VERDICT {args.shape}: " + "  ".join(f"{b}={t[b]}" for b in BUCKETS) + f"  total={t['total']}")
    si = verdict["subject_identity"]
    es = verdict["expectation_summary"]
    print(f"  SUBJECT SUBSTITUTION: {si['substitution_count']} row(s) {si['substitution_rows']}")
    if si["unreadable_artefact_rows"]:
        print(f"  unreadable artefacts (NOT a pass): {si['unreadable_artefact_rows']}")
    print(f"  vector-committed rows: {len(si['vector_committed_rows'])} {si['vector_committed_rows']}")
    print(f"  EXPECTATION: agree={es['agree']} agree_weak={es['agree_weak']} disagree={es['disagree']} unscored={es['unscored']}")
    d = verdict["rig_diagnostics"]
    print(f"  413 overruns: {d['attempt_overrun_413_n']} attempts over {d['rows_with_overrun_413']} rows"
          f"  (baseline recorded 0)  ids={d['overrun_413_ids']}")
    if d["rows_with_overrun_413"]:
        od = d["overrun_413_detail"][0]
        print(f"  DISCLOSURE: {d['rows_with_overrun_413']} rows 413'd at the rig's "
              f"{od.get('max_items')}-item ceiling (prod 45): measuredItems={od.get('measured_items')} "
              f"axis={od.get('axis')}")
        e = verdict["totals_excluding_ceiling_overruns"]
        print("  excluding those rows: " + "  ".join(f"{b}={e[b]}" for b in BUCKETS) + f"  total={e['total']}")
    print(f"  upstream 504s: {d['attempt_upstream_504_n']} attempts over {d['rows_with_upstream_504']} rows"
          f"  (retried attempts included; terminal status may still be served)")
    print(f"-> {args.out}")


if __name__ == "__main__":
    main()
