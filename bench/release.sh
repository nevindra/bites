#!/bin/bash
# The release run of v0.1.0 on the bench machine, one command (bench/README.md, "The release run").
# Four phases, each in its own output directory under $OUT, each a fresh server a run, pinned as
# run.sh pins, rounds interleaved:
#
#   step17    b2b8576 against this checkout over TCP, the routes of milestone 1 and the variants
#             of docs/research/m2-request-path (prov, valid, mw4, log): what step 17's pass on the
#             Mac (docs/research/hotpath-mac) is worth over a real socket
#   profile   docs/research/profile-fib/run.sh on this checkout: the CPU profile of bites and fib
#             that step 17 repeats, its numbers in $OUT/profile, its profiles in its own cache
#   compare   run.sh with bites, nethttp, huma, nilo and fib, allocs get post idle: milestone 2's
#             "done when" 7 and the release's table
#   tls       run.sh with bites alone over TLS, get post allocs idle: TLS throughput and
#             allocations a request, which milestone 2 left unmeasured
#
# Usage: cd bench && ulimit -n 524288 && ./release.sh   (PHASES="step17 compare" to run some)
# About 90 minutes for the defaults. Nothing is written into the repository but $OUT, which is
# gitignored; the numbers worth keeping go to bench/result/ by hand, with the machine and commit.
set -u
HERE=$(cd "$(dirname "$0")" && pwd)
REPO=$(cd "$HERE/.." && pwd)
PHASES=${PHASES:-"step17 profile compare tls"}
BEFORE_COMMIT=${BEFORE_COMMIT:-b2b8576}
ROUNDS=${ROUNDS:-3}
OUT=${OUT:-$HERE/out/release}
CACHE=${BENCH_CACHE:-$HOME/.cache/bites-release}

if [[ $(uname -s) != Linux ]]; then
  echo "the release run needs Linux: taskset, /proc, and parking on epoll (ADR 0005)" >&2
  exit 1
fi
if [[ -n $(git -C "$REPO" status --porcelain --untracked-files=no) ]]; then
  echo "the tree has uncommitted changes: commit them, so the run names the commit it measured" >&2
  exit 1
fi
mkdir -p "$OUT" "$CACHE"
git -C "$REPO" rev-parse --short HEAD > "$OUT/commit.txt"
fail=0

for phase in $PHASES; do
  echo "==== $phase" >&2
  case $phase in
  step17)
    before=$CACHE/before-$BEFORE_COMMIT
    if [[ ! -d $before ]]; then
      mkdir -p "$before" && git -C "$REPO" archive "$BEFORE_COMMIT" | tar -x -C "$before" || exit 1
    fi
    for r in $(seq "$ROUNDS"); do
      for v in base prov valid mw4 log; do
        var=$v; [[ $v == base ]] && var=
        for side in before after; do
          dir=$REPO; [[ $side == before ]] && dir=$before
          echo "== step17 round $r: $v $side" >&2
          (cd "$dir/bench" && BITES_VARIANT=$var SERVERS=bites EXPS="get post allocs" ROUNDS=1 \
            BENCH_CACHE="$CACHE/cache-$side" OUT="$OUT/step17/$v-$side-r$r" ./run.sh) || fail=1
        done
      done
    done
    ;;
  profile)
    (cd "$REPO/docs/research/profile-fib" && OUT="$OUT/profile" CACHE="$CACHE/profile" ROUNDS=$ROUNDS ./run.sh \
      && OUT="$OUT/profile" CACHE="$CACHE/profile" ./pprof.sh \
      && OUT="$OUT/profile" CACHE="$CACHE/profile" python3 -I analyze.py --unmatched > "$OUT/profile/analysis.txt") || fail=1
    ;;
  compare)
    SERVERS="bites nethttp huma nilo fib" EXPS="allocs get post idle" ROUNDS=$ROUNDS \
      BENCH_CACHE="$CACHE/cache-after" OUT="$OUT/compare" "$HERE/run.sh" || fail=1
    python3 -I "$HERE/analyze.py" "$OUT/compare" > "$OUT/compare/analysis.txt" || fail=1
    ;;
  tls)
    TLS=1 SERVERS=bites EXPS="allocs get post idle" ROUNDS=$ROUNDS \
      BENCH_CACHE="$CACHE/cache-after" OUT="$OUT/tls" "$HERE/run.sh" || fail=1
    python3 -I "$HERE/analyze.py" "$OUT/tls" > "$OUT/tls/analysis.txt" || fail=1
    ;;
  *)
    echo "unknown phase $phase" >&2; fail=1 ;;
  esac
done
echo "done: $OUT (commit $(cat "$OUT/commit.txt"))" >&2
exit $fail
