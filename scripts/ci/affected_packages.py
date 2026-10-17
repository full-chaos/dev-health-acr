#!/usr/bin/env python3
"""Choose the packages a pull request's Go tests must cover.

Usage: affected_packages.py NAME_STATUS_FILE [--deps-file FILE]

NAME_STATUS_FILE is `git diff --name-status --no-renames BASE...HEAD`
(one `<status>\t<path>` per line). Output, on stdout:

    full <reason>          run the whole suite
    subset                 followed by one import path per line

The rule fails wide. SUBSET only when EVERY changed path is an added or
modified .go file under internal/ or cmd/, outside any testdata directory.
Any other path anywhere (docs, root files, go.mod, workflows, unknown
directories, a deletion, a rename) is FULL, because a test may read a repo
file that the import graph cannot see (//go:embed, testdata, golden files,
SQL, JSON, workflow text). A changed directory that `go list` does not know
(a package excluded by build tags) is FULL, and so is any error.

Affected packages = the changed packages plus every package whose test
binary imports one of them, directly or transitively. The closure comes from
ONE `go list -test` call, so test-only imports count.

Source-reading guards always run. A package is a guard when its code or tests
import a Go-source toolchain package (go/parser, go/ast, go/build, go/types,
golang.org/x/tools/go/..., see SOURCE_TOOL_IMPORTS): decided from the import
lists `go list` resolves, so no call shape can hide it. Walking, loading and
file-read patterns (below) are a second net.
"""
import os
import re
import subprocess
import sys

SUBSET_ROOTS = ("internal/", "cmd/")

# A package whose Go code reads source outside its own directory (a census
# that walks the module, a packages.Load, a `go list`, the repo-root helper)
# can change result when ANY .go file changes, which the import graph cannot
# see. Those packages are derived from the source here, never listed by hand,
# and always run. A walk or parse of "." reads only the package's own files,
# which the changed-package rule already covers.
AMBIENT = re.compile(
    r"filepath\.Walk(?:Dir)?\(\s*(?!\"\.\")"
    r"|parser\.ParseDir\(\s*[\w.]+\s*,\s*(?!\"\.\")"
    r"|packages\.Load\("
    r"|repopath\."
    r"|\"go\"\s*,\s*\"list\""
)
# A file that opens repo files AND reaches above its own directory (a "..",
# the repo-root helper, runtime.Caller) reads sources the import graph cannot
# see: log-event and drift guards, contract and doc parity checks.
FILE_READ = re.compile(
    r"os\.(?:ReadFile|Open|ReadDir)\(|parser\.ParseFile\(|filepath\.Glob\(|os\.DirFS\("
)
REACHES_UP = re.compile(r"\"\.\.\"|\.\./|repopath|runtime\.Caller|repoRoot|moduleRoot|projectRoot")
# ... and the same file works with Go source (a .go path or the go/* parsers).
# Reading docs, JSON, SQL or golden files above the package is not enough: a
# change to one of those is a non-Go path, which is always the full suite.
GO_SOURCE = re.compile(r"\.go[\"`]|go/parser|go/ast|go/types|go/token")


def reads_source_tree(text):
    if AMBIENT.search(text):
        return True
    return bool(FILE_READ.search(text) and REACHES_UP.search(text) and GO_SOURCE.search(text))


SKIP_DIRS = {".git", "vendor", "node_modules"}

# A package that imports a Go-source toolchain package (in its code or its
# tests) can inspect source files of other packages through any call shape, so
# it is a source-reading guard. Decided from the import lists `go list`
# resolves, never from call syntax. Lines: ImportPath|Imports TestImports
# XTestImports (space separated).
SOURCE_TOOL_IMPORTS = {
    "go/ast", "go/build", "go/constant", "go/doc", "go/format", "go/importer",
    "go/parser", "go/printer", "go/scanner", "go/token", "go/types",
}
SOURCE_TOOL_PREFIXES = ("golang.org/x/tools/go/", "golang.org/x/tools/imports")


def imports_source_tooling(imports):
    for imp in imports:
        if imp in SOURCE_TOOL_IMPORTS or imp.startswith(SOURCE_TOOL_PREFIXES):
            return True
    return False


def full(reason):
    print("full " + reason)
    sys.exit(0)


def base_name(import_path):
    name = import_path.split(" [", 1)[0]
    if name.endswith(".test"):
        name = name[: -len(".test")]
    if name.endswith("_test"):
        name = name[: -len("_test")]
    return name


def read_module():
    with open("go.mod", encoding="utf-8") as f:
        for line in f:
            if line.startswith("module "):
                return line.split()[1]
    full("go.mod has no module line")


def read_deps(deps_file):
    if deps_file:
        with open(deps_file, encoding="utf-8") as f:
            return f.read()
    out = subprocess.run(
        ["go", "list", "-test", "-f", "{{.ImportPath}}|{{join .Deps \" \"}}", "./..."],
        check=True, capture_output=True, text=True,
    )
    return out.stdout


def read_imports(imports_file):
    if imports_file:
        with open(imports_file, encoding="utf-8") as f:
            return f.read()
    out = subprocess.run(
        ["go", "list", "-f",
         "{{.ImportPath}}|{{join .Imports \" \"}} {{join .TestImports \" \"}} {{join .XTestImports \" \"}}",
         "./..."],
        check=True, capture_output=True, text=True,
    )
    return out.stdout


def main():
    args = sys.argv[1:]
    deps_file = None
    imports_file = None
    if "--deps-file" in args:
        i = args.index("--deps-file")
        deps_file = args[i + 1]
        del args[i : i + 2]
    if "--imports-file" in args:
        i = args.index("--imports-file")
        imports_file = args[i + 1]
        del args[i : i + 2]
    if len(args) != 1:
        full("usage error")
    with open(args[0], encoding="utf-8") as f:
        lines = [ln.rstrip("\n") for ln in f if ln.strip()]
    if not lines:
        full("no changed paths")

    module = read_module()
    changed_dirs = set()
    for ln in lines:
        parts = ln.split("\t")
        if len(parts) != 2:
            full("unparsable change line")
        status, path = parts
        if status not in ("A", "M"):
            full("status %s on %s" % (status, path))
        if not path.endswith(".go") or not path.startswith(SUBSET_ROOTS):
            full("non-Go-under-internal-or-cmd path " + path)
        if "/testdata/" in path or '"' in path or "\\" in path:
            full("testdata or quoted path " + path)
        changed_dirs.add(path.rsplit("/", 1)[0])

    try:
        listing = read_deps(deps_file)
    except Exception as exc:  # any failure widens
        full("go list failed: %s" % exc)

    owners = {}
    packages = set()
    for ln in listing.splitlines():
        if "|" not in ln:
            continue
        name, deps = ln.split("|", 1)
        owner = base_name(name)
        if not owner.startswith(module):
            continue
        packages.add(owner)
        dep_set = owners.setdefault(owner, set())
        for d in deps.split():
            d = d.split(" [", 1)[0]
            if d.startswith(module):
                dep_set.add(base_name(d) if d.endswith(".test") else d)

    ambient = set()
    try:
        import_listing = read_imports(imports_file)
    except Exception as exc:
        full("go list (imports) failed: %s" % exc)
    for ln in import_listing.splitlines():
        if "|" not in ln:
            continue
        name, imports = ln.split("|", 1)
        if name.startswith(module) and imports_source_tooling(imports.split()):
            ambient.add(name)
    for root, dirs, files in os.walk("."):
        dirs[:] = [d for d in dirs if d not in SKIP_DIRS and d != "testdata"]
        rel = os.path.relpath(root, ".")
        pkg = module if rel == "." else module + "/" + rel.replace(os.sep, "/")
        if pkg not in packages:
            continue
        for name in files:
            if not name.endswith(".go"):
                continue
            with open(os.path.join(root, name), encoding="utf-8", errors="replace") as f:
                if reads_source_tree(f.read()):
                    ambient.add(pkg)
                    break

    changed = set()
    for d in changed_dirs:
        pkg = module + "/" + d
        if pkg not in packages:
            full("changed directory is not a listed package: " + d)
        changed.add(pkg)

    affected = set(changed) | ambient
    for owner, dep_set in owners.items():
        if dep_set & changed:
            affected.add(owner)
    print("subset")
    for pkg in sorted(affected):
        print(pkg)


if __name__ == "__main__":
    try:
        main()
    except SystemExit:
        raise
    except Exception as exc:
        full("selector error: %s" % exc)
