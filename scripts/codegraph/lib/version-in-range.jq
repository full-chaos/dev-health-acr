# Strictly checks --arg version against the manifest's supported range.
# Both operands must be numeric X.Y.Z SemVer with no leading-zero components.
# Invoked as:
# jq -n --argjson m <manifest> --arg version <version> -f this
($m.supported_codegraph_version_range) as $range
| "(0|[1-9][0-9]*)\\.(0|[1-9][0-9]*)\\.(0|[1-9][0-9]*)" as $semver
| ($version | test("^\($semver)$")) as $version_valid
| ($range | test("^>=\($semver),<\($semver)$")) as $range_valid
| (
    if $range_valid then
      ($range | capture("^>=(?<low>\($semver)),<(?<high>\($semver))$"))
    else { low: null, high: null }
    end
  ) as $bounds
| if ($version_valid and $range_valid) then
    ($version | split(".") | map(tonumber)) as $v
    | ($bounds.low | split(".") | map(tonumber)) as $lo
    | ($bounds.high | split(".") | map(tonumber)) as $hi
    | { version: $version, range: $range, version_valid: true, range_valid: true, in_range: (($v >= $lo) and ($v < $hi)), ok: (($v >= $lo) and ($v < $hi)) }
  else
    { version: $version, range: $range, version_valid: $version_valid, range_valid: $range_valid, in_range: false, ok: false }
  end
