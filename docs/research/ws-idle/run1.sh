#!/bin/bash
# usage: run1.sh <server> <idle|bcast|bcast-ack>
DIR=$(cd $(dirname $0) && pwd)
B=/tmp/claude-1000/-home-nezhifi-Development-nilo/c3529e15-d781-409c-ad5c-befd03675bc7/scratchpad/ws-bin
name=$1; exp=$2; srv=${name%%:*}; extra=""
case $name in hand-b) srv=hand; extra="-variant b";; hand-b2) srv=hand; extra="-variant b2";; hand-park) srv=hand; extra="-variant park";; esac
[ "${exp:0:5}" = bcast ] && extra="$extra -push"
PORT=19001
export STATS=$DIR/out/stats-$name.json; rm -f $STATS
taskset -c 2,3,10,11 $B/$srv -addr 127.0.0.1:$PORT $extra >$DIR/out/$name.log 2>&1 &
pid=$!
taskset -c 4-7,12-15 $B/client -mode wait -addr 127.0.0.1:$PORT || { echo '{"error":"server did not start"}'; kill $pid; exit 1; }
sleep 0.5
case $exp in
 idle) taskset -c 4-7,12-15 $B/client -mode idle -addr 127.0.0.1:$PORT -pid $pid -stats $STATS ;;
 bcast) taskset -c 4-7,12-15 $B/client -mode bcast -addr 127.0.0.1:$PORT -pid $pid -stats $STATS ;;
 bcast-ack) taskset -c 4-7,12-15 $B/client -mode bcast -ack -addr 127.0.0.1:$PORT -pid $pid -stats $STATS ;;
esac
cp $DIR/out/$name.log $DIR/out/last-$name-$exp.log
kill $pid; wait $pid 2>/dev/null
