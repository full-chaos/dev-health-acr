#!/usr/bin/env bash
# Unit test for wait_for_healthz (scripts/container/lib/healthz-probe.sh).
#
# CHAOS-12966: a fresh container's published port accepts the connection and
# resets it before the app listens. The listener below does exactly that for its
# first N connections (accept, then close with unread data so the peer sees a
# reset), then serves /healthz. Red control: the probe verify.sh used (curl
# --retry 20 --retry-connrefused) fails on the same listener.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=scripts/container/lib/healthz-probe.sh
source "${repo_root}/scripts/container/lib/healthz-probe.sh"
command -v python3 >/dev/null || { printf 'python3 is required\n' >&2; exit 1; }

fixture="$(mktemp -d)"
listener_pid=""
cleanup() {
  [[ -n "$listener_pid" ]] && kill "$listener_pid" 2>/dev/null || true
  rm -rf "$fixture"
}
trap cleanup EXIT

pass=0
fail=0
ok() { printf 'ok: %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL: %s\n' "$1" >&2; fail=$((fail + 1)); }

# start_listener RESETS BODY: resets the first RESETS connections, then answers BODY (@file reads it from a file).
start_listener() {
  local resets="$1" body="$2"
  rm -f "$fixture/port"
  python3 - "$resets" "$body" "$fixture/port" <<'PY' &
import socket, struct, sys
resets, body, portfile = int(sys.argv[1]), sys.argv[2], sys.argv[3]
if body.startswith("@"):
    body = open(body[1:]).read()
srv = socket.socket(); srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
srv.bind(("127.0.0.1", 0)); srv.listen(16)
open(portfile, "w").write(str(srv.getsockname()[1]))
seen = 0
while True:
    conn, _ = srv.accept()
    seen += 1
    if seen <= resets:
        conn.setsockopt(socket.SOL_SOCKET, socket.SO_LINGER, struct.pack("ii", 1, 0))
        conn.close()
        continue
    try:
        conn.recv(4096)
        payload = body.encode()
        conn.sendall(b"HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s" % (len(payload), payload))
    except OSError:
        pass
    conn.close()
PY
  listener_pid=$!
  for _ in $(seq 1 50); do [[ -s "$fixture/port" ]] && break; sleep 0.1; done
  port="$(cat "$fixture/port")"
}
stop_listener() { kill "$listener_pid" 2>/dev/null || true; wait "$listener_pid" 2>/dev/null || true; listener_pid=""; }

# 1. Red control: the old probe fails on a reset.
start_listener 3 '{"status":"ok"}'
if curl --fail --silent --show-error --retry 20 --retry-connrefused "http://127.0.0.1:${port}/healthz" >/dev/null 2>&1; then
  bad "the old curl --retry probe passed against a listener that resets first (the control is not red)"
else
  ok "the old curl --retry probe fails on a reset (red control)"
fi
stop_listener

# 2. The new probe waits through resets.
start_listener 3 '{"status":"ok"}'
if wait_for_healthz "http://127.0.0.1:${port}/healthz" 30 0.2 2>"$fixture/err"; then
  ok "wait_for_healthz passes after 3 connection resets"
else
  bad "wait_for_healthz failed on a listener that resets 3 times: $(cat "$fixture/err")"
fi
stop_listener

# 3. It does not accept an answer that is not ok.
start_listener 0 '{"status":"starting"}'
if wait_for_healthz "http://127.0.0.1:${port}/healthz" 2 0.2 2>"$fixture/err"; then
  bad "wait_for_healthz accepted status starting"
elif grep -q 'answered without status ok' "$fixture/err" && grep -q 'not ready after 2s' "$fixture/err"; then
  ok "wait_for_healthz refuses a non-ok body at the deadline and reports it"
else
  bad "wait_for_healthz failed without naming the last result: $(cat "$fixture/err")"
fi
stop_listener

# 3b. One valid JSON value only: a multi-value body whose last value is ok is not ready.
start_listener 0 '{"status":"starting"}
{"status":"ok"}'
if wait_for_healthz "http://127.0.0.1:${port}/healthz" 2 0.2 2>"$fixture/err"; then
  bad "wait_for_healthz accepted a body with two JSON values, the last one ok"
else
  ok "wait_for_healthz refuses a body with more than one JSON value"
fi
stop_listener

# 3c. A huge reply is neither held whole nor printed whole.
head -c 1048576 /dev/zero | tr '\000' x >"$fixture/huge"
start_listener 0 "@$fixture/huge"
if wait_for_healthz "http://127.0.0.1:${port}/healthz" 2 0.2 2>"$fixture/err"; then
  bad "wait_for_healthz accepted a 1 MiB junk body"
elif [[ "$(wc -c <"$fixture/err")" -lt 2048 ]]; then
  ok "wait_for_healthz reports a bounded diagnostic for a 1 MiB reply"
else
  bad "wait_for_healthz printed $(wc -c <"$fixture/err") bytes for a 1 MiB reply"
fi
stop_listener

# 3d. The read is capped at 64 KiB: a reply that is valid JSON with status ok but
# only past the cap is not a health answer. Without the cap the validator would
# accept it (the P3 of CHAOS-12966's third review round).
{ printf '{"status":"ok","pad":"'; head -c 100000 /dev/zero | tr '\000' x; printf '"}'; } >"$fixture/oversize-ok"
start_listener 0 "@$fixture/oversize-ok"
if wait_for_healthz "http://127.0.0.1:${port}/healthz" 2 0.2 2>"$fixture/err"; then
  bad "wait_for_healthz accepted an ok reply larger than its 64 KiB read cap"
else
  ok "wait_for_healthz does not accept an ok reply larger than its read cap"
fi
stop_listener

# 4. A listener that resets forever fails at the deadline, not never.
start_listener 100000 '{"status":"ok"}'
started="$(date +%s)"
if wait_for_healthz "http://127.0.0.1:${port}/healthz" 2 0.2 2>"$fixture/err"; then
  bad "wait_for_healthz passed against a listener that always resets"
elif (( $(date +%s) - started <= 10 )) && grep -q 'not ready after 2s' "$fixture/err"; then
  ok "wait_for_healthz gives up at its deadline on a listener that always resets"
else
  bad "wait_for_healthz did not give up in time: $(cat "$fixture/err")"
fi
stop_listener

printf '%d passed, %d failed\n' "$pass" "$fail"
[[ "$fail" -eq 0 ]]
