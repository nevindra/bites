#!/bin/bash
# ablation (park) idle x3, then broadcast: push-only x3, push+ack x3, hand-b
DIR=$(cd $(dirname $0) && pwd); cd $DIR
for r in 1 2 3; do ./run1.sh hand-park idle > raw/idle-hand-park-$r.json 2>raw/idle-hand-park-$r.err; done
for r in 1 2 3; do ./run1.sh hand-b bcast > raw/bcast-hand-b-$r.json 2>raw/bcast-hand-b-$r.err; cp out/last-hand-b-bcast.log raw/bcast-hand-b-$r.srvlog
 ./run1.sh hand-b bcast-ack > raw/bcastack-hand-b-$r.json 2>raw/bcastack-hand-b-$r.err; cp out/last-hand-b-bcast-ack.log raw/bcastack-hand-b-$r.srvlog; done
echo done > raw/DONE2
