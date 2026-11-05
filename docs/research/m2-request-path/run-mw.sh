#!/bin/bash
# The four first-party middleware one at a time, after run.sh showed the four together costing
# about a quarter of the throughput: after (none) beside cors, rid, log and timeout, interleaved,
# three rounds, as run.sh. Usage: docs/research/m2-request-path/run-mw.sh
set -u
REPO=$(cd "$(dirname "$0")/../../.." && pwd)
RAW=${RAW:-$HOME/.cache/nilo-go-m2-raw}
mkdir -p "$RAW"
for r in 1 2 3; do
  for v in after2 cors rid log timeout; do
    var=$v; [[ $v == after2 ]] && var=
    echo "== round $r: $v" >&2
    (cd "$REPO/bench" && NILOGO_VARIANT=$var SERVERS=nilogo EXPS="get post allocs" ROUNDS=1 \
      BENCH_CACHE="$RAW/cache-$v" OUT="$RAW/$v-r$r" ./run.sh)
  done
done
