#!/bin/bash
# The milestone 1 benchmark: bites beside net/http, Huma and nilo, one machine, one client.
# Entry point; every knob is an environment variable (see bench/README.md). Output: one JSON
# object a run in $OUT/<exp>-<server>-<round>.json, $OUT/sizes.json and $OUT/versions.json.
# Rounds are interleaved: round 1 runs every experiment on every server before round 2 starts.
set -u
HERE=$(cd "$(dirname "$0")" && pwd)

SERVERS=${SERVERS:-"bites nethttp huma nilo"}   # also known: fib (an engine, see README), gin fiber echo chi hertz (the framework comparison)
EXPS=${EXPS:-"allocs get post idle"}   # allocs: mallocs/request; get, post: load; idle: memory/connection;
                                       # block (not a default): 1,000 connections on GET /block, a 10 ms sleeping handler
ROUNDS=${ROUNDS:-3}
WARM=${WARM:-3s}
DUR=${DUR:-10s}
CONNS=${CONNS:-256}                    # load connections
BLOCK_CONNS=${BLOCK_CONNS:-1000}       # block: connections (as docs/research/engines/run1.sh)
BLOCK_WARM=${BLOCK_WARM:-2s}
BLOCK_DUR=${BLOCK_DUR:-15s}
N1=${N1:-10000}                        # idle: first count
N2=${N2:-20000}                        # idle: second count; the marginal cost is (n1 to n2)
SETTLE=${SETTLE:-5s}                   # idle: wait before a snapshot; must exceed 100 ms (ADR 0005)
ALLOCS_N=${ALLOCS_N:-50000}
SRV_CPUS=${SRV_CPUS:-2,3,10,11}        # lscpu -e: one hyperthread pair per core on the 9700X
CLI_CPUS=${CLI_CPUS:-4-7,12-15}
PORT=${PORT:-19001}
CACHE=${BENCH_CACHE:-$HOME/.cache/bites-bench}
BIN=$CACHE/bin
OUT=${OUT:-$HERE/out}
NILO_DIR=${NILO_DIR:-$HOME/Development/nilo}
SKIP_BUILD=${SKIP_BUILD:-0}
TLS=${TLS:-0}                          # 1: the server serves and the client speaks TLS (bites only: its -tls flag)
POST_BODY='{"name":"ada","email":"ada@example.dev","age":36}'
POST_PATH='/users/7?page=2&expand=true'
GET_PATH='/users/7'
BLOCK_PATH='/block'
ADDR=127.0.0.1:$PORT

mkdir -p "$OUT" "$BIN"

need_fds=$((N2 * 2 + 2000))
ulimit -n "$(ulimit -Hn)" 2>/dev/null
if [[ " $EXPS " == *" idle "* && $(ulimit -n) -lt $need_fds ]]; then
  echo "ulimit -n is $(ulimit -n), idle needs $need_fds: raise the hard limit (limits.conf or systemd LimitNOFILE)" >&2
  exit 1
fi

if [[ $SKIP_BUILD != 1 ]]; then
  cd "$HERE" || exit 1
  for s in bites nethttp huma fib gin fiber echo chi hertz; do
    [[ " $SERVERS " == *" $s "* ]] || continue
    tags=()
    [[ $s == gin ]] && tags=(-tags go_json)   # Gin's documented fast JSON: goccy/go-json
    go build "${tags[@]}" -trimpath -ldflags "-s -w" -o "$BIN/$s" ./servers/$s || exit 1
  done
  go build -trimpath -ldflags "-s -w" -o "$BIN/client" ./client || exit 1
  if [[ " $SERVERS " == *" nilo "* ]]; then
    # zig always fetches packages into ./zig-pkg beside the build root, so build from a copy of
    # the three sources under $CACHE, the path dependency pointing at a symlink to the checkout
    # (zig wants a path relative to the build root): nothing is written inside the repository.
    NSRC=$CACHE/zig-src
    mkdir -p "$NSRC"
    cp servers/nilo/main.zig servers/nilo/build.zig "$NSRC/" || exit 1
    ln -sfn "$NILO_DIR" "$NSRC/nilo-checkout" || exit 1
    sed 's|"../../../../nilo"|"nilo-checkout"|' servers/nilo/build.zig.zon > "$NSRC/build.zig.zon" || exit 1
    (cd "$NSRC" && ZIG_GLOBAL_CACHE_DIR="$CACHE/zig-global" zig build --release=fast \
      --cache-dir "$CACHE/zig-cache" -p "$CACHE/zig-out") || exit 1
    cp "$CACHE/zig-out/bin/nilo" "$BIN/nilo" || exit 1
  fi
fi

# Binary sizes (stripped) and the versions behind a run.
python3 -I - "$OUT" "$BIN" "$SERVERS" <<'PY'
import json, os, sys
out, bindir, servers = sys.argv[1], sys.argv[2], sys.argv[3].split()
json.dump({s: os.path.getsize(f"{bindir}/{s}") for s in servers}, open(f"{out}/sizes.json", "w"))
PY
{
  echo "{"
  echo "\"date\":\"$(date -Is)\","
  echo "\"nilo_go_commit\":\"$(git -C "$HERE/.." rev-parse --short HEAD 2>/dev/null)\","
  echo "\"nilo_go_dirty\":\"$(git -C "$HERE/.." status --porcelain 2>/dev/null | wc -l) paths\","
  echo "\"nilo_commit\":\"$(git -C "$NILO_DIR" describe --tags --always 2>/dev/null)\","
  echo "\"nilo_dirty\":\"$(git -C "$NILO_DIR" status --porcelain 2>/dev/null | wc -l) paths\","
  echo "\"go\":\"$(go version)\","
  echo "\"zig\":\"$(zig version 2>/dev/null)\","
  echo "\"huma\":\"$(cd "$HERE" && go list -m -f '{{.Version}}' github.com/danielgtaylor/huma/v2)\","
  echo "\"fib\":\"$(cd "$HERE" && go list -m -f '{{.Version}}' github.com/lesismal/fib)\","
  for m in gin-gonic/gin gofiber/fiber/v3 labstack/echo/v5 go-chi/chi/v5 cloudwego/hertz goccy/go-json; do
    echo "\"$m\":\"$(cd "$HERE" && go list -m -f '{{.Version}}' github.com/$m)\","
  done
  echo "\"kernel\":\"$(uname -r)\","
  echo "\"cpu\":\"$(lscpu | sed -n 's/^Model name: *//p')\","
  echo "\"server_cpus\":\"$SRV_CPUS\",\"client_cpus\":\"$CLI_CPUS\","
  echo "\"tls\":$TLS,"
  echo "\"rounds\":$ROUNDS,\"conns\":$CONNS,\"warm\":\"$WARM\",\"dur\":\"$DUR\",\"n1\":$N1,\"n2\":$N2,\"settle\":\"$SETTLE\""
  echo "}"
} > "$OUT/versions.json"

CLIENT="taskset -c $CLI_CPUS $BIN/client"
[[ $TLS == 1 ]] && CLIENT="$CLIENT -tls"

# one <exp> <server> <round>: start the server pinned, run the client pinned, stop the server.
one() {
  local exp=$1 s=$2 r=$3 file="$OUT/$1-$2-$3.json" pid
  export STATS="$OUT/.stats-$s.json"
  rm -f "$STATS"
  local tls=()
  [[ $TLS == 1 ]] && tls=(-tls)
  taskset -c "$SRV_CPUS" "$BIN/$s" -addr "$ADDR" "${tls[@]}" >"$OUT/.server-$s.log" 2>&1 &
  pid=$!
  if ! $CLIENT -mode wait -addr "$ADDR"; then
    echo "{\"error\":\"server did not start\"}" > "$file"; kill "$pid" 2>/dev/null; return 1
  fi
  sleep 0.5
  local st=(-stats "$STATS")
  [[ $s == nilo ]] && st=()   # the Zig server has no stats hook
  case $exp in
    allocs) $CLIENT -mode allocs -addr "$ADDR" -method POST -path "$POST_PATH" -body "$POST_BODY" -n "$ALLOCS_N" -pid "$pid" "${st[@]}" > "$file" ;;
    get)    $CLIENT -mode load -addr "$ADDR" -method GET -path "$GET_PATH" -c "$CONNS" -warm "$WARM" -dur "$DUR" -pid "$pid" "${st[@]}" > "$file" ;;
    post)   $CLIENT -mode load -addr "$ADDR" -method POST -path "$POST_PATH" -body "$POST_BODY" -c "$CONNS" -warm "$WARM" -dur "$DUR" -pid "$pid" "${st[@]}" > "$file" ;;
    block)  $CLIENT -mode load -addr "$ADDR" -method GET -path "$BLOCK_PATH" -c "$BLOCK_CONNS" -warm "$BLOCK_WARM" -dur "$BLOCK_DUR" -pid "$pid" "${st[@]}" > "$file" ;;
    idle)   $CLIENT -mode idle -addr "$ADDR" -method GET -path "$GET_PATH" -n1 "$N1" -n2 "$N2" -settle "$SETTLE" -pid "$pid" "${st[@]}" > "$file" ;;
  esac
  local rc=$?
  kill "$pid" 2>/dev/null; wait "$pid" 2>/dev/null
  [[ $rc -ne 0 ]] && echo "FAILED: $exp $s round $r (exit $rc)" >&2
  sleep 1   # let TIME_WAIT sockets and the port settle between runs
  return $rc
}

fail=0
for r in $(seq "$ROUNDS"); do
  for exp in $EXPS; do
    for s in $SERVERS; do
      [[ $exp == allocs && $s == nilo ]] && continue   # Zig allocator: not comparable, see README
      echo "round $r: $exp $s" >&2
      one "$exp" "$s" "$r" || fail=1
    done
  done
done
echo "done, raw runs in $OUT; python3 $HERE/analyze.py $OUT" >&2
exit $fail
