#!/bin/bash
# usage: run.sh <run number> [msg|nomsg]; binaries are built outside the repository
# (go build -o ~/.cache/nilo-go-ws-gate/server ./server, same for client) and raw output
# goes to ~/.cache/nilo-go-ws-gate/raw. Server on cores 2,3,10,11; client on 4-7,12-15.
B=$HOME/.cache/nilo-go-ws-gate
mkdir -p $B/raw
n=$1; kind=${2:-msg}; flag=true; [ "$kind" = nomsg ] && flag=false
export STATS=$B/stats-$kind-$n.json; rm -f $STATS $STATS.gc
ulimit -n 200000
taskset -c 2,3,10,11 $B/server -addr 127.0.0.1:19101 >$B/raw/server-$kind-$n.log 2>&1 &
pid=$!
taskset -c 4-7,12-15 $B/client -mode wait -addr 127.0.0.1:19101 || { echo '{"error":"server did not start"}'; kill $pid; exit 1; }
sleep 0.5
taskset -c 4-7,12-15 $B/client -mode idle -msg=$flag -addr 127.0.0.1:19101 -pid $pid -stats $STATS >$B/raw/$kind-$n.json 2>$B/raw/$kind-$n.err
kill $pid; wait $pid 2>/dev/null
