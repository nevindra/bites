#!/bin/bash
# push + client ack (first run of this was a bug: client acked the echoes too, infinite ping-pong; kept in raw_discarded/)
DIR=$(cd $(dirname $0) && pwd); cd $DIR
for r in 1 2 3; do ./run1.sh hand-b bcast-ack > raw/bcastack-hand-b-$r.json 2>raw/bcastack-hand-b-$r.err; cp out/last-hand-b-bcast-ack.log raw/bcastack-hand-b-$r.srvlog; done
echo done > raw/DONE3
