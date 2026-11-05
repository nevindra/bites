#!/bin/bash
# The -top and -top -cum text of every profile, kept in out/pprof/ (the binary profiles stay under the cache).
set -u
HERE=$(cd "$(dirname "$0")" && pwd); CACHE=${CACHE:-$HOME/.cache/nilo-go-profile}
OUT=${OUT:-$HERE/out}
mkdir -p "$OUT/pprof"
for p in "$CACHE"/prof/*.pb.gz; do
  n=$(basename "$p" .pb.gz); s=$(echo "$n" | cut -d- -f2)
  go tool pprof -top -nodecount=60 "$CACHE/bin/$s" "$p" > "$OUT/pprof/$n.top.txt" 2>&1
  go tool pprof -top -cum -nodecount=60 "$CACHE/bin/$s" "$p" > "$OUT/pprof/$n.cum.txt" 2>&1
done
