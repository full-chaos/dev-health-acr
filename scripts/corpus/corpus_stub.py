"""Scoped installation of the synthetic corpus, for tests only.

Round 2 found that installing `corpus_example` as `corpus` at import time left it in
sys.modules for whatever ran next. Round 3 found the first fix insufficient: restoring the
`corpus` KEY does not undo modules that imported it while the alias was live -- after the
context exited, `merge_corpus.CORPUS` still held four synthetic rows, so a later in-process
merge could score against the example corpus and look entirely normal.

The isolation is therefore a SNAPSHOT of the whole sys.modules dict, restored exactly on
exit. Any module first imported inside the block is evicted with it, so nothing can carry
the synthetic corpus out.
"""
import sys
from contextlib import contextmanager


@contextmanager
def using_example_corpus():
    import corpus_example
    snapshot = dict(sys.modules)
    sys.modules["corpus"] = corpus_example
    try:
        yield corpus_example
    finally:
        # evict everything imported inside the block, then restore the exact prior state
        for name in [n for n in sys.modules if n not in snapshot]:
            del sys.modules[name]
        for name, mod in snapshot.items():
            sys.modules[name] = mod
        if "corpus" not in snapshot:
            sys.modules.pop("corpus", None)
