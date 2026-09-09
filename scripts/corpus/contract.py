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
here rather than re-derived at test time: `harness.post` writes exactly three failure
bodies -- the transport arm (`{error: str(e)}`, status 0, no exchange happened at all),
the undecodable-HTTP-error arm (`{error: "unparseable body"}`, a non-2xx exchange whose
body would not decode, real status), and the undecodable-2xx arm (`{undecodable:
"unparseable body"}`, a COMPLETED exchange whose body would not decode, real status;
CHAOS-5380 r3 P1-1) -- and `harness.validate_live_payload` synthesizes a `failure`
ENVELOPE, which is a separate signal the classifier already reads on its own. Adding a
fourth writer means adding its key here.

The undecodable-2xx arm gets its OWN key rather than reusing `error`: a scripted body
`{"error": "..."}`  under a 200 is a body that decoded FINE and happens to carry the
producer's generic failure key (see attempt_classes' `_error`); reusing that key for a
body that did NOT decode would make the two indistinguishable at the artefact, which is
exactly the loss chris ruled against ("add the member" -- name it for what it is).
"""

# The statuses the PRODUCER treats as served. A set, not a scalar: if this ever widens,
# it widens in one place and every reader follows, instead of one side learning about it.
SERVED_STATUSES = frozenset({200})

# The body key the producer writes to mean "this did not work", read from a body that DID
# decode. Named, so the producer builds its failure bodies FROM it rather than spelling it.
ERROR_BODY_KEY = "error"
# The body key the producer writes when the exchange COMPLETED but the body itself would
# not decode (CHAOS-5380 r3 P1-1) -- distinct from ERROR_BODY_KEY because that case is a
# body that decoded fine and reported failure, a different fact from a body that did not
# decode at all.
UNDECODABLE_BODY_KEY = "undecodable"
FAILURE_BODY_KEYS = frozenset({ERROR_BODY_KEY, UNDECODABLE_BODY_KEY})


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
