#!/usr/bin/env bash
#
# Refuse a Go file whose NAME silently constrains the build.
#
# WHY THIS EXISTS. Go applies build constraints from the FILE NAME: a file
# called `foo_linux.go`, `foo_arm64.go` or `foo_linux_amd64.go` is compiled
# only on that platform. The rule also applies to `_test.go` files, and it
# applies whether or not the author meant it.
#
# `internal/contracts/v1/completeness_state_legacy_arm_test.go` meant "the
# legacy arm" as in a branch of a decision. Go read the trailing `_arm` as
# GOARCH=arm. The file carried no `//go:build` line, so nothing declared the
# constraint and nothing reported it: `go list` simply moved the file into
# IgnoredGoFiles, `go build` said nothing, `go test` said nothing, and the
# three tests inside it never compiled on any machine that has ever built this
# repository. A test that never runs still READS as protection in review, which
# is worse than no test at all.
#
# WHAT THIS ENFORCES. A platform suffix must be DELIBERATE, and the way to
# show that is an explicit `//go:build` line in the file. Genuine
# platform-specific files keep working -- they just have to say so twice, once
# in the name and once in a constraint a human wrote on purpose. An ACCIDENTAL
# suffix cannot pass, because the author would have to write a `//go:build arm`
# they do not mean.
#
# The GOOS/GOARCH vocabulary comes from `go tool dist list`, never a hardcoded
# table: a hardcoded one is wrong the first time the toolchain adds a port,
# and it would be a second authority for a question the toolchain already
# answers.

set -euo pipefail

cd "$(dirname "$0")/../.."

if ! command -v go >/dev/null 2>&1; then
    echo "FAIL: go is not on PATH; this check needs it for the GOOS/GOARCH vocabulary" >&2
    exit 2
fi

# The authoritative vocabulary, one token per line, deduplicated.
tokens="$(go tool dist list | tr '/' '\n' | sort -u)"

failed=0
checked=0

while IFS= read -r file; do
    checked=$((checked + 1))
    base="$(basename "$file" .go)"
    # A _test suffix is not itself a constraint; the constraint, if any, is the
    # token before it.
    stem="${base%_test}"
    last="${stem##*_}"
    # A name with no underscore has no suffix to misread.
    [ "$last" = "$stem" ] && continue
    printf '%s\n' "$tokens" | grep -qxF "$last" || continue

    # The name constrains the build. Demand that the file says so out loud.
    if grep -qE '^//go:build ' "$file"; then
        continue
    fi

    failed=$((failed + 1))
    cat >&2 <<MSG
FAIL: $file
  The name ends in "_$last", which Go reads as a build constraint, so this file
  is compiled only on that platform -- and it carries no //go:build line, so
  nothing declares that and nothing reports it.
  If the constraint is INTENDED, add an explicit //go:build line.
  If it is ACCIDENTAL, rename the file so the last token is not a GOOS/GOARCH.
MSG
done < <(git ls-files '*.go')

if [ "$failed" -ne 0 ]; then
    echo "build-constraint filenames: $failed offending file(s) of $checked checked" >&2
    exit 1
fi

echo "build-constraint filenames: ok ($checked Go files checked)"
