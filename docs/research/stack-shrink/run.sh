#!/bin/bash
# A hot wait under padding (internal/engine/conn.go, waitHot), before and after. The after is the
# working tree; the before is the same tree with internal/engine/conn.go taken from $BEFORE_REF
# (default HEAD), the only file the change touches in the engine. Both are built here, in one
# session with the same flags, then bench/run.sh runs GET, POST and the allocations, three rounds
# interleaved, each experiment its own server, with the server's stack bytes logged every 10 ms
# ($STACKS, bench/common):
#   before-none, before-mw1, before-mw3, after-none, after-mw1, after-mw3
# (mw1: one middleware that reads a header and adds one; mw3: three of it). Then the memory a
# idle connection holds (bench/run.sh idle), once a tree.
# Usage: docs/research/stack-shrink/run.sh   (raw runs under $RAW, default ~/.cache/bites-stack-shrink-raw)
set -u
HERE=$(cd "$(dirname "$0")" && pwd)
REPO=$(cd "$HERE/../../.." && pwd)
CACHE=$HOME/.cache/bites-stack-shrink
RAW=${RAW:-$HOME/.cache/bites-stack-shrink-raw}
REF=${BEFORE_REF:-HEAD}
mkdir -p "$CACHE/bin" "$RAW"
rm -rf "$CACHE/before" && mkdir -p "$CACHE/before"
(cd "$REPO" && git ls-files -co --exclude-standard | tar -cf - -T -) | tar -x -C "$CACHE/before"
git -C "$REPO" show "$REF:internal/engine/conn.go" > "$CACHE/before/internal/engine/conn.go"
(cd "$CACHE/before/bench" && go build -trimpath -ldflags "-s -w" -o "$CACHE/bin/bites-before" ./servers/bites) || exit 1
(cd "$REPO/bench" && go build -trimpath -ldflags "-s -w" -o "$CACHE/bin/bites" ./servers/bites \
  && go build -trimpath -ldflags "-s -w" -o "$CACHE/bin/client" ./client) || exit 1
one() { # one <variant> <round> <exps>
  local v=$1 r=$2 exps=$3 srv=bites mw=0
  [[ $v == before-* ]] && srv=bites-before
  case $v in *-mw1) mw=1 ;; *-mw3) mw=3 ;; esac
  echo "== round $r: $v $exps" >&2
  (cd "$REPO/bench" && BITES_MW=$mw STACKS="$RAW/$v-r$r-$exps.stacks" SERVERS=$srv EXPS="$exps" ROUNDS=1 \
    SKIP_BUILD=1 BENCH_CACHE="$CACHE" OUT="$RAW/$v-r$r" ./run.sh) >/dev/null 2>&1
}
for r in 1 2 3; do
  for v in before-none before-mw1 before-mw3 after-none after-mw1 after-mw3; do
    for e in get post allocs; do one $v $r $e; done
  done
done
for v in before-none after-none; do one $v idle idle; done
python3 -I "$HERE/summary.py" "$RAW" "$HERE/raw.jsonl" > "$HERE/summary.txt"
cat "$HERE/summary.txt"
