#!/usr/bin/env bash
# Print a URL with any userinfo (`user:pass@`) and query string stripped -- FOR LOGGING
# ONLY. Never use this output to make a real request: corpus_origin.sh's own output
# still carries whatever credentials CORPUS_BASE embeds, because the actual probe/
# request needs them. Same redaction shape as harness.py's own `_redacted_base()`.
#
# CHAOS-5562 r2: the launchers' readiness-probe abort message printed the probe URL
# (built from corpus_origin.sh's origin, which PRESERVES userinfo in `netloc`)
# verbatim -- a CORPUS_BASE carrying basic-auth credentials or a token leaked straight
# into the abort line on stderr. harness.py's own redaction covers only its own print
# statements, not this shell-side one.
set -euo pipefail
url="${1:?usage: corpus_redact.sh <url>}"
if command -v python3 >/dev/null 2>&1; then
  python3 - "$url" <<'PY'
import sys
from urllib.parse import urlsplit, urlunsplit
u = urlsplit(sys.argv[1])
host = u.hostname or ""
if u.port:
    host = f"{host}:{u.port}"
print(urlunsplit((u.scheme, host, u.path, "", "")))
PY
else
  # Best-effort fallback, same spirit as corpus_origin.sh's own: strip a userinfo@
  # prefix (only the LAST '@' before the first '/', so a path containing '@' with no
  # real userinfo is left alone) and any ?query/#fragment. r3 review: the fragment
  # strip was applied to `authority` only -- a query-or-fragment separator sitting
  # inside the PATH (the far more common shape: `/api#fragment-secret`) survived
  # untouched. Strip both from `path` too, same as `?query` already was.
  rest="${url#*://}"; scheme="${url%%://*}"
  authority="${rest%%/*}"
  path="${rest#"$authority"}"
  authority="${authority##*@}"
  authority="${authority%%\?*}"; authority="${authority%%#*}"
  path="${path%%\?*}"; path="${path%%#*}"
  printf '%s://%s%s\n' "$scheme" "$authority" "$path"
fi
