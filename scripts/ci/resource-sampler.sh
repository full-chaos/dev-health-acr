#!/usr/bin/env bash
# resource-sampler.sh <seconds> (CHAOS-14593): prints the runner's CPU model,
# MHz and nproc once, then every <seconds> one line with a UTC time, the CPU
# split since the last line (us = user+nice, sy = system+irq+softirq,
# wa = iowait, st = steal, in % of all CPU time), /proc/loadavg, `free -m` row
# 2 and one `docker stats` line. It writes straight to its own stdout (the
# step's live log), so the lines are there even when the runner dies later.
# Started in the background and stopped with SIGTERM; it exits 0 and never
# fails its caller. Fields it cannot read are left empty.
set +e
secs="${1:-15}"
case "$secs" in '' | *[!0-9]* | 0) secs=15 ;; esac

nap=""
trap 'if [ -n "$nap" ]; then kill "$nap" 2>/dev/null; fi; exit 0' TERM INT

printf 'resource-start nproc=%s\n' "$(nproc 2>/dev/null)"
grep -E '^(model name|cpu MHz)' /proc/cpuinfo 2>/dev/null | sed -E 's/[[:space:]]+:[[:space:]]+/: /' | sort | uniq -c |
  sed 's/^/resource-start /'

prev="cpu 0 0 0 0 0 0 0 0"
while :; do
  cur="$(sed -n 1p /proc/stat 2>/dev/null)"
  split="$(awk -v a="$prev" -v b="$cur" 'BEGIN {
    n = split(a, x, " "); m = split(b, y, " ")
    if (n < 9 || m < 9) exit
    t = 0
    for (i = 2; i <= 9; i++) { d[i] = y[i] - x[i]; t += d[i] }
    if (t <= 0) exit
    printf "us=%.0f sy=%.0f wa=%.0f st=%.0f", 100 * (d[2] + d[3]) / t, 100 * (d[4] + d[7] + d[8]) / t, 100 * d[6] / t, 100 * d[9] / t
  }')"
  [ -n "$cur" ] && prev="$cur"
  printf 'resource-sample %s cpu[%s] load[%s] mem[%s] docker[%s]\n' "$(date -u +%H:%M:%SZ)" \
    "$split" \
    "$(cut -d' ' -f1-3 /proc/loadavg 2>/dev/null)" \
    "$(free -m 2>/dev/null | sed -n 2p)" \
    "$(docker stats --no-stream --format '{{.Name}} {{.MemUsage}} {{.MemPerc}} {{.CPUPerc}}' 2>/dev/null | tr '\n' ';')"
  sleep "$secs" &
  nap=$!
  wait "$nap"
  nap=""
done
