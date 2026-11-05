#!/bin/bash
# Syscalls per request by number, from a ptrace counter (strace is not installed): for each server
# and route, with 1 connection (one request in flight, nothing to batch) and 256 connections
# (batching grows because tracing slows the server, so read the 256 figures as upper bounds on
# the batching, lower bounds on epoll_wait and futex per request). Counts only, no timings.
# Output: out/syscalls-<route>-<server>-c<conns>.json = {"reqs":N,"counts":{...}}.
set -u
HERE=$(cd "$(dirname "$0")" && pwd)
CACHE=${CACHE:-$HOME/.cache/nilo-go-profile}; BIN=$CACHE/bin
ADDR=127.0.0.1:19001
POST_BODY='{"name":"ada","email":"ada@example.dev","age":36}'
for conns in ${CONNS_LIST:-1 256}; do for route in get post; do for s in nilogo fib; do
  if [[ $route == get ]]; then args=(-method GET -path /users/7); else args=(-method POST -path '/users/7?page=2&expand=true' -body "$POST_BODY"); fi
  taskset -c 2,3,10,11 "$BIN/syscount" -out "$CACHE/.counts.json" -- "$BIN/$s" -addr $ADDR >"$CACHE/.sc.log" 2>&1 &
  tp=$!
  taskset -c 4-7,12-15 "$BIN/client" -mode wait -addr $ADDR || { echo "no start $s"; kill $tp; continue; }
  sleep 1
  taskset -c 4-7,12-15 "$BIN/client" -mode load -addr $ADDR "${args[@]}" -c $conns -warm 0s -dur ${DUR:-6s} -pid $tp >"$CACHE/.c.json"
  kill -USR1 $tp; sleep 0.5
  python3 -I - "$CACHE/.c.json" "$CACHE/.counts.json" > "$HERE/out/syscalls-$route-$s-c$conns.json" <<'PY'
import json, sys
c = json.load(open(sys.argv[1])); n = json.load(open(sys.argv[2]))
print(json.dumps({"reqs": c["reqs"], "rps": c["rps"], "counts": n}))
PY
  kill -TERM $tp; wait $tp 2>/dev/null; sleep 1
done; done; done
