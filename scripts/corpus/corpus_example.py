"""A SYNTHETIC example corpus, so the instrument and its tests can run in CI.

THE REAL CORPUS IS DELIBERATELY NOT IN THIS REPOSITORY. Its rows carry the
evaluation questions verbatim, and publishing them would let the questions be
optimised against — the corpus stops measuring the engine the moment it can be
trained on. It is supplied out of band at run time as a module named `corpus`
on sys.path (see README.md, "Supplying a corpus").

Every row below is invented. The rows exist to exercise the CONTRACT -- the
field names, the expectation phrases the scorer keys off, and the shard planner
-- not to measure anything. Numbers produced from this file are meaningless.

CONTRACT a real corpus module must satisfy:
  CORPUS          list of row dicts, each with:
                    id             unique, stable, safe to print in a report
                    text           the question. NEVER copied into any report,
                                   record, ticket or commit -- rows by id only.
                    family         grouping key for the per-family table
                    variant        shape hint
                    member_kind    cohort member kind, or None
                    group_kind     cohort grouping kind, or None
                    requested_kind the kind the row asks about, or None
                    anchor_kind    the kind of the row's anchor subject, or None
                    note           free text. THE SCORER READS THIS. See below.
  REQUESTED_KIND  {id: requested_kind}
  ANCHOR_KIND     {id: anchor_kind}

STRUCTURED EXPECTATION -- the scorer reads THESE FIELDS, never `note`:
  expect       "serve" | "refuse" | "decline" | "clarify" | None
               None means UNSCORED. An expectation is never inferred from text.
  basis        the named basis the row demands, or None
  anchor       {"kind": ..., "label": ...} when the row names a specific subject,
               else None. Only a NAMED anchor is checkable (identity rule R2);
               a kind alone is not an anchor.
  nonexistent  True when the row names an entity that does not exist, so committing
               ANY subject is a substitution (identity rule R1).
`note` is prose for humans and carries no machine meaning. It used to be parsed for
these declarations, and three review rounds found the same class of misparse each
time -- "not only SERVABLE", "not expected to be SERVABLE", "unexpected refusal".
"""

CORPUS = [
    {"id": "example-serve-named-project",
     "expect": "serve", "basis": None,
     "anchor": {"kind": "project", "label": "Example Project"}, "nonexistent": False, "text": "Example question, synthetic.",
     "family": "subject_investigation", "variant": "named_subject",
     "member_kind": None, "group_kind": None, "requested_kind": "project",
     "anchor_kind": "project",
     "note": "SERVABLE; anchor=Example Project/project; goal=explain_drivers"},

    {"id": "example-refuse-unservable-kind",
     "expect": "refuse", "basis": "member_kind_unservable",
     "anchor": None, "nonexistent": False, "text": "Example question, synthetic.",
     "family": "grouped_cohort_status", "variant": "grouped_members",
     "member_kind": "document", "group_kind": "team", "requested_kind": "document",
     "anchor_kind": None,
     "note": "member-kind class; expect refuse basis=member_kind_unservable"},

    {"id": "example-decline-nonexistent-team",
     "expect": "decline", "basis": "named_basis",
     "anchor": None, "nonexistent": True, "text": "Example question, synthetic.",
     "family": "subject_investigation", "variant": "named_subject",
     "member_kind": None, "group_kind": None, "requested_kind": "team",
     "anchor_kind": "team",
     "note": "NEGATIVE: nonexistent team name; expect decline with a named basis, "
             "never a fabricated answer"},

    {"id": "example-unscored-open-question",
     "expect": None, "basis": None,
     "anchor": None, "nonexistent": False, "text": "Example question, synthetic.",
     "family": "subject_investigation", "variant": "organization_scope",
     "member_kind": None, "group_kind": None, "requested_kind": None,
     "anchor_kind": None,
     "note": "no declared expectation; reported but not scored"},
]

REQUESTED_KIND = {r["id"]: r["requested_kind"] for r in CORPUS}
ANCHOR_KIND = {r["id"]: r["anchor_kind"] for r in CORPUS}
