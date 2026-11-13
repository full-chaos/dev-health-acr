#!/usr/bin/env python3
"""Print one line per mermaid block (file, block index, node count, edge count).

Quoted labels are blanked first, so a label edit cannot change the counts; a
changed node or edge does. Run it at two refs and diff to prove a text sweep
left every diagram's structure alone.
"""
import re
import subprocess
import sys

EDGE = re.compile(r"(<?-{2,}[>ox-]?|<?={2,}>?|-\.+-?>?)")
NODE = re.compile(r"([A-Za-z_][A-Za-z0-9_]*)\s*(?:\[|\(|\{|>|\)|\])?")


def blocks(text):
    out, cur = [], None
    for line in text.split("\n"):
        if cur is None:
            if re.match(r"^\s*```+\s*mermaid\b", line):
                cur = []
        elif re.match(r"^\s*```+\s*$", line):
            out.append(cur)
            cur = None
        else:
            cur.append(line)
    return out


def counts(block):
    first = next((l.strip() for l in block if l.strip()), "")
    if not re.match(r"^(flowchart|graph)\b", first):
        # sequence and other diagrams: unquoted text sits in the statements, so count statements and arrows
        stmts = [l for l in block if l.strip()]
        return len(stmts), sum(len(re.findall(r"-->>|->>|--x|-x|-->|->", l.split(":", 1)[0])) for l in stmts)
    body = []
    for line in block:
        line = re.sub(r'"[^"]*"', '""', line)
        line = re.sub(r"%%.*$", "", line)
        body.append(line)
    nodes, edges = set(), 0
    for line in body:
        if re.match(r"^\s*(subgraph|end|direction|flowchart|graph|classDef|class|style|linkStyle|click)\b", line):
            continue
        edges += len(EDGE.findall(line))
        for m in re.finditer(r"(?:^|[\s;&|>-])([A-Za-z_][A-Za-z0-9_]*)\s*(?=[\[({]|-|=|\.|$|\s*&|;|\|)", line):
            nodes.add(m.group(1))
    return len(nodes), edges


def main():
    ref = sys.argv[1] if len(sys.argv) > 1 else None
    if ref:
        names = subprocess.check_output(["git", "ls-tree", "-r", "--name-only", ref, "docs"], text=True).split()
    else:
        names = subprocess.check_output(["git", "ls-files", "docs"], text=True).split()
    for name in sorted(n for n in names if n.endswith(".md")):
        text = (subprocess.check_output(["git", "show", f"{ref}:{name}"], text=True) if ref else open(name).read())
        for i, b in enumerate(blocks(text), 1):
            n, e = counts(b)
            print(f"{name}\t{i}\tnodes={n}\tedges={e}")


main()
