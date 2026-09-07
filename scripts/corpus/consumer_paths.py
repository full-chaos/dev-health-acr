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


def _rule_for(expr, schema, roots, cells=None):
    """The schema RULE an expression reads through, if it reads through one.

    `(failure.get("narrowerContinuation") or {}).get("axis")` reads through a field that is
    DECLARED and never observed, so no child node was measured for it. That is a known gap
    recorded in the schema, not an unresolvable base, and conflating the two would make the
    sweep fail on a hole it already documents.
    """
    inner = expr
    if isinstance(inner, ast.BoolOp) and inner.values:
        inner = inner.values[0]
    if isinstance(inner, ast.Call) and isinstance(inner.func, ast.Attribute) \
            and inner.func.attr == "get" and inner.args \
            and isinstance(inner.args[0], ast.Constant):
        base = _resolve(inner.func.value, schema, roots, cells)
        if isinstance(base, str) and base in schema:
            return (schema[base] or {}).get(inner.args[0].value)
    if isinstance(inner, ast.Subscript) and isinstance(inner.slice, ast.Constant):
        base = _resolve(inner.value, schema, roots, cells)
        if isinstance(base, str) and base in schema:
            return (schema[base] or {}).get(inner.slice.value)
    return None


def _resolve(expr, schema, roots, cells=None):
    """The schema node this expression evaluates to, or None.

    Handles the shapes round 1 found missing: subscripted results, `.get()` chains,
    `(x.get(k) or {})`, `next(...)`/`list(...)` wrappers around a resolvable base, and
    names bound to any of those.
    """
    # (x or {}) / (x or [])
    if isinstance(expr, ast.BoolOp) and isinstance(expr.op, ast.Or) and expr.values:
        return _resolve(expr.values[0], schema, roots, cells)
    if isinstance(expr, ast.Name):
        got = roots.get(expr.id)
        return None if got == UNRESOLVED else got
    # A stash: `memory["kind"]` after `memory["kind"] = (rid, sn["kind_options"])`.
    # This is the shape that forced `candidates[].receipt_id` to be DECLARED rather than
    # derived -- the list is put in a local dict as half of a tuple, unpacked turns later,
    # then indexed. Following it is what makes the class derivable instead of hand-listed.
    if cells is not None and isinstance(expr, ast.Subscript) \
            and isinstance(expr.value, ast.Name) \
            and isinstance(expr.slice, ast.Constant):
        got = cells.get((expr.value.id, expr.slice.value))
        if got is not None:
            return got
    # x["k"] and x[0]
    if isinstance(expr, ast.Subscript):
        base = _resolve(expr.value, schema, roots, cells)
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
        base = _resolve(expr.func.value, schema, roots, cells)
        if base is None:
            return None
        return _child(schema, base, expr.args[0].value)
    # [o for o in <resolvable>] -- the comprehension's value is the element node, which is
    # how `match = [o for o in opts if ...]` then `match[0]["receipt_id"]` reaches the
    # boundary. Leaving this unresolved is what forced receipt_id to be declared by hand.
    if isinstance(expr, (ast.ListComp, ast.SetComp, ast.GeneratorExp)) \
            and expr.generators:
        gen = expr.generators[0]
        it = _resolve(gen.iter, schema, roots, cells)
        if it and isinstance(gen.target, ast.Name):
            inner = dict(roots)
            inner[gen.target.id] = it
            return _resolve(expr.elt, schema, inner, cells)
        return None
    # `x if cond else y` -- both arms should agree; take the first that resolves
    if isinstance(expr, ast.IfExp):
        return (_resolve(expr.body, schema, roots, cells)
                or _resolve(expr.orelse, schema, roots, cells))
    # list(x) / next(iter(x)) / sorted(x) -- wrappers that preserve the element node
    if isinstance(expr, ast.Call) and isinstance(expr.func, ast.Name) \
            and expr.func.id in ("list", "sorted", "next", "iter", "reversed") \
            and expr.args:
        return _resolve(expr.args[0], schema, roots, cells)
    return None


UNRESOLVED = "<unresolved>"


def _tuple_of(expr, schema, roots, cells):
    """Resolve a tuple/list literal element-wise, so a stashed pair keeps its shape."""
    if isinstance(expr, (ast.Tuple, ast.List)):
        return tuple(_resolve(e, schema, roots, cells) for e in expr.elts)
    return None


def _rebind(roots, name, value):
    """Rebind a name, but NEVER drop a SEED.

    SEEDS is a declared convention -- these names mean these nodes -- and `run_shard` does
    `ok, a, _ = load_attempt(f)`, whose call the sweep cannot resolve. Popping `a` there
    unbound the seed and silently lost every read off it: eight in run_shard, nine in
    subject_identity, all of them real. A rebind we cannot follow must not repeal the
    convention; the seed stands.
    """
    if value:
        roots[name] = value
    elif name in SEEDS:
        roots[name] = SEEDS[name]
    else:
        roots.pop(name, None)


def _artefact_derived(expr, roots):
    """Does this expression read off something artefact-shaped, even if it does not
    resolve to a node? A read off such a value is a boundary question we cannot answer,
    which is exactly the case that must be reported rather than skipped.

    A dict or list LITERAL is excluded however it was populated: `rec = {...}` built from
    artefact fields is the instrument's OWN record, and reads off it are not boundary
    questions. Reporting them was a false positive that would have made the pin unusable.
    """
    if isinstance(expr, (ast.Dict, ast.List, ast.Set, ast.Tuple)):
        return False
    for sub in ast.walk(expr):
        if isinstance(sub, ast.Name) and sub.id in roots:
            return True
    return False


def _walk(node, schema, roots, reads, unresolved, label, cells):
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
        _walk(node.iter, schema, roots, reads, unresolved, label, cells)
        inner = dict(roots)
        if isinstance(node.target, ast.Name):
            got = _resolve(node.iter, schema, roots, cells)
            if got:
                inner[node.target.id] = got
            elif node.target.id not in SEEDS \
                    and (node.target.id in roots
                         or _artefact_derived(node.iter, roots)):
                # The loop rebinds a name that WAS a node (or iterates something
                # artefact-derived we cannot follow). Keeping the old binding would make
                # the sweep invent reads; dropping it silently would make the sweep miss
                # them. Taint it, so reads off it are REPORTED.
                inner[node.target.id] = UNRESOLVED
            else:
                _rebind(inner, node.target.id, None)
        for child in node.body + node.orelse:
            _walk(child, schema, inner, reads, unresolved, label, cells)
        return

    if isinstance(node, (ast.ListComp, ast.SetComp, ast.GeneratorExp, ast.DictComp)):
        inner = dict(roots)
        for gen in node.generators:
            _walk(gen.iter, schema, inner, reads, unresolved, label, cells)
            if isinstance(gen.target, ast.Name):
                got = _resolve(gen.iter, schema, inner, cells)
                if got:
                    inner[gen.target.id] = got
                else:
                    inner.pop(gen.target.id, None)
        parts = ([node.key, node.value] if isinstance(node, ast.DictComp)
                 else [node.elt])
        for gen in node.generators:
            parts += gen.ifs
        for part in parts:
            _walk(part, schema, inner, reads, unresolved, label, cells)
        return

    if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)):
        inner = dict(SEEDS)          # a function starts from the seeds, not the caller's
        for child in node.body:
            _walk(child, schema, inner, reads, unresolved, label, cells)
        return

    # `a, b = <resolvable>` binds every element target. `rid, opts = memory["kind"]` is the
    # shape `receipt_id` travels through, and leaving tuple targets unbound is why that path
    # had to be declared by hand instead of derived.
    if isinstance(node, ast.Assign) and len(node.targets) == 1 \
            and isinstance(node.targets[0], (ast.Tuple, ast.List)):
        _record(node.value, schema, roots, reads, unresolved, label, cells)
        for child in ast.iter_child_nodes(node.value):
            _walk(child, schema, roots, reads, unresolved, label, cells)
        got = _resolve(node.value, schema, roots, cells)
        # A tuple is not a node: unpack it element-wise BEFORE treating `got` as one, or
        # both names bind to the whole pair and every later read looks like a read off a
        # tuple.
        tup = got if isinstance(got, tuple) else None
        if tup is None:
            tup = _tuple_of(node.value, schema, roots, cells)
            if tup is None and isinstance(node.value, ast.Subscript) \
                    and isinstance(node.value.value, ast.Name) \
                    and isinstance(node.value.slice, ast.Constant):
                stash = cells.get((node.value.value.id, node.value.slice.value))
                if isinstance(stash, tuple):
                    tup = stash
        if tup is not None:
            for elt, val in zip(node.targets[0].elts, tup):
                if isinstance(elt, ast.Name):
                    _rebind(roots, elt.id, val)
            return
        for elt in node.targets[0].elts:
            if isinstance(elt, ast.Name):
                if got:
                    roots[elt.id] = got
                elif elt.id not in SEEDS and _artefact_derived(node.value, roots):
                    roots[elt.id] = UNRESOLVED
                else:
                    _rebind(roots, elt.id, None)
        return

    # A STASH WRITE: `memory["kind"] = (rid, sn["kind_options"])`. The value is remembered
    # per (name, key) so a later `rid, opts = memory["kind"]` can bind `opts` to the node
    # the list came from.
    if isinstance(node, ast.Assign) and len(node.targets) == 1 \
            and isinstance(node.targets[0], ast.Subscript) \
            and isinstance(node.targets[0].value, ast.Name) \
            and isinstance(node.targets[0].slice, ast.Constant):
        _record(node.value, schema, roots, reads, unresolved, label, cells)
        for child in ast.iter_child_nodes(node.value):
            _walk(child, schema, roots, reads, unresolved, label, cells)
        cell = (node.targets[0].value.id, node.targets[0].slice.value)
        val = _resolve(node.value, schema, roots, cells) \
            or _tuple_of(node.value, schema, roots, cells)
        if val:
            cells[cell] = val
        return

    # A binding assignment updates THIS environment and is then walked for its own reads.
    if isinstance(node, ast.Assign) and len(node.targets) == 1 \
            and isinstance(node.targets[0], ast.Name):
        _record(node.value, schema, roots, reads, unresolved, label, cells)
        for child in ast.iter_child_nodes(node.value):
            _walk(child, schema, roots, reads, unresolved, label, cells)
        got = _resolve(node.value, schema, roots)
        if got:
            roots[node.targets[0].id] = got
        else:
            _rebind(roots, node.targets[0].id, None)
            # TAINT. The value came off artefact-shaped data but did not resolve to a node
            # -- an unknown key, or a shape this sweep cannot follow. Reads off it must be
            # REPORTED, not dropped: `x = result.get("nowhere"); x["deep"]` was returning
            # one read and an EMPTY unresolved list, so "reports rather than skips" was
            # true of the code and false of everything downstream.
            if node.targets[0].id not in SEEDS \
                    and _artefact_derived(node.value, roots):
                roots[node.targets[0].id] = UNRESOLVED
        return

    _record(node, schema, roots, reads, unresolved, label, cells)
    for child in ast.iter_child_nodes(node):
        _walk(child, schema, roots, reads, unresolved, label, cells)


def _record(node, schema, roots, reads, unresolved, label, cells):
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
    if isinstance(base_expr, ast.Name) and roots.get(base_expr.id) == UNRESOLVED:
        unresolved.append(f"{label}: {base_expr.id}[{key!r}] read off an unresolved "
                          "artefact-derived value")
        return
    parent_rule = _rule_for(base_expr, schema, roots, cells)
    if parent_rule is not None and parent_rule.get("unmeasured"):
        return              # declared-unmeasured object: no node exists to descend into
    base = _resolve(base_expr, schema, roots, cells)
    if isinstance(base, tuple):
        return                      # a stashed pair, not a node: the unpack binds it
    if base is None:
        if _artefact_derived(base_expr, roots):
            unresolved.append(f"{label}: {ast.dump(base_expr)[:60]}[{key!r}] unresolved")
        return
    if base == UNRESOLVED:
        unresolved.append(f"{label}: [{key!r}] read off an unresolved value")
        return
    reads.add((base, key, uncond))


def sweep(src, schema, label="<src>"):
    """(reads, unresolved).

    `reads` is a set of (node, key, unconditional). `unresolved` lists reads whose base
    could not be resolved -- reported so the caller can FAIL on them rather than skip.
    """
    reads, unresolved = set(), []
    cells = {}
    tree = ast.parse(src)
    # Two passes: a stash may be written after the code that reads it, and a single pass
    # would see the read before the write and call it unresolvable.
    for _ in range(2):
        reads, unresolved = set(), []
        roots = dict(SEEDS)
        for node in tree.body:
            _walk(node, schema, roots, reads, unresolved, label, cells)
    return reads, unresolved
