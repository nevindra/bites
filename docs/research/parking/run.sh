#!/bin/bash
# Parking idle connections (roadmap step 16, ADR 0005): parking on, parking off (WithoutParking,
# the same binary) and "base" (the tree before step 16, built from BASE_DIR with -tags base),
# interleaved round by round. Server on cores 2,3,10,11, client on 4-7,12-15 (lscpu -e: one
# hyperthread pair per core on the Ryzen 7 9700X), clients from 127.0.0.2 to 127.0.0.5.
#
# usage: BASE_DIR=<export of the tree before> docs/research/parking/run.sh <block>...
#   idle   heap plus stack and RSS a connection, marginal from 10k to 20k: plain, TLS, WebSocket
#   load   GET, POST, GET over TLS (bench/client, 256 connections) and WebSocket echo (256
#          sockets): throughput and p50/p99/p999; and allocations a POST
#   wake   2,000 connections each sending a request every 300 ms (past moveAfter, so every
#          request finds its connection idle): plain, TLS, WebSocket
# Raw output: $OUT/<exp>-<variant>-<round>.json, plus $OUT/load-<block>.txt (uptime before it).
# Binaries go to $BIN, outside the repository.
set -u
HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$HERE/../../.." && pwd)
BIN=${BIN:-$HOME/.cache/nilo-go-parking/bin}
OUT=${OUT:-$HERE/out}
ROUNDS=${ROUNDS:-3}
SRV_CPUS=${SRV_CPUS:-2,3,10,11}
CLI_CPUS=${CLI_CPUS:-4-7,12-15}
PORT=${PORT:-19201}
ADDR=127.0.0.1:$PORT
VARIANTS=${VARIANTS:-"on off base"}
mkdir -p "$BIN" "$OUT"
ulimit -n "$(ulimit -Hn)"

(cd "$HERE" && go build -trimpath -ldflags "-s -w" -o "$BIN/server" ./server && go build -trimpath -o "$BIN/client" ./client) || exit 1
(cd "$ROOT/bench" && go build -trimpath -o "$BIN/benchclient" ./client) || exit 1
if [[ " $VARIANTS " == *" base "* ]]; then
  [[ -n ${BASE_DIR:-} ]] || { echo "BASE_DIR is needed for the base variant" >&2; exit 1; }
  mkdir -p "$BASE_DIR/docs/research/parking"
  cp -r "$HERE/go.mod" "$HERE/server" "$BASE_DIR/docs/research/parking/" || exit 1
  (cd "$BASE_DIR/docs/research/parking" && go build -trimpath -tags base -ldflags "-s -w" -o "$BIN/server-base" ./server) || exit 1
fi
{
  echo "{\"date\":\"$(date -Is)\",\"commit\":\"$(git -C "$ROOT" rev-parse --short HEAD)\",\"dirty\":\"$(git -C "$ROOT" status --porcelain | wc -l) paths\","
  echo "\"go\":\"$(go version)\",\"kernel\":\"$(uname -r)\",\"cpu\":\"$(lscpu | sed -n 's/^Model name: *//p')\","
  echo "\"server_cpus\":\"$SRV_CPUS\",\"client_cpus\":\"$CLI_CPUS\",\"rounds\":$ROUNDS,\"variants\":\"$VARIANTS\"}"
} > "$OUT/versions-$(date +%s).json"

CLIENT="taskset -c $CLI_CPUS $BIN/client"
BCLIENT="taskset -c $CLI_CPUS $BIN/benchclient"
POST_BODY='{"name":"ada","email":"ada@example.dev","age":36}'

# one <exp> <variant> <round>
one() {
  local exp=$1 v=$2 r=$3 file="$OUT/$1-$2-$3.json" pid srv=$BIN/server park=on tls=()
  [[ $v == base ]] && srv=$BIN/server-base
  [[ $v == off ]] && park=off
  [[ $exp == *tls* ]] && tls=(-tls)
  export STATS="$OUT/.stats.json"; rm -f "$STATS"
  taskset -c "$SRV_CPUS" "$srv" -addr "$ADDR" -park "$park" "${tls[@]}" >"$OUT/.server.log" 2>&1 &
  pid=$!
  $BCLIENT -mode wait -addr "$ADDR" || { echo '{"error":"server did not start"}' > "$file"; kill $pid; return 1; }
  sleep 0.5
  echo "round $r: $exp $v" >&2
  echo "$exp $v $r $(date +%T) $(cut -d' ' -f1-3 /proc/loadavg)" >> "$OUT/loadavg.txt"
  local st=(-pid "$pid" -stats "$STATS")
  case $exp in
    idle-plain|idle-tls) $BCLIENT -mode idle -addr "$ADDR" -method GET -path /users/7 -n1 10000 -n2 20000 -settle 5s "${st[@]}" "${tls[@]}" > "$file" ;;
    idle-ws)  $CLIENT -mode wsidle -addr "$ADDR" -n1 10000 -n2 20000 -settle 5s "${st[@]}" > "$file" ;;
    get|get-tls) $BCLIENT -mode load -addr "$ADDR" -method GET -path /users/7 -c 256 -warm 3s -dur 10s "${st[@]}" "${tls[@]}" > "$file" ;;
    post)     $BCLIENT -mode load -addr "$ADDR" -method POST -path '/users/7?page=2&expand=true' -body "$POST_BODY" -c 256 -warm 3s -dur 10s "${st[@]}" > "$file" ;;
    allocs)   $BCLIENT -mode allocs -addr "$ADDR" -method POST -path '/users/7?page=2&expand=true' -body "$POST_BODY" -n 50000 "${st[@]}" > "$file" ;;
    wsload)   $CLIENT -mode wsload -addr "$ADDR" -c 256 -warm 3s -dur 10s "${st[@]}" > "$file" ;;
    wake-plain|wake-tls) $CLIENT -mode wake -addr "$ADDR" -c 2000 -gap 300ms -warm 3s -dur 15s "${st[@]}" "${tls[@]}" > "$file" ;;
    wake-ws)  $CLIENT -mode wake -ws -addr "$ADDR" -c 2000 -gap 300ms -warm 3s -dur 15s "${st[@]}" > "$file" ;;
  esac
  local rc=$?
  kill $pid 2>/dev/null; wait $pid 2>/dev/null
  [[ $rc -ne 0 ]] && echo "FAILED: $exp $v round $r" >&2
  sleep 2 # TIME_WAIT and the port
  return 0
}

for block in "$@"; do
  { date -Is; uptime; } > "$OUT/load-$block.txt"
  case $block in
    idle) EXPS="idle-plain idle-tls idle-ws" ;;
    load) EXPS="get post get-tls wsload allocs" ;;
    wake) EXPS="wake-plain wake-tls wake-ws" ;;
    *) echo "unknown block $block" >&2; exit 1 ;;
  esac
  for r in $(seq "$ROUNDS"); do
    for exp in $EXPS; do
      for v in $VARIANTS; do
        one "$exp" "$v" "$r"
      done
    done
  done
  { date -Is; uptime; } >> "$OUT/load-$block.txt"
done
rm -f "$OUT/.stats.json" "$OUT/.server.log"
