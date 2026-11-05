#!/bin/bash
# CPU profiles of the bench's nilo-go and fib servers under the bench's load (see report.md).
# Round r runs get then post on nilogo then fib; a fresh server a run, pinned as bench/run.sh does.
# Binaries and profiles live under $CACHE; the per-run numbers go to out/run-<route>-<server>-<round>.txt.
set -u
HERE=$(cd "$(dirname "$0")" && pwd)
CACHE=${CACHE:-$HOME/.cache/nilo-go-profile}
BIN=$CACHE/bin; PROF=$CACHE/prof
ROUNDS=${ROUNDS:-3}; SERVERS=${SERVERS:-"nilogo fib"}
SRV_CPUS=2,3,10,11; CLI_CPUS=4-7,12-15; PORT=${PORT:-19001}; ADDR=127.0.0.1:$PORT
POST_BODY='{"name":"ada","email":"ada@example.dev","age":36}'
OUT=${OUT:-$HERE/out}   # where the per-run numbers go; another directory keeps an earlier run's
mkdir -p "$BIN" "$PROF" "$OUT"

if [[ ${SKIP_BUILD:-0} != 1 ]]; then
  (cd "$HERE" && for s in nilogo fib; do go build -trimpath -o "$BIN/$s" ./servers/$s || exit 1; done) || exit 1
  (cd "$HERE/../../../bench" && go build -trimpath -ldflags "-s -w" -o "$BIN/client" ./client) || exit 1
fi

ticks() { f=($(sed 's/^.*) //' /proc/$1/stat)); echo "${f[11]} ${f[12]}"; }   # utime stime, 10 ms ticks
io()    { awk '/^syscr/{r=$2}/^syscw/{w=$2}END{print r, w}' /proc/$1/io; }
ctx()   { cat /proc/$1/task/*/status | awk '/^voluntary_ctxt/{v+=$2}/^nonvoluntary_ctxt/{n+=$2}END{print v, n}'; }
nthr()  { ls /proc/$1/task | wc -l; }

one() {
  local route=$1 s=$2 r=$3 out="$OUT/run-$1-$2-$3.txt" args
  if [[ $route == get ]]; then args=(-method GET -path /users/7); else args=(-method POST -path '/users/7?page=2&expand=true' -body "$POST_BODY"); fi
  PROF_FILE="$PROF/$route-$s-$r.pb.gz" PROF_SECS=10 PROF_HZ=${PROF_HZ:-500} STATS="$CACHE/.stats" \
    taskset -c $SRV_CPUS "$BIN/$s" -addr "$ADDR" >"$CACHE/.server-$s.log" 2>&1 &
  local pid=$!
  taskset -c $CLI_CPUS "$BIN/client" -mode wait -addr "$ADDR" || { echo "no start" >"$out"; kill $pid; return 1; }
  sleep 0.5
  taskset -c $CLI_CPUS "$BIN/client" -mode load -addr "$ADDR" "${args[@]}" -c 256 -warm 3s -dur 11s -pid $pid >"$CACHE/.client.json" &
  local cpid=$!
  sleep 3.2
  read -r u0 s0 < <(ticks $pid); read -r r0 w0 < <(io $pid); read -r v0 n0 < <(ctx $pid)
  kill -USR2 $pid
  sleep 10
  read -r u1 s1 < <(ticks $pid); read -r r1 w1 < <(io $pid); read -r v1 n1 < <(ctx $pid)
  local th; th=$(nthr $pid)
  wait $cpid
  sleep 0.5   # the profile ends 10 s after the signal
  kill $pid 2>/dev/null; wait $pid 2>/dev/null
  { echo "window_user_ticks $((u1-u0)) window_sys_ticks $((s1-s0))"
    echo "window_syscr $((r1-r0)) window_syscw $((w1-w0))"
    echo "window_vol_ctx $((v1-v0)) window_nonvol_ctx $((n1-n0)) threads $th"
    echo "client $(cat "$CACHE/.client.json")"; } >"$out"
  sleep 1
}

for r in $(seq "$ROUNDS"); do
  for route in get post; do
    for s in $SERVERS; do echo "round $r: $route $s" >&2; one $route $s $r; done
  done
done
