#!/bin/bash
# The three probes that found the cause, against the tree before the fix (git archive $REF, default
# db35bea, the commit before it): GET, one pass-through route middleware against none.
#   gc:      the collector on (GOGC=100) and off (GOGC=off, GOMEMLIMIT=20GiB), three rounds interleaved
#   gctrace: GODEBUG=gctrace=1, two rounds, the server's log kept for the cycles a second
#   room:    the same tree with the 80 B room of serveChain removed, GET and POST, three rounds
#   shrink:  the fixed tree (the working tree), none and three middleware, with and without
#            GODEBUG=gcshrinkstackoff=1, two rounds; and a CPU profile of each without it
# Usage: docs/research/middleware-p99/probes.sh [gc|gctrace|room|shrink]...   (raw under ~/.cache/bites-mwp99-probes)
set -u
REPO=$(cd "$(dirname "$0")/../../.." && pwd)
CACHE=$HOME/.cache/bites-mwp99-probes
RAW=$CACHE/raw
REF=${REF:-db35bea}   # the last commit before the fix
mkdir -p "$CACHE/bin" "$RAW"
build() { # build <dir> <binary>
  (cd "$1/bench" && go build -trimpath -ldflags "-s -w" -o "$CACHE/bin/$2" ./servers/bites) || exit 1
}
rm -rf "$CACHE/src" "$CACHE/noroom" && mkdir -p "$CACHE/src" "$CACHE/noroom"
git -C "$REPO" archive "$REF" | tar -x -C "$CACHE/src"
git -C "$REPO" archive "$REF" | tar -x -C "$CACHE/noroom"
sed -i 's/^\t\tex.AddSlab(generatedIDLen + rateLimitRoom)$/\t\t_ = generatedIDLen + rateLimitRoom/' "$CACHE/noroom/middleware.go"
build "$CACHE/src" bites; build "$CACHE/noroom" bites-noroom
(cd "$CACHE/src/bench" && go build -trimpath -ldflags "-s -w" -o "$CACHE/bin/client" ./client) || exit 1
one() { # one <out> <server> <mw> <exps> [env...]
  local out=$1 srv=$2 mw=$3 exps=$4; shift 4
  (cd "$REPO/bench" && env BITES_MW=$mw BITES_MWKIND=bare "$@" SERVERS=$srv EXPS="$exps" ROUNDS=1 SKIP_BUILD=1 \
    BENCH_CACHE="$CACHE" OUT="$RAW/$out" ./run.sh) >/dev/null 2>&1
}
for p in "${@:-gc gctrace room}"; do for probe in $p; do case $probe in
  gc) for r in 1 2 3; do for v in none bare1; do mw=0; [[ $v == bare1 ]] && mw=1
        one gc/$v-on-r$r bites $mw get GOGC=100
        one gc/$v-off-r$r bites $mw get GOGC=off GOMEMLIMIT=20GiB
      done; done ;;
  gctrace) for r in 1 2; do for v in none bare1; do mw=0; [[ $v == bare1 ]] && mw=1
        one gctrace/$v-r$r bites $mw get GODEBUG=gctrace=1
      done; done ;;
  room) for r in 1 2 3; do
        one room/none-r$r bites 0 "get post"
        one room/bare1-r$r bites 1 "get post"
        one room/noroom-r$r bites-noroom 1 "get post"
      done ;;
  shrink)
      (cd "$REPO/bench" && go build -trimpath -ldflags "-s -w" -o "$CACHE/bin/bites-fixed" ./servers/bites) || exit 1
      for r in 1 2; do for v in none mw3; do mw=0; [[ $v == mw3 ]] && mw=3
        one shrink/$v-def-r$r bites-fixed $mw get BITES_MWKIND=
        one shrink/$v-noshrink-r$r bites-fixed $mw get BITES_MWKIND= GODEBUG=gcshrinkstackoff=1
      done; done
      for v in none mw3; do mw=0; [[ $v == mw3 ]] && mw=3
        one shrink/prof-$v bites-fixed $mw get BITES_MWKIND= CPUPROFILE="$RAW/shrink/$v.pprof"
      done ;;
esac; done; done
echo "raw runs in $RAW" >&2
