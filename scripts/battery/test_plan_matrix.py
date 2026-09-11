#!/usr/bin/env python3
"""Executed pin for plan_matrix.py's language-aware _SENTINEL file choice.

The _SENTINEL arm (run_arm.sh) proves the mutation-apply mechanism is inert
by appending `// mutation-battery sentinel: an inert comment...` to a target
file. That line is legal Go anywhere after the last top-level declaration --
it is NOT a comment in JSON or YAML at all, it is trailing garbage that
breaks every consumer's parse.

Hit live: acr PR #501's hosted battery (run 34609368911) had an all-JSON
mutant table; the old default (mutants[0]["file"], no language check) picked
one of those .json files as the sentinel target, and the sentinel arm went
HARNESS_ERROR with 119 named test failures -- every one of them downstream of
the same broken JSON parse, none a real finding. Per aggregate.py's own rule
("a red sentinel means the harness may be false-killing"), that voided a run
whose 11 real mutants had ALL independently reported KILLED, both locally and
on the hosted runners.

This file does two things: (1) proves choose_sentinel_file() picks correctly
for an all-JSON table, a mixed table, and refuses a JSON/YAML override; (2)
EXECUTES the actual sentinel edit against a real copy of the fallback file
and a real copy of the JSON file that broke on #501 -- reproducing both the
original red and the fix's green directly, not just arguing about them. Every
mutated file is restored in a `finally`, verified byte-identical afterward.
"""
import json
import subprocess
import sys
from pathlib import Path

HERE = Path(__file__).parent
REPO_ROOT = HERE.parent.parent
sys.path.insert(0, str(HERE))

from plan_matrix import SENTINEL_FALLBACK_GO_FILE, choose_sentinel_file  # noqa: E402

# The exact text run_arm.sh:152 appends -- kept byte-identical to that file so
# this pin tests the REAL edit, not a plausible stand-in for it.
SENTINEL_COMMENT = (
    "\n// mutation-battery sentinel: an inert comment. If this arm goes red the\n"
    "// harness false-kills and every KILLED verdict in this run is void.\n"
)

FAILURES = []


def _check(name, condition, detail=""):
    if condition:
        print("PASS: %s" % name)
    else:
        FAILURES.append(name)
        print("FAIL: %s%s" % (name, (" -- " + detail) if detail else ""))


def test_all_json_table_selects_the_go_fallback():
    mutants = [
        {"id": "M1", "file": "contracts/jsonschema/v1/a.schema.json"},
        {"id": "M2", "file": "contracts/jsonschema/v1/b.schema.json"},
    ]
    chosen = choose_sentinel_file(mutants, override="")
    _check(
        "an all-JSON table selects the Go fallback",
        chosen == SENTINEL_FALLBACK_GO_FILE,
        "got %r, want %r" % (chosen, SENTINEL_FALLBACK_GO_FILE),
    )


def test_mixed_table_selects_the_first_go_file():
    mutants = [
        {"id": "M1", "file": "contracts/jsonschema/v1/a.schema.json"},
        {"id": "M2", "file": "internal/contextfabric/fact_scope.go"},
        {"id": "M3", "file": "internal/contextfabric/other.go"},
    ]
    chosen = choose_sentinel_file(mutants, override="")
    _check(
        "a mixed table selects the first .go mutant file, not the first mutant overall",
        chosen == "internal/contextfabric/fact_scope.go",
        "got %r" % chosen,
    )


def test_json_override_is_refused():
    mutants = [{"id": "M1", "file": "contracts/jsonschema/v1/a.schema.json"}]
    try:
        choose_sentinel_file(mutants, override="contracts/jsonschema/v1/a.schema.json")
    except ValueError:
        _check("an explicit JSON --sentinel-file override is refused", True)
        return
    _check("an explicit JSON --sentinel-file override is refused", False, "no exception raised")


def test_yaml_override_is_refused():
    mutants = [{"id": "M1", "file": "some/file.go"}]
    try:
        choose_sentinel_file(mutants, override="some/config.yaml")
    except ValueError:
        _check("an explicit YAML --sentinel-file override is refused", True)
        return
    _check("an explicit YAML --sentinel-file override is refused", False, "no exception raised")


def test_go_override_passes_through_unchanged():
    mutants = [{"id": "M1", "file": "contracts/jsonschema/v1/a.schema.json"}]
    chosen = choose_sentinel_file(mutants, override="cmd/acr-mcp/main.go")
    _check(
        "an explicit .go --sentinel-file override passes through unchanged",
        chosen == "cmd/acr-mcp/main.go",
        "got %r" % chosen,
    )


def test_the_fallback_go_file_stays_green_under_the_real_sentinel_edit():
    """EXECUTED: append the REAL sentinel text to a real copy of the fallback
    file, run `go vet` on its package, assert rc=0. Proves the fallback
    choice is actually safe, not merely plausible."""
    target = REPO_ROOT / SENTINEL_FALLBACK_GO_FILE
    original = target.read_text(encoding="utf-8")
    try:
        target.write_text(original + SENTINEL_COMMENT, encoding="utf-8")
        pkg_dir = "./" + str(Path(SENTINEL_FALLBACK_GO_FILE).parent) + "/"
        proc = subprocess.run(
            ["go", "vet", pkg_dir], cwd=REPO_ROOT, capture_output=True, text=True
        )
        _check(
            "the fallback file stays green under the real sentinel edit (go vet)",
            proc.returncode == 0,
            "rc=%d stderr=%s" % (proc.returncode, proc.stderr[-2000:]),
        )
    finally:
        target.write_text(original, encoding="utf-8")
        restored = target.read_text(encoding="utf-8")
        if restored != original:
            FAILURES.append("restore of %s failed -- tree left dirty" % SENTINEL_FALLBACK_GO_FILE)
            print("FAIL: restore of %s failed -- tree left dirty" % SENTINEL_FALLBACK_GO_FILE)


def test_a_json_target_goes_red_under_the_same_sentinel_edit():
    """EXECUTED: reproduce the ORIGINAL #501 failure shape directly against
    the same file that actually broke -- json.loads is the language-neutral
    stand-in for the Go side's encoding/json.Unmarshal, same defect class."""
    target = REPO_ROOT / "contracts/jsonschema/v1/context_fabric_common.v1.schema.json"
    original = target.read_text(encoding="utf-8")
    try:
        try:
            json.loads(original)
        except json.JSONDecodeError as exc:
            FAILURES.append("control: the untouched JSON fixture is not valid JSON")
            print("FAIL: control: the untouched JSON fixture is not valid JSON -- %s" % exc)
            return
        target.write_text(original + SENTINEL_COMMENT, encoding="utf-8")
        try:
            json.loads(target.read_text(encoding="utf-8"))
        except json.JSONDecodeError:
            _check(
                "the same sentinel edit makes the JSON target invalid JSON (the original defect)",
                True,
            )
            return
        _check(
            "the same sentinel edit makes the JSON target invalid JSON (the original defect)",
            False,
            "json.loads did not raise -- the reproduction is no longer valid",
        )
    finally:
        target.write_text(original, encoding="utf-8")
        restored = target.read_text(encoding="utf-8")
        if restored != original:
            FAILURES.append("restore of the JSON fixture failed -- tree left dirty")
            print("FAIL: restore of the JSON fixture failed -- tree left dirty")


TESTS = [
    test_all_json_table_selects_the_go_fallback,
    test_mixed_table_selects_the_first_go_file,
    test_json_override_is_refused,
    test_yaml_override_is_refused,
    test_go_override_passes_through_unchanged,
    test_the_fallback_go_file_stays_green_under_the_real_sentinel_edit,
    test_a_json_target_goes_red_under_the_same_sentinel_edit,
]


if __name__ == "__main__":
    for t in TESTS:
        t()
    if FAILURES:
        print("\n%d FAILURE(S): %s" % (len(FAILURES), ", ".join(FAILURES)))
        sys.exit(1)
    print("\nALL %d PASS" % len(TESTS))
