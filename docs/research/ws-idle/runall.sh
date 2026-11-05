#!/bin/bash
DIR=$(cd $(dirname $0) && pwd); cd $DIR; mkdir -p raw
SERVERS="hand-b hand-b2 gws coder gorilla fib nbio"
for r in 1 2 3; do for s in $SERVERS; do ./run1.sh $s idle > raw/idle-$s-$r.json 2>raw/idle-$s-$r.err; echo "$(date +%T) $s $r" >> raw/progress.txt; done; done
echo done > raw/DONE
