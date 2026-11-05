#!/bin/bash
# usage: run.sh <bindir> <rounds>: interleaves before and after on both routes, server on cores
# 2,3,10,11 and client on 4-7,12-15; one JSON line a run.
BIN=$1; ROUNDS=${2:-3}
for r in $(seq $ROUNDS); do
  for route in /ping/7 /deep/7; do
    for v in before after; do
      taskset -c 2,3,10,11 $BIN/$v >/dev/null 2>&1 &
      pid=$!
      sleep 0.5
      echo -n "{\"round\":$r,\"build\":\"$v\",\"route\":\"$route\",\"run\":"
      taskset -c 4-7,12-15 $BIN/$v -mode load -path $route -pid $pid
      echo "}"
      kill $pid; wait $pid 2>/dev/null
    done
  done
done
