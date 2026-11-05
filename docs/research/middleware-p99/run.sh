#!/bin/bash
# The p99 of a route behind middleware, before and after the slab room is counted per chain.
# Builds the before from `git archive db35bea` (or $BEFORE_REF) into ~/.cache and the after from the
# working tree, then runs bench/run.sh for GET and POST, three rounds interleaved:
#   before-none, before-mw1, after-none, after-mw1 (one middleware that reads a header and adds
#   one), after-bare1 (one pass-through), after-mw3.
# Usage: docs/research/middleware-p99/run.sh   (raw runs under $RAW, default ~/.cache/bites-mwp99-raw)
set -u
REPO=$(cd "$(dirname "$0")/../../.." && pwd)
CACHE=$HOME/.cache/bites-mwp99
RAW=${RAW:-$HOME/.cache/bites-mwp99-raw}
REF=${BEFORE_REF:-db35bea}   # the last commit before the fix
mkdir -p "$CACHE/bin" "$RAW"
rm -rf "$CACHE/before" && mkdir -p "$CACHE/before" && git -C "$REPO" archive "$REF" | tar -x -C "$CACHE/before"
(cd "$CACHE/before/bench" && go build -trimpath -ldflags "-s -w" -o "$CACHE/bin/bites-before" ./servers/bites) || exit 1
(cd "$REPO/bench" && go build -trimpath -ldflags "-s -w" -o "$CACHE/bin/bites" ./servers/bites \
  && go build -trimpath -ldflags "-s -w" -o "$CACHE/bin/client" ./client) || exit 1
for r in 1 2 3; do
  for v in before-none before-mw1 after-none after-mw1 after-bare1 after-mw3; do
    srv=bites; [[ $v == before-* ]] && srv=bites-before
    mw=0 kind=
    case $v in *-mw1) mw=1 ;; *-bare1) mw=1 kind=bare ;; *-mw3) mw=3 ;; esac
    echo "== round $r: $v" >&2
    (cd "$REPO/bench" && BITES_MW=$mw BITES_MWKIND=$kind SERVERS=$srv EXPS="get post allocs" ROUNDS=1 \
      SKIP_BUILD=1 BENCH_CACHE="$CACHE" OUT="$RAW/$v-r$r" ./run.sh) >/dev/null 2>&1
  done
done
python3 -I "$(dirname "$0")/summary.py" "$RAW" "$(dirname "$0")/raw.jsonl" > "$(dirname "$0")/summary.txt"
cat "$(dirname "$0")/summary.txt"
