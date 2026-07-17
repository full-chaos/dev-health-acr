#!/usr/bin/env bash
set -euo pipefail

if [[ "$#" -ne 4 ]]; then
  printf 'usage: %s <expected-status> <expected-code> <actual-status> <body-file>\n' "$0" >&2
  exit 2
fi

expected_status="$1"
expected_code="$2"
actual_status="$3"
body_file="$4"

[[ "$actual_status" == "$expected_status" ]] || exit 1

jq -e --arg expected_code "$expected_code" --argjson expected_status "$expected_status" '
  type == "object"
  and (keys | sort) == ["error", "request_id", "schema_version"]
  and .schema_version == "error.v1"
  and (.request_id | type) == "string"
  and (.request_id | length) > 0
  and (.request_id | length) <= 256
  and (.error | type) == "object"
  and ((.error | keys | sort) == ["code", "details", "http_status", "message", "retryable"]
    or (.error | keys | sort) == ["code", "http_status", "message", "retryable"])
  and .error.code == $expected_code
  and (.error.http_status | type) == "number"
  and .error.http_status == (.error.http_status | floor)
  and .error.http_status == $expected_status
  and (.error.message | type) == "string"
  and (.error.message | length) > 0
  and (.error.message | length) <= 2000
  and (.error.retryable | type) == "boolean"
  and (((.error | has("details")) | not) or (.error.details | type) == "object")
' "$body_file" >/dev/null
