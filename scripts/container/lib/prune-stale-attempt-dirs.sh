#!/usr/bin/env bash
# Shared, unit-testable pruning for scan.sh's failure-evidence attempt dirs.
#
# CHAOS-3772 R2-1: multiple scan.sh invocations can share the same .tmp
# root (a developer running container-scan twice, or a Release build
# alongside a CI job on one machine). Pruning MUST NOT delete another
# invocation's still-running attempt directory. Ownership is a PID marker
# file (.owner.pid) written by the owning process; pruning removes only
# directories whose marker names a PID that is provably dead (or absent
# entirely). A live PID -- including one this process cannot signal, in
# which case kill -0 still reports success -- is always left alone.
#
# Usage: prune_stale_attempt_dirs <tmp_root> <glob prefix> <dir to keep>
prune_stale_attempt_dirs() {
  local tmp_root="$1" prefix="$2" keep_dir="$3"
  local candidate marker owner_pid

  for candidate in "${tmp_root}/${prefix}"*; do
    [[ -d "$candidate" && "$candidate" != "$keep_dir" ]] || continue
    marker="${candidate}/.owner.pid"
    if [[ -f "$marker" ]]; then
      owner_pid="$(cat "$marker" 2>/dev/null || true)"
      if [[ "$owner_pid" =~ ^[1-9][0-9]*$ ]] && kill -0 "$owner_pid" 2>/dev/null; then
        continue
      fi
    fi
    rm -rf "$candidate"
  done
}
