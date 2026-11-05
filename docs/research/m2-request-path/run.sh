#!/bin/bash
# Milestone 2 steps 6 to 11 on the request path: what they cost, interleaved, three rounds, one
# bench/run.sh round at a time (SERVERS=nilogo EXPS="get post allocs"). Variants:
#   before  the tree at 6b086e4 (git archive into $BEFORE), the routes of milestone 1
#   after   this checkout, the same routes (what every route now pays: noescape as a call, the
#           Provider branch in serve, the extra arguments of Stream and Validate)
#   prov    this checkout, the Input asks for a Caller whose Provider needs a Session
#   valid   this checkout, constraint types in every field and a Validate that returns nil
#   mw4     this checkout, CORS, RequestID and AccessLog on the app and Timeout on each route
# bench/servers/nilogo/main.go reads NILOGO_VARIANT. Usage: docs/research/m2-request-path/run.sh
set -u
REPO=$(cd "$(dirname "$0")/../../.." && pwd)
BEFORE=${BEFORE:-$HOME/.cache/nilo-go-m2-before}
RAW=${RAW:-$HOME/.cache/nilo-go-m2-raw}
mkdir -p "$RAW"
for r in 1 2 3; do
  for v in before after prov valid mw4; do
    dir=$REPO; var=$v
    case $v in before) dir=$BEFORE; var= ;; after) var= ;; esac
    echo "== round $r: $v" >&2
    (cd "$dir/bench" && NILOGO_VARIANT=$var SERVERS=nilogo EXPS="get post allocs" ROUNDS=1 \
      BENCH_CACHE="$RAW/cache-$v" OUT="$RAW/$v-r$r" ./run.sh)
  done
done
