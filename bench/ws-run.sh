#!/bin/bash
# The WebSocket comparison: bites beside gws, fib, gorilla/websocket and coder/websocket, the method of
# run.sh (server and client pinned to separate cores, rounds interleaved, a fresh server process a
# run). Output: $OUT/<exp>-<server>-<round>.json, read by analyze.py. Experiments:
#   wsallocs  one socket, the server's mallocs per 32 B raw echo
#   wsraw32 wsraw4k    GET /ws, the message echoed as bytes, 32 B and 4 KiB
#   wsjson32 wsjson4k  GET /wsjson, decoded into a struct and the struct sent back
#   wsidle    marginal memory per socket from N1 to N2, each having echoed one message
set -u
HERE=$(cd "$(dirname "$0")" && pwd)
SERVERS=${SERVERS:-"bites gws fib gorilla coder"}
EXPS=${EXPS:-"wsallocs wsraw32 wsraw4k wsjson32 wsjson4k wsidle"}
ROUNDS=${ROUNDS:-3}
WARM=${WARM:-3s}
DUR=${DUR:-10s}
CONNS=${CONNS:-256}
N1=${N1:-10000}
N2=${N2:-20000}
SETTLE=${SETTLE:-5s}
ALLOCS_N=${ALLOCS_N:-50000}
SRV_CPUS=${SRV_CPUS:-2,3,10,11}
CLI_CPUS=${CLI_CPUS:-4-7,12-15}
PORT=${PORT:-19001}
CACHE=${BENCH_CACHE:-$HOME/.cache/bites-bench}
BIN=$CACHE/bin
OUT=${OUT:-$HERE/out/ws}
SKIP_BUILD=${SKIP_BUILD:-0}
ADDR=127.0.0.1:$PORT
mkdir -p "$OUT" "$BIN"
ulimit -n "$(ulimit -Hn)" 2>/dev/null
if [[ " $EXPS " == *" wsidle "* && $(ulimit -n) -lt $((N2 * 2 + 2000)) ]]; then
  echo "ulimit -n is $(ulimit -n), wsidle needs $((N2 * 2 + 2000))" >&2
  exit 1
fi
if [[ $SKIP_BUILD != 1 ]]; then
  cd "$HERE" || exit 1
  for s in $SERVERS; do
    go build -trimpath -ldflags "-s -w" -o "$BIN/ws-$s" ./servers/ws-$s || exit 1
  done
  go build -trimpath -ldflags "-s -w" -o "$BIN/wsclient" ./wsclient || exit 1
  go build -trimpath -ldflags "-s -w" -o "$BIN/client" ./client || exit 1
fi
{
  echo "{\"date\":\"$(date -Is)\",\"nilo_go_commit\":\"$(git -C "$HERE/.." rev-parse --short HEAD)\","
  echo "\"nilo_go_dirty\":\"$(git -C "$HERE/.." status --porcelain | wc -l) paths\",\"go\":\"$(go version)\","
  for m in lxzan/gws lesismal/fib gorilla/websocket coder/websocket goccy/go-json; do
    echo "\"$m\":\"$(cd "$HERE" && go list -m -f '{{.Version}}' github.com/$m)\","
  done
  echo "\"kernel\":\"$(uname -r)\",\"cpu\":\"$(lscpu | sed -n 's/^Model name: *//p')\",\"server_cpus\":\"$SRV_CPUS\",\"client_cpus\":\"$CLI_CPUS\","
  echo "\"rounds\":$ROUNDS,\"conns\":$CONNS,\"warm\":\"$WARM\",\"dur\":\"$DUR\",\"n1\":$N1,\"n2\":$N2,\"settle\":\"$SETTLE\"}"
} > "$OUT/versions.json"
python3 -I - "$OUT" "$BIN" "$SERVERS" <<'PY'
import json, os, sys
out, bindir, servers = sys.argv[1], sys.argv[2], sys.argv[3].split()
json.dump({s: os.path.getsize(f"{bindir}/ws-{s}") for s in servers}, open(f"{out}/sizes.json", "w"))
PY
WS="taskset -c $CLI_CPUS $BIN/wsclient"
one() {
  local exp=$1 s=$2 r=$3 file="$OUT/$1-$2-$3.json" pid
  export STATS="$OUT/.stats-$s.json"
  rm -f "$STATS"
  taskset -c "$SRV_CPUS" "$BIN/ws-$s" -addr "$ADDR" >"$OUT/.server-$s.log" 2>&1 &
  pid=$!
  if ! taskset -c "$CLI_CPUS" "$BIN/client" -mode wait -addr "$ADDR"; then
    echo "{\"error\":\"server did not start\"}" > "$file"; kill "$pid" 2>/dev/null; return 1
  fi
  sleep 0.5
  local c=(-addr "$ADDR" -pid "$pid" -stats "$STATS")
  case $exp in
    wsallocs) $WS -mode allocs "${c[@]}" -path /ws -size 32 -n "$ALLOCS_N" > "$file" ;;
    wsraw32)  $WS -mode load "${c[@]}" -path /ws -size 32 -c "$CONNS" -warm "$WARM" -dur "$DUR" > "$file" ;;
    wsraw4k)  $WS -mode load "${c[@]}" -path /ws -size 4096 -c "$CONNS" -warm "$WARM" -dur "$DUR" > "$file" ;;
    wsjson32) $WS -mode load "${c[@]}" -path /wsjson -size 32 -c "$CONNS" -warm "$WARM" -dur "$DUR" > "$file" ;;
    wsjson4k) $WS -mode load "${c[@]}" -path /wsjson -size 4096 -c "$CONNS" -warm "$WARM" -dur "$DUR" > "$file" ;;
    wsidle)   $WS -mode idle "${c[@]}" -path /ws -size 32 -n1 "$N1" -n2 "$N2" -settle "$SETTLE" > "$file" ;;
  esac
  local rc=$?
  kill "$pid" 2>/dev/null; wait "$pid" 2>/dev/null
  [[ $rc -ne 0 ]] && echo "FAILED: $exp $s round $r (exit $rc)" >&2
  sleep 1
  return $rc
}
fail=0
for r in $(seq "$ROUNDS"); do
  for exp in $EXPS; do
    for s in $SERVERS; do
      echo "round $r: $exp $s" >&2
      one "$exp" "$s" "$r" || fail=1
    done
  done
done
echo "done, raw runs in $OUT; python3 -I $HERE/analyze.py $OUT" >&2
exit $fail
