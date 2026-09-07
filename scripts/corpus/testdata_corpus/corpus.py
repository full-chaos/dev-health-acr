"""A `corpus` module for tests, resolving to the synthetic example.

Isolation is by SUBPROCESS, not by mutating sys.modules. Earlier attempts installed the
example under the name `corpus` inside the running interpreter and tried to undo it
afterwards; neither restoring the key nor snapshotting the whole dict could retract a
reference a module had already captured, so a synthetic corpus could survive into a later
merge and look entirely normal.

A test that needs a corpus puts THIS directory on PYTHONPATH in a fresh interpreter. The
parent process never has a `corpus` module at all, so there is nothing to leak.
"""
from corpus_example import CORPUS, REQUESTED_KIND, ANCHOR_KIND  # noqa: F401
