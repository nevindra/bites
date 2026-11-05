#!/bin/bash
# usage: run1.sh <server> <exp:idle|plain|block> <outfile-tag> [extra server args]
DIR=$(cd $(dirname $0) && pwd)
name=$1; exp=$2; shift 2
srv=${name%%:*}; extra=""
case $name in hand-a) srv=hand; extra="-variant a";; hand-b) srv=hand; extra="-variant b";; fib-async) srv=fib; extra="-async";; esac
PORT=19001
export STATS=$DIR/out/stats-$name.json
rm -f $STATS
taskset -c 2,3,10,11 $DIR/bin/$srv -addr 127.0.0.1:$PORT $extra >$DIR/out/$name.log 2>&1 &
pid=$!
taskset -c 4-7,12-15 $DIR/bin/client -mode wait -addr 127.0.0.1:$PORT || { echo '{"error":"server did not start"}'; kill $pid; exit 1; }
sleep 0.5
case $exp in
 idle)  taskset -c 4-7,12-15 $DIR/bin/client -mode idle -addr 127.0.0.1:$PORT -pid $pid -stats $STATS ;;
 plain) taskset -c 4-7,12-15 $DIR/bin/client -mode load -c 256 -path / -warm ${WARM:-3s} -dur ${DUR:-10s} -addr 127.0.0.1:$PORT -pid $pid -stats $STATS ;;
 block) taskset -c 4-7,12-15 $DIR/bin/client -mode load -c 1000 -path /block -warm 2s -dur ${DUR:-15s} -addr 127.0.0.1:$PORT -pid $pid -stats $STATS ;;
esac
kill $pid; wait $pid 2>/dev/null
