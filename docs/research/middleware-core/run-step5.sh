#!/bin/bash
# Step 5 of milestone 2: the cost of the middleware core, before and after, interleaved.
# before = the tree at a66baa8 (git archive into ~/.cache/nilo-go-step5-before), after = this
# checkout, after with NILOGO_MW=1 and =3 (route middleware on every route of the bench server).
# One bench/run.sh round a time, three times, the variants in turn; raw runs under $RAW.
# Usage (from anywhere): ulimit -n 524288 && docs/research/middleware-core/run.sh
set -u
REPO=$(cd "$(dirname "$0")/../../.." && pwd)
BEFORE=${BEFORE:-$HOME/.cache/nilo-go-step5-before}
RAW=${RAW:-$HOME/.cache/nilo-go-step5-raw}
mkdir -p "$RAW"
for r in 1 2 3; do
  for v in before after mw1 mw3; do
    dir=$REPO; mw=0
    case $v in before) dir=$BEFORE ;; mw1) mw=1 ;; mw3) mw=3 ;; esac
    echo "== round $r: $v" >&2
    (cd "$dir/bench" && NILOGO_MW=$mw SERVERS=nilogo EXPS="get post allocs" ROUNDS=1 \
      BENCH_CACHE="$RAW/cache-$v" OUT="$RAW/$v-r$r" ./run.sh)
  done
done
