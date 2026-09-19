"""A `corpus` module for the conversation-runner tests: the synthetic single-turn example plus
one INVENTED conversation (no real question text). Isolation is by subprocess, same as
testdata_corpus/."""
from corpus_example import CORPUS, REQUESTED_KIND, ANCHOR_KIND  # noqa: F401

CONVERSATIONS = [
    dict(
        id="syn-clarify-then-redeem",
        shape="(c) synthetic follow-up naming a different subject -> clarify -> redeem",
        turns=[
            dict(n=1, text="synthetic first question", expect="serve", anchor_kind="team",
                 expected_subject={"kind": "team", "canonical_id": "syn:one"}),
            dict(n=2, text="synthetic second question", parent="turn1", expect="clarify",
                 anchor_kind="team",
                 clarify_candidates=[{"kind": "team", "canonical_id": "syn:one", "remembered": True},
                                     {"kind": "team", "canonical_id": "syn:two", "remembered": False}]),
            dict(n=3, text="synthetic second question", parent="turn2", expect="serve",
                 anchor_kind="team", redeem={"kind": "team", "canonical_id": "syn:two"},
                 expected_subject={"kind": "team", "canonical_id": "syn:two"}),
        ],
    ),
]
