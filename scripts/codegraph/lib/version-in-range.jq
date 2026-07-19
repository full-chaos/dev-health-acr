# Checks --arg version against the manifest's supported_codegraph_version_range
# (format ">=X.Y.Z,<A.B.C"). Invoked as:
# jq -n --argjson m <manifest> --arg version <version> -f this
($m.supported_codegraph_version_range) as $range
| ($range | split(",")) as $parts
| ($parts[0] | ltrimstr(">=")) as $low
| ($parts[1] | ltrimstr("<")) as $high
| ($version | split(".") | map(tonumber)) as $v
| ($low | split(".") | map(tonumber)) as $lo
| ($high | split(".") | map(tonumber)) as $hi
| { version: $version, low: $lo, high: $hi, in_range: (($v >= $lo) and ($v < $hi)) }
