#!/bin/bash
# The probes before the interleaved run (single runs, GET, 256 connections, bench/run.sh):
#   stacks:  the tree before (internal/engine/conn.go from $BEFORE_REF, default HEAD) with none and
#            three middleware, with and without GODEBUG=gcshrinkstackoff=1, and the tree after,
#            each logging its stack bytes ($STACKS); results in raw-probe.jsonl, probe-stacks.txt
#   depth:   a CPU profile of the before with none and three middleware, and the deepest sampled
#            call chains with each frame's size from the compiler (depth.py)
#   gctrace: three middleware, before, before without shrinking, after, GODEBUG=gctrace=1 (gctrace.txt)
# Usage: docs/research/stack-shrink/probe.sh [stacks|depth|gctrace]...   (raw under ~/.cache/bites-stack-shrink-probe)
set -u
HERE=$(cd "$(dirname "$0")" && pwd)
REPO=$(cd "$HERE/../../.." && pwd)
CACHE=$HOME/.cache/bites-stack-shrink-probe
RAW=$CACHE/raw
REF=${BEFORE_REF:-HEAD}
mkdir -p "$CACHE/bin" "$RAW"
rm -rf "$CACHE/before" && mkdir -p "$CACHE/before"
(cd "$REPO" && git ls-files -co --exclude-standard | tar -cf - -T -) | tar -x -C "$CACHE/before"
git -C "$REPO" show "$REF:internal/engine/conn.go" > "$CACHE/before/internal/engine/conn.go"
(cd "$CACHE/before/bench" && go build -trimpath -ldflags "-s -w" -o "$CACHE/bin/bites-before" ./servers/bites) || exit 1
(cd "$REPO/bench" && go build -trimpath -ldflags "-s -w" -o "$CACHE/bin/bites" ./servers/bites \
  && go build -trimpath -ldflags "-s -w" -o "$CACHE/bin/client" ./client) || exit 1
one() { # one <out> <server> <mw> [env...]
  local out=$1 srv=$2 mw=$3; shift 3
  (cd "$REPO/bench" && env BITES_MW=$mw "$@" SERVERS=$srv EXPS=get ROUNDS=1 SKIP_BUILD=1 \
    BENCH_CACHE="$CACHE" OUT="$RAW/$out" ./run.sh) >/dev/null 2>&1
}
for p in "${@:-stacks depth gctrace}"; do for probe in $p; do case $probe in
  stacks) for v in none mw3; do mw=0; [[ $v == mw3 ]] && mw=3
        one base-$v-def bites-before $mw STACKS="$RAW/base-$v-def.stacks"
        one base-$v-noshrink bites-before $mw STACKS="$RAW/base-$v-noshrink.stacks" GODEBUG=gcshrinkstackoff=1
        one pad-$v bites $mw STACKS="$RAW/pad-$v.stacks"
      done ;;
  depth) S=$CACHE/S && mkdir -p "$S"
      (cd "$CACHE/before/bench" && go build -gcflags='all=-S' -o "$S/bites-S" ./servers/bites 2>&1) | grep -E " STEXT" \
        | sed -E 's/^(\S+) STEXT.* locals=(0x[0-9a-f]+).*/\2 \1/' | awk '{printf "%d %s\n", strtonum($1), $2}' > "$S/frames.txt"
      for v in none mw3; do mw=0; [[ $v == mw3 ]] && mw=3
        one prof-$v bites-before $mw CPUPROFILE="$RAW/$v.pprof"
        go tool pprof -traces "$CACHE/bin/bites-before" "$RAW/$v.pprof" > "$S/$v.traces" 2>/dev/null
        echo "== $v"; python3 -I "$HERE/depth.py" "$S/frames.txt" "$S/$v.traces"
      done ;;
  gctrace) for t in before before-noshrink after; do srv=bites-before; g=gctrace=1
        [[ $t == after ]] && srv=bites; [[ $t == before-noshrink ]] && g=gctrace=1,gcshrinkstackoff=1
        one gc-$t $srv 3 GODEBUG=$g
        echo "== mw3, $t: heap goal and scanned stacks of the cycles after the 300th"
        grep "^gc " "$RAW/gc-$t/.server-$srv.log" | awk 'NR>300' | grep -o "[0-9]* MB goal, [0-9]* MB stacks" | sort | uniq -c
      done ;;
esac; done; done
for f in "$RAW"/*.stacks; do [[ -e $f ]] && echo "$(basename "$f" .stacks): $(python3 -I - "$f" <<'PY'
import sys, statistics as st
rows = [list(map(int, l.split())) for l in open(sys.argv[1]) if len(l.split()) == 4]
rows = [r for r in rows if r[2] > 200]; n = len(rows); rows = rows[n // 5: n - n // 5]
per = [r[1] / r[2] for r in rows]; secs = (rows[-1][0] - rows[0][0]) / 1e9
print(f"stack/goroutine median {st.median(per):.0f} B [{min(per):.0f}-{max(per):.0f}], gc {(rows[-1][3] - rows[0][3]) / secs:.0f}/s")
PY
)"; done
