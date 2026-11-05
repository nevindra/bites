#!/bin/bash
# Milestone 2 step 5, the cost of the middleware core, interleaved, three rounds, one
# bench/run.sh round at a time (SERVERS=nilogo EXPS="get post allocs"). Variants:
#   base     the tree at a66baa8 (git archive into $BASE), no middleware
#   s5mw3    the first version of step 5 ($S5, a copy of the tree), 3 route middleware
#   after    this checkout, no middleware
#   mw1 mw3  this checkout, NILOGO_MW=1 or 3 (each reads a header and adds one)
#   bare3    this checkout, 3 pass-through middleware (NILOGO_MWKIND=bare): the chain alone
# The first run of this directory (run-step5.sh) compared base, after, mw1 and mw3 of the first
# version. Usage: ulimit -n 524288 && docs/research/middleware-core/run.sh
set -u
REPO=$(cd "$(dirname "$0")/../../.." && pwd)
BASE=${BASE:-$HOME/.cache/nilo-go-step5-before}
S5=${S5:-$HOME/.cache/nilo-go-step5b-before}
RAW=${RAW:-$HOME/.cache/nilo-go-step5b-raw}
mkdir -p "$RAW"
for r in 1 2 3; do
  for v in base s5mw3 after mw1 mw3 bare3; do
    dir=$REPO; mw=0; kind=work
    case $v in base) dir=$BASE ;; s5mw3) dir=$S5; mw=3 ;; mw1) mw=1 ;; mw3) mw=3 ;; bare3) mw=3; kind=bare ;; esac
    echo "== round $r: $v" >&2
    (cd "$dir/bench" && NILOGO_MW=$mw NILOGO_MWKIND=$kind SERVERS=nilogo EXPS="get post allocs" ROUNDS=1 \
      BENCH_CACHE="$RAW/cache-$v" OUT="$RAW/$v-r$r" ./run.sh)
  done
done
