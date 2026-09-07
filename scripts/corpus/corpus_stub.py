"""Scoped installation of the synthetic corpus, for tests only.

r2 flagged that a pin file which installs `corpus_example` as `corpus` at import time
leaves it in `sys.modules` for whatever runs next -- the same contamination finding #9
was about, reintroduced by the pin file written to fix #9. The install is now SCOPED:
it exists for the duration of a `with` block and the previous state is restored exactly,
so importing a test module changes nothing globally.
"""
import sys
from contextlib import contextmanager


@contextmanager
def using_example_corpus():
    import corpus_example
    had = "corpus" in sys.modules
    prev = sys.modules.get("corpus")
    sys.modules["corpus"] = corpus_example
    try:
        yield corpus_example
    finally:
        if had:
            sys.modules["corpus"] = prev
        else:
            sys.modules.pop("corpus", None)
