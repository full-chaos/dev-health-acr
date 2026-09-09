#!/usr/bin/env python3
"""THE contract between the corpus PRODUCER and every reader of what it writes.

Two values live here and nowhere else: which HTTP status means the request was served,
and which response-body keys the producer writes to mean "this did not work".

WHY A MODULE AND NOT A TEST. Review round 2 killed the previous arrangement, in which a
pin DERIVED these facts by walking `harness.py`'s AST. Both derivations were hollow:

  * the body-key walk scanned only dicts nested under `Return` nodes, and the
    HTTP-error body is an ASSIGNMENT -- so it never saw it. It returned the right
    answer anyway, BY ACCIDENT, because the transport arm's returned dict happens to
    carry the same key. A producer mutant adding `fatal` classified `ok_200` with the
    class-invariant pin passing.
  * the served-status walk asserted 200 was among the AST's constants and then returned
    the literal 200. A producer mutant accepting 201 was undetectable; all pins passed.

A PROXY FOR A CONTRACT HOLLOWS OUT SILENTLY, and it does so while reading as rigour.
There is nothing to derive when there is one value: the producer and every reader import
these names, so they cannot hold different opinions about the same artefact. A pin
(`test_no_module_spells_the_contract_itself`) fails if any other module in this directory
writes the served status or a body key as a literal, because a second spelling is how the
two sides drift apart again.

FAILURE_BODY_KEYS was ENUMERATED from the producer, once, and the reading is recorded
here rather than re-derived at test time: `harness.post` writes exactly one failure body
INSIDE `response` -- the transport arm (`{error: str(e)}`, status 0, no exchange happened
at all) -- and `harness.validate_live_payload` synthesizes a `failure` ENVELOPE, which is
a separate signal the classifier already reads on its own. Adding a second writer means
adding its key here.

A COMPLETED exchange whose body could not be read or decoded (CHAOS-5380 r3 P1-1) is a
DIFFERENT signal, and deliberately NOT a body key: r4 (astra) found that a key inside
`response` shares a namespace with server-controlled JSON content, so a validly-decoded
response that happened to carry that exact key was indistinguishable from a genuine
decode failure. `body_undecodable` is instead an ARTEFACT-level field `run_replicate`
writes beside `response` (and `post`'s fourth return value) -- never inside the body a
server controls.
"""

# The statuses the PRODUCER treats as served. A set, not a scalar: if this ever widens,
# it widens in one place and every reader follows, instead of one side learning about it.
SERVED_STATUSES = frozenset({200})

# The one body key the producer writes to mean "this did not work". Named, so the
# producer builds its failure bodies FROM it rather than spelling it.
ERROR_BODY_KEY = "error"
FAILURE_BODY_KEYS = frozenset({ERROR_BODY_KEY})


# The floor of a real HTTP exchange. A status BELOW this means no exchange happened at
# all -- `harness.post` writes 0 on any transport exception. It is a DIFFERENT fact from
# "served", and it only coincidentally shares a number with it today, so it is named
# rather than spelled: if SERVED_STATUSES ever widens, this must not move with it.
TRANSPORT_CEILING = 200


def reached_the_service(status):
    """Did an HTTP exchange happen at all?"""
    return not (isinstance(status, int) and status < TRANSPORT_CEILING)


def is_success_status(status):
    """Was this request served, per the producer's own contract?"""
    return status in SERVED_STATUSES


def body_failure_keys(response):
    """Which of the producer's failure keys this response body carries.

    PRESENCE, not truthiness: the producer writes these keys only to report a failure, so
    an empty value is still the producer calling the attempt broken.
    """
    if not isinstance(response, dict):
        return frozenset()
    return FAILURE_BODY_KEYS & set(response)
