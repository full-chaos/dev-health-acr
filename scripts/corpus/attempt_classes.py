#!/usr/bin/env python3
"""THE classifier for one attempt artefact. CHAOS-5380.

Two modules counted attempts and each carried its own ladder: run_shard.attempt_diagnostics
knew 504 and 413; engine_failures.scan knew 500, 413, 504, an unreadable file and an
`OTHER_<status>` catch-all -- with two branches, because a gateway 504 often carries a
non-JSON body and so has no parsed `failure` object at all. They agreed only by accident,
and every instrument defect on this seam so far has been two counters disagreeing about
one artefact.

So the classification lives here, once, and both import it.

WHY A ROW NEEDS THE CLASS AND NOT JUST A COUNT. A row's terminal status is not evidence
about what happened during the row: a 504 the harness retried into a 200 leaves no trace
in `final_http`, which is how the 09-06 smoke report printed zero deadline failures
against five logged 504s. Counting 504s fixed that ONE class; §6 of the
regression-diagnosis doc records eight sequential non-200 attempts comprising four 422s,
one 504, two 413s and one 400, and no per-row counter has ever carried the 422s or the
400. The vocabulary below is closed and TOTAL: every attempt lands in exactly one member,
so a class nobody thought of cannot vanish.

THE LEGACY KIND IS PART OF THE ARTEFACT OF RECORD. `post_hoc_attempt_classes` in the
merged verdict is keyed by engine_failures' own kind strings, so those strings are frozen
-- inconsistencies included (a bare 502/503 HTTP status is `UPSTREAM_502`/`UPSTREAM_503`,
while a PARSED upstream 502 is `OTHER_502`). legacy_engine_failure_kind is DERIVED from the
original code path rather than reconstructed from a description of it, and the equivalence
is MEASURED over the whole enumerated shape space rather than spot-checked -- see that
function and its pin. An earlier hand-reconstruction agreed with the original everywhere
except one column and was believed for three review rounds.
"""

import contract

# The closed, total vocabulary. Ordered as a reader reads a run: the served case, the
# deadline family, the two ACR-side rejections, the engine defect, then the catch-alls.
CLASSES = (
    "ok_200",
    "upstream_502",
    "upstream_503",
    "upstream_504",
    "overrun_413",
    "unprocessable_422",
    "rejected_400",
    "engine_invalid_500",
    # The consumer returned a NON-2xx for an upstream call that returned 200:
    # acr answered, and its BODY failed the investigation contract. Found live
    # on a private pair -- 7 attempts, every one of them http 502 with
    # failure.httpStatus 200 and code acr_contract_violation. See classify().
    "contract_violation",
    # A failed attempt whose statuses are BOTH in the served range. Reachable:
    # a malformed JSON-shaped body under HTTP 200 yields acr_malformed_response,
    # failed=True and this class (confirmed by review round 2). It exists so the
    # vocabulary is TOTAL -- an attempt that cannot be placed must land somewhere
    # visible, not in ok_200, which is the entire defect this module exists to stop.
    "failure_under_2xx",
    # An exchange COMPLETED -- a real status came back -- but the body itself did not
    # decode. CHAOS-5380 r3 P1-1: `harness.post` used to decode inside its transport
    # try/except, so this shape was reported as status 0, indistinguishable from a
    # refused connection. chris's ruling (iii): add the member, name it for the fact
    # (a completed 2xx whose body would not decode), not for how it is used.
    "served_2xx_undecodable_body",
    # No HTTP exchange happened at all: harness.post returns status 0 on any
    # transport exception (connection refused, DNS, read timeout), with no failure
    # envelope. Distinct from a 5xx: "never reached the service" and "the service
    # answered 5xx" are different facts about a run.
    "transport_failure",
    "other_4xx",
    "other_5xx",
    "unreadable",
)


def zero_counts():
    """A counter with EVERY member present at zero.

    An absent key and a zero must never look alike: §5 of the regression-diagnosis doc
    forbids reporting an absent measurement as a measured zero, and the only way a reader
    can rely on that is if a real zero is always spelled out.
    """
    return {name: 0 for name in CLASSES}


def _failure(attempt):
    response = attempt.get("response")
    failure = (response or {}).get("failure") if isinstance(response, dict) else None
    return failure if isinstance(failure, dict) else None


def _error(attempt):
    """The producer's OTHER way of saying "this did not work".

    codex r1 P1: `failed()` looked at exactly two signals -- the status, and the presence
    of a `failure` object -- so a body key that MEANS failure but is not spelled `failure`
    was invisible to it, and `harness.validate_live_payload` PRESERVES unknown keys. A 200
    carrying `{"error": ...}` therefore classified `ok_200`.

    `error` is not a guess at what a body might contain: it is the key `harness.post`
    itself writes, in both of its failure arms -- `{"error": "unparseable body"}` when an
    HTTPError's body will not decode, and `{"error": str(e)}` on any transport exception.
    Today neither arm can pair it with a 200 (the first implies status >= 400, the second
    writes status 0), so this is not a live defect being fixed. It is a CLAIM being made
    true: `failed()` is documented as enumerating SUCCESS, and a predicate that ignores
    the producer's own failure key does not do that.

    PRESENCE, not truthiness. The producer writes this key only to report a failure, so an
    `error` that happens to be empty is still an attempt the producer called broken.
    """
    response = attempt.get("response")
    keys = body_failure_keys(response)
    return sorted(keys)[0] if keys else None


def is_valid_count(value):
    """Is this a value a class counter may legitimately hold?

    codex r1 P1: `attempt_class_totals` validated the class table's KEY SET and nothing
    about its VALUES, and `counts[name] or 0` turned a `None` into a PUBLISHED MEASURED
    ZERO -- the exact guarantee that function exists to make, defeated through the one
    axis nobody had enumerated. Measured: `None` published zeros, a negative published a
    negative total, a float published 1.5, `True` published 1, and a string crashed the
    merge.

    A count is a non-negative int. `bool` is EXCLUDED explicitly because it is an int
    subclass in Python, so `True` would otherwise pass as the count 1 -- the same trap
    `validators.py` already guards on `attempt.status`.

    No UPPER bound is imposed. A large count is not evidence of corruption, and inventing
    a ceiling here would be a policy nobody decided; a huge value is accepted and is
    pinned as accepted so the omission is deliberate rather than forgotten.
    """
    return isinstance(value, int) and not isinstance(value, bool) and value >= 0


def _statuses(attempt):
    """(attempt_http, upstream_http). The attempt's own HTTP status -- what the consumer
    returned to the harness -- and, when the body parsed, the upstream status ACR reported
    inside it. Either can be absent, and THEY CAN DISAGREE."""
    http = attempt.get("status")
    failure = _failure(attempt)
    upstream = (failure or {}).get("httpStatus")
    return (http if isinstance(http, int) else None,
            upstream if isinstance(upstream, int) else None)


# THE contract lives in `contract.py` and NOTHING here spells it. codex r4 P1 (this cut's
# ancestor) found `failed()` accepting all of [200,400) while the producer accepts only
# 200, so a 201 classified `ok_200` while its own row bucketed `error` -- one artefact,
# two verdicts. Review round 2 then found the pin that was supposed to police that had
# been DERIVING the producer's contract by AST and getting it right by accident. Both
# sides now import one value; a literal spelled anywhere else fails a pin.
is_success_status = contract.is_success_status
body_failure_keys = contract.body_failure_keys
ERROR_BODY_KEY = contract.ERROR_BODY_KEY
FAILURE_BODY_KEYS = contract.FAILURE_BODY_KEYS


def is_upstream_504(attempt):
    """The frozen deadline predicate, in the ORIGINAL's shape: INDEPENDENT of every other.

    codex r4 P1: the frozen counters were rebuilt off the EXCLUSIVE class (one class per
    attempt), but the originals were independent tests -- an attempt could count as BOTH a
    504 and a 413. So an outer 504 answered with an inner 422 or 413 lost its deadline
    count entirely, and `reclassify_deadlines.is_deadline` reads that counter, so the row
    was never selected for reclassification. A deadline hidden by the reporting, in the
    counter this change twice claimed was safe "because it is derived from the same walk":
    same walk, but through a predicate of a different SHAPE.
    """
    http, upstream = _statuses(attempt)
    return http == 504 or upstream == 504


def is_overrun_413(attempt):
    """The frozen ceiling predicate, INDEPENDENT like its sibling above, and EXACTLY the
    original's: `upstream == 413`, nothing else.

    A draft of this widened it to `or http == 413` so that a BARE 413 -- the attempt's own
    status with no parsed failure object, the shape a non-JSON body produces -- would be
    counted, since the original counts it nowhere. That was wrong, and the ruling is worth
    keeping next to the code: FROZEN MEANS FROZEN. `attempt_overrun_413_n` keys a published
    artefact; changing what it counts changes what past numbers meant, and a widening is
    indistinguishable from a rise in ceiling rejections to anyone reading a trend. If the
    original's omission is a defect it earns a NEW counter or a ticket, never a quiet change
    in a frozen key's meaning.

    The bare-413 gap is real and is filed separately. It is not invisible in the meantime:
    the exclusive CLASS vocabulary does classify such an attempt `overrun_413`, and
    `attempt_class_n` carries it. Only the frozen counter stays faithful.
    """
    _, upstream = _statuses(attempt)
    return upstream == 413


def failed(attempt):
    """Did this attempt FAIL? The question is asked once, here.

    THE PREDICATE ENUMERATES SUCCESS, NOT FAILURE, and that inversion is the whole point.

    Three rounds of review and one live replicate found the same class -- a failure the
    reporting hides -- and every time the cause was the same: the rule listed the shapes
    that count as broken, and the shape that bit us was not on the list. A 502 whose
    upstream said 200. A partial class table. And then `harness.post`'s TRANSPORT arm,
    which returns `0, {"error": str(e)}` on a connection refusal, a DNS failure or a read
    timeout (harness.py) -- status 0, no failure envelope, so a rule written as
    `http >= 400 or a failure object` fired on NEITHER and a refused connection classified
    as `ok_200`.

    codex r1 P1 closed the last hole in that inversion: the rule still listed only TWO
    signals (the status, and a `failure` object), so the third thing a body can say --
    `{"error": ...}`, which `harness.post` writes in both of its own failure arms -- was
    not on the list either. An attempt is served only when its status is 200 AND it
    carries neither a failure object NOR an error. Nothing about the body is "irrelevant"
    to a predicate that claims to enumerate success.

    The set of statuses that mean "this attempt was served" is small and closed -- and
    codex r4 showed it is smaller than a first reading suggests. The PRODUCER accepts only
    200: `harness.post` retries or terminates on anything else, and `merge_corpus.classify`
    buckets `http != 200` as `error`. So "served" is exactly `is_success_status(http)` AND
    no failure object; EVERYTHING else is a failure, including 201, including 0, including
    599, including whatever the next consumer invents. Widening this to [200, 400) made the
    classifier disagree with its own producer about the same artefact.
    """
    http, _ = _statuses(attempt)
    # An exchange whose body could not be READ or DECODED at all is never success,
    # regardless of status -- r4 (astra): this is an ARTEFACT-level field, not a body
    # key, so it is checked directly rather than through _error()/_failure(). CHAOS-5380
    # r3 P1-1.
    if attempt.get("body_undecodable"):
        return True
    if _failure(attempt) is not None:
        return True
    # The producer's OTHER failure key. See _error(): `failed()` is documented as
    # enumerating SUCCESS, and a predicate that ignores `error` -- which `harness.post`
    # writes in both of its failure arms -- enumerates "no failure OBJECT" instead.
    if _error(attempt) is not None:
        return True
    return not is_success_status(http)


def classify(attempt):
    """The class of ONE readable attempt artefact.

    THE ATTEMPT'S OWN STATUS DECIDES WHETHER IT FAILED. The upstream status only refines
    WHY, and only when it is itself an error.

    An earlier version of this function said "the upstream status decides when it is
    present", and a live replicate against a private pair caught what that costs: on a
    contract violation the consumer returns 502 while `failure.httpStatus` is 200 -- acr's
    call did return 200, its BODY failed the investigation contract -- so SEVEN failed
    attempts classified as `ok_200`. That is the defect this whole module exists to
    prevent (a failure hidden inside the reporting), reintroduced one level down. Measured
    shape of every attempt in that run:

        attempt_http  upstream  failure.code                 n
                 200      None  None                        91
                 502       200  acr_contract_violation        7
                 422       422  acr_answer_rejected           6
                 400       400  acr_rejected_request          1
    """
    http, upstream = _statuses(attempt)
    # THE ATTEMPT'S OWN STATUS DECIDES WHETHER IT IS UNREADABLE. r3 P1-2: this used to
    # require BOTH the attempt's own status and the upstream status to be absent, so a
    # status-less attempt with a present upstream status classified FROM the upstream
    # (e.g. "engine_invalid_500") with no unreadable_reason -- contradicting
    # UNREADABLE_STATUS_ABSENT, which documents this as exactly the cell it fires on. The
    # upstream status is recorded BESIDE the class by outcome() regardless; it is never
    # used to classify an attempt whose own status is absent.
    if http is None:
        return "unreadable"
    # A COMPLETED, SERVED exchange whose body could not be read or decoded (r3 P1-1,
    # chris's ruling (iii): "add the member"). Gated on `is_success_status(http)`, not
    # merely on "an exchange completed" -- r4 (astra) finding 3: a NON-2xx exchange with
    # an undecodable body (e.g. a 504 gateway error with no parseable body) keeps
    # classifying from its real status below, exactly as it always has; only a status
    # the producer actually counts as SERVED gets this class. `body_undecodable` is an
    # ARTEFACT-level field, never a body key (r4 finding 2: a key inside `response`
    # shares a namespace with server-controlled content and a validly-decoded response
    # could impersonate it).
    if attempt.get("body_undecodable") and contract.is_success_status(http):
        return "served_2xx_undecodable_body"
    if not failed(attempt):
        return "ok_200"
    failure = _failure(attempt) or {}
    # A transport failure has no status at all to classify from: harness.post writes 0.
    # It is its own class rather than an "other_5xx", because "we never reached the
    # service" and "the service answered 5xx" are different facts about a run.
    if not contract.reached_the_service(http):
        return "transport_failure"
    # Keyed on the CODE, not on a status pair: the consumer's 502 and the upstream's 200
    # are both true, and neither one alone names what happened.
    if failure.get("code") == "acr_contract_violation":
        return "contract_violation"
    # The upstream status refines the class ONLY when it is itself an error; otherwise the
    # consumer's own status is what the attempt actually got.
    status = upstream if (upstream is not None and upstream >= 400) else http
    if status is None or status < 400:
        return "failure_under_2xx"
    if status == 400:
        return "rejected_400"
    if status == 413:
        return "overrun_413"
    if status == 422:
        return "unprocessable_422"
    if status == 500:
        return "engine_invalid_500"
    if status in (502, 503, 504):
        return f"upstream_{status}"
    return "other_4xx" if status < 500 else "other_5xx"


def outcome(attempt, turn, index):
    """One entry of the per-attempt list a corpus row carries beside its terminal bucket.

    `turn` and `attempt` come from the FILENAME's recorded sequence (attempt_order), never
    from position in a directory listing -- lexicographic order puts t10 before t9, and
    that defect has already selected the wrong terminal attempt twice on this instrument.
    """
    http, upstream = _statuses(attempt)
    failure = _failure(attempt)
    return {
        "turn": turn,
        "attempt": index,
        "class": classify(attempt),
        "http": http,
        "upstream_http": upstream,
        "code": (failure or {}).get("code") if isinstance(failure, dict) else None,
        "dt_s": attempt.get("dt"),
        # ALWAYS PRESENT, None when the attempt is not unreadable. See
        # unreadable_reason() for why the class alone is not enough.
        "unreadable_reason": unreadable_reason(attempt),
    }


# The two distinct facts `unreadable` names. They are different failures of the
# instrument and a reader must not have to guess which one happened.
UNREADABLE_PARSE_FAILED = "parse_failed"
UNREADABLE_STATUS_ABSENT = "status_absent"


def unreadable_reason(attempt):
    """WHY this attempt is unreadable, or None if it is not.

    `unreadable` covers two genuinely different facts: an artefact the loader could not
    decode at all, and one it decoded perfectly that carried no `status` (the loader
    ACCEPTS that -- measured, and pinned). Both fail CLOSED, which is the property that
    matters, but they are different instrument failures: the first says the file is
    damaged, the second says the harness wrote an attempt without recording what the
    consumer answered. Collapsing them into one word is the same shape of loss this
    module exists to stop, one level down, so the reason travels beside the class.

    The key is present on EVERY outcome record, None included -- a missing key and a
    known-not-unreadable attempt must never look alike, exactly as with the class table's
    explicit zeros.
    """
    if classify(attempt) != "unreadable":
        return None
    return UNREADABLE_STATUS_ABSENT


def unreadable_outcome(turn, index, reason):
    """An artefact that did not parse is REPORTED, never skipped and never guessed at.

    A scanner that drops what it cannot read reports a smaller, cleaner run than the one
    that happened -- the silent-skip shape that has bitten this harness as a dropped
    battery arm and as a needle matching zero occurrences.
    """
    return {"turn": turn, "attempt": index, "class": "unreadable",
            "http": None, "upstream_http": None, "code": None, "dt_s": None,
            # The loader's own message, kept verbatim for a human reading one row...
            "detail": reason,
            # ...and the CLOSED reason beside it, for anything counting rows. This is
            # the artefact that did not decode; a decoded attempt with no `status` is
            # the other value, and unreadable_reason() assigns it.
            "unreadable_reason": UNREADABLE_PARSE_FAILED}


def legacy_engine_failure_kind(attempt):
    """engine_failures.scan's historical kind for a FAILING attempt, or None if it served.

    DERIVED FROM THE ORIGINAL CODE PATH, not reconstructed from a description of it
    (chris's ruling, option (a)). The body below is origin/main's `scan` ladder transcribed
    statement for statement, in its own order, keyed on its own predicates -- including the
    two that a re-implementation got wrong:

      * `fail = (response or {}).get("failure") or {}` then `if not fail:`. The branch is
        chosen by whether a failure object is PRESENT AND TRUTHY, never by whether an
        upstream status happens to be there. An empty `{"failure": {}}` takes the
        status-only branch exactly as the original does.
      * the status-only branch yields a kind ONLY for `st >= 400`; anything else produced
        no record at all in the original, so it is None here.

    Round 3 found the cost of getting that predicate nearly right: branching on
    `upstream is None` agreed with the original everywhere EXCEPT the column where a
    failure object carries no `httpStatus`, and in that column it either went silent or
    invented a status-derived key -- 24 of 364 enumerated cells, none of them covered by a
    fixture. These strings key `post_hoc_attempt_classes` in a PUBLISHED artefact, so the
    equivalence is not a matter of care: `test_legacy_ladder_is_equivalent_over_the_whole
    _shape_space` re-derives the original from git and asserts zero differences over every
    cell.
    """
    response = attempt.get("response")
    # isinstance guard only: the original indexes `response` directly, and validators.
    # load_attempt already rejects a non-dict response before scan ever sees it, so this
    # can differ from the original only on inputs the loader refuses outright.
    fail = ((response or {}).get("failure") or {}) if isinstance(response, dict) else {}
    if not isinstance(fail, dict):
        fail = {}
    if not fail:
        st = attempt.get("status")
        if isinstance(st, int) and st >= 400:
            return f"UPSTREAM_{st}" if st in (504, 502, 503) else f"OTHER_{st}"
        return None
    up = fail.get("httpStatus")
    if up == 500:
        return "ENGINE_INVALID_RESULT"
    if up == 413:
        return "RIG_CEILING_413"
    if up == 504 or attempt.get("status") == 504:
        return "UPSTREAM_504"
    return f"OTHER_{up}"
