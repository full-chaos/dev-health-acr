#!/usr/bin/env python3
"""ONE implementation of "what does the code read off an artefact". CHAOS-5430 r2.

Two things need this answer and they must not answer it differently: the generator, which
marks a key REQUIRED when consumers dereference it unconditionally, and the totality pin,
which fails when a consumer reads a path the boundary does not type. Round 1 found the pin
recognising only `Name["key"]` and `Name.get("key")`, so `match[0]["receipt_id"]` and
`(failure.get("x") or {}).get("y")` were invisible to it -- and the pin passed while a
missing `receipt_id` crashed the consumer.

So the sweep walks the EXPRESSION, not the variable name. Any subscript or `.get()` whose
base resolves to a schema node is a read of that node, however the base was written: an
index result, a chained `.get()`, an `or {}` fallback, a temporary, a call result held in a
name. A base it cannot resolve is REPORTED, never skipped -- an unresolvable read is
exactly where a real one hides, and silently ignoring it is what let the pin pass
vacuously against the merge base.

UNCONDITIONAL means `x["k"]`, which raises when the key is absent. `x.get("k")` tolerates
absence and says so, so it does not make a key required.
"""
import ast

# Entry points: a name that IS an artefact-shaped object at the start of a chain.
SEEDS = {
    "result": "attempt.response.result",
    "prev_result": "attempt.response.result",
    "res": "attempt.response.result",
    "sr": "attempt.response.result.subject_resolution",
    "failure": "attempt.response.failure",
    "fail": "attempt.response.failure",
    "resp": "attempt.response",
    "payload": "attempt.response",
    "response": "attempt.response",
    "a": "attempt",
    "attempt": "attempt",
    "last": "attempt",
}


def _child(schema, node, key):
    """The node a key leads to, if the schema says it leads anywhere."""
    rule = (schema.get(node) or {}).get(key) or {}
    return rule.get("node") or rule.get("element_node")


def _resolve(expr, schema, roots):
    """The schema node this expression evaluates to, or None.

    Handles the shapes round 1 found missing: subscripted results, `.get()` chains,
    `(x.get(k) or {})`, `next(...)`/`list(...)` wrappers around a resolvable base, and
    names bound to any of those.
    """
    # (x or {}) / (x or [])
    if isinstance(expr, ast.BoolOp) and isinstance(expr.op, ast.Or) and expr.values:
        return _resolve(expr.values[0], schema, roots)
    if isinstance(expr, ast.Name):
        return roots.get(expr.id)
    # x["k"] and x[0]
    if isinstance(expr, ast.Subscript):
        base = _resolve(expr.value, schema, roots)
        if base is None:
            return None
        if isinstance(expr.slice, ast.Constant) and isinstance(expr.slice.value, str):
            return _child(schema, base, expr.slice.value)
        return base            # an INDEX into an array keeps the element node
    # x.get("k")
    if isinstance(expr, ast.Call) and isinstance(expr.func, ast.Attribute) \
            and expr.func.attr == "get" and expr.args \
            and isinstance(expr.args[0], ast.Constant) \
            and isinstance(expr.args[0].value, str):
        base = _resolve(expr.func.value, schema, roots)
        if base is None:
            return None
        return _child(schema, base, expr.args[0].value)
    # list(x) / next(iter(x)) / sorted(x) -- wrappers that preserve the element node
    if isinstance(expr, ast.Call) and isinstance(expr.func, ast.Name) \
            and expr.func.id in ("list", "sorted", "next", "iter", "reversed") \
            and expr.args:
        return _resolve(expr.args[0], schema, roots)
    return None


def _walk(node, schema, roots, reads, unresolved, label):
    """Recursive walk carrying a BINDING ENVIRONMENT.

    A name-keyed map for the whole function cannot represent real code: `subject_identity`
    binds `c` in two consecutive loops, first over `committed[]` and then over
    `candidates[]`, the way anyone writes those loops. A flat map keeps one winner and
    attributes every read in the function to it, so the sweep reported reads of
    `committed[].match_mechanisms` that no line performs. A sweep that INVENTS paths is as
    useless as one that misses them -- both make its output something to second-guess.

    So a loop or comprehension binds its target inside its own body only, and the
    environment is copied on the way in.
    """
    if isinstance(node, (ast.For, ast.AsyncFor)):
        _walk(node.iter, schema, roots, reads, unresolved, label)
        inner = dict(roots)
        if isinstance(node.target, ast.Name):
            got = _resolve(node.iter, schema, roots)
            if got:
                inner[node.target.id] = got
            else:
                inner.pop(node.target.id, None)
        for child in node.body + node.orelse:
            _walk(child, schema, inner, reads, unresolved, label)
        return

    if isinstance(node, (ast.ListComp, ast.SetComp, ast.GeneratorExp, ast.DictComp)):
        inner = dict(roots)
        for gen in node.generators:
            _walk(gen.iter, schema, inner, reads, unresolved, label)
            if isinstance(gen.target, ast.Name):
                got = _resolve(gen.iter, schema, inner)
                if got:
                    inner[gen.target.id] = got
                else:
                    inner.pop(gen.target.id, None)
        parts = ([node.key, node.value] if isinstance(node, ast.DictComp)
                 else [node.elt])
        for gen in node.generators:
            parts += gen.ifs
        for part in parts:
            _walk(part, schema, inner, reads, unresolved, label)
        return

    if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)):
        inner = dict(SEEDS)          # a function starts from the seeds, not the caller's
        for child in node.body:
            _walk(child, schema, inner, reads, unresolved, label)
        return

    # A binding assignment updates THIS environment and is then walked for its own reads.
    if isinstance(node, ast.Assign) and len(node.targets) == 1 \
            and isinstance(node.targets[0], ast.Name):
        _record(node.value, schema, roots, reads, unresolved, label)
        for child in ast.iter_child_nodes(node.value):
            _walk(child, schema, roots, reads, unresolved, label)
        got = _resolve(node.value, schema, roots)
        if got:
            roots[node.targets[0].id] = got
        else:
            roots.pop(node.targets[0].id, None)
        return

    _record(node, schema, roots, reads, unresolved, label)
    for child in ast.iter_child_nodes(node):
        _walk(child, schema, roots, reads, unresolved, label)


def _record(node, schema, roots, reads, unresolved, label):
    key = base_expr = None
    uncond = False
    if isinstance(node, ast.Subscript) and isinstance(node.slice, ast.Constant) \
            and isinstance(node.slice.value, str):
        base_expr, key, uncond = node.value, node.slice.value, True
    elif isinstance(node, ast.Call) and isinstance(node.func, ast.Attribute) \
            and node.func.attr == "get" and node.args \
            and isinstance(node.args[0], ast.Constant) \
            and isinstance(node.args[0].value, str):
        base_expr, key, uncond = node.func.value, node.args[0].value, False
    if key is None:
        return
    base = _resolve(base_expr, schema, roots)
    if base is None:
        if isinstance(base_expr, ast.Name) and base_expr.id in roots:
            unresolved.append(f"{label}: {base_expr.id}[{key!r}] unresolved")
        return
    reads.add((base, key, uncond))


def sweep(src, schema, label="<src>"):
    """(reads, unresolved).

    `reads` is a set of (node, key, unconditional). `unresolved` lists reads whose base
    could not be resolved -- reported so the caller can FAIL on them rather than skip.
    """
    reads, unresolved = set(), []
    roots = dict(SEEDS)
    for node in ast.parse(src).body:
        _walk(node, schema, roots, reads, unresolved, label)
    return reads, unresolved
