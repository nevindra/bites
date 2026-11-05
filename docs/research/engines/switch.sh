#!/bin/bash
cd $(dirname $0)
while [ ! -s raw/idle-hand-b-3.json ]; do sleep 1; done
pkill -f "[r]unall.sh"
sleep 1; pkill -f "[r]un1.sh"; pkill -f "[b]in/client"
for s in stdhttp fasthttp fib nbio hertz hand; do pkill -f "[e]ngines/bin/$s"; done
sleep 62   # let TIME_WAIT drain
rm -f raw/plain-* raw/block-*
ulimit -n 524288
exec ./runall2.sh
