#!/usr/bin/env bash
# Print the ORIGIN (scheme://host[:port]) of a URL.
#
# r2 #13: this was a sed expression `s#(https?://[^/]+).*#\1#` inlined in both launchers.
# It only strips at the first `/`, so a URL whose authority is followed by `?` or `#`
# came back whole: `http://host:3040?tenant=foo` -> `http://host:3040?tenant=foo`, and the
# probe then requested a nonsense address. URLs are not a regex problem; this defers to a
# real parser and falls back to bash parameter expansion only if python is unavailable.
set -euo pipefail
url="${1:?usage: corpus_origin.sh <url>}"
if command -v python3 >/dev/null 2>&1; then
  python3 - "$url" <<'PY'
import sys
from urllib.parse import urlsplit
u = urlsplit(sys.argv[1])
if not u.scheme or not u.netloc:
    sys.exit(f"corpus_origin: not an absolute URL: {sys.argv[1]}")
print(f"{u.scheme}://{u.netloc}")
PY
else
  rest="${url#*://}"; scheme="${url%%://*}"
  rest="${rest%%/*}"; rest="${rest%%\?*}"; rest="${rest%%#*}"
  printf '%s://%s\n' "$scheme" "$rest"
fi
