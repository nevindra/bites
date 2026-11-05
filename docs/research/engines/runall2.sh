#!/bin/bash
# Reduced second half (time budget): 2 interleaved runs of plain and block.
DIR=$(cd $(dirname $0) && pwd); cd $DIR; mkdir -p raw
SERVERS="stdhttp fasthttp fib nbio hertz hand-a hand-b"
for r in 1 2; do for s in $SERVERS; do ./run1.sh $s plain > raw/plain-$s-$r.json 2>raw/plain-$s-$r.err; done; done
for r in 1 2; do for s in $SERVERS fib-async; do ./run1.sh $s block > raw/block-$s-$r.json 2>raw/block-$s-$r.err; done; done
echo done > raw/DONE
