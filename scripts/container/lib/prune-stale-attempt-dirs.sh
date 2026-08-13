#!/usr/bin/env bash
# Shared, unit-testable pruning for scan.sh's failure-evidence attempt dirs.
#
# CHAOS-3772 R3: age-based, not process-ownership-based. A PID-marker
# design was tried first and cost three separate findings in one review
# round -- a creation-vs-marker race, EPERM misread as "dead", and
# unbounded retention if the OS reuses a recorded PID -- each fix adding
# complexity without removing the underlying problem. Age closes all
# three at once: a brand-new directory is always younger than the
# threshold (no race window to close at all), there is no process-liveness
# signal to misinterpret, and retention is bounded purely by the
# threshold, independent of PID reuse. A scan run takes minutes; the
# default six-hour threshold is generous headroom for a failed run's
# evidence to reach CI's artifact upload and post-mortem inspection while
# still bounding disk use on a long-lived machine.
#
# Usage: prune_stale_attempt_dirs <tmp_root> <glob prefix> <keep_dir> [max_age_seconds] [now_epoch_seconds]
# max_age_seconds defaults to 21600 (6h); now_epoch_seconds defaults to
# the real current time and exists so tests can pin it.
prune_stale_attempt_dirs() {
  local tmp_root="$1" prefix="$2" keep_dir="$3"
  local max_age_seconds="${4:-21600}"
  local now="${5:-$(date -u +%s)}"
  local candidate mtime age

  for candidate in "${tmp_root}/${prefix}"*; do
    [[ -d "$candidate" && "$candidate" != "$keep_dir" ]] || continue
    mtime="$(attempt_dir_mtime "$candidate")" || continue
    age=$((now - mtime))
    if [[ "$age" -gt "$max_age_seconds" ]]; then
      rm -rf "$candidate"
    fi
  done
  # Explicit, unconditional success: under `set -e` at the call site, a
  # bare function call aborts the whole caller if the function's own exit
  # status is nonzero. Without this, the function's status would be
  # whatever the LAST loop iteration's last command happened to return --
  # e.g. a false `[[ age -gt max_age ]]` for a young, correctly-kept
  # directory -- silently killing scan.sh on the common case where nothing
  # needed pruning.
  return 0
}

# stat's mtime flag differs between GNU (-c %Y) and BSD/macOS (-f %m).
attempt_dir_mtime() {
  stat -c %Y "$1" 2>/dev/null || stat -f %m "$1" 2>/dev/null
}
