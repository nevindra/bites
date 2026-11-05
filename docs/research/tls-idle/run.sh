#!/bin/bash
# Idle memory of a TLS connection against a plain one: the bench harness's idle experiment
# (bench/client -mode idle: 10k then 20k connections, each served one request, settle 5 s,
# server RSS and heap plus stack read from the server's own stats), run interleaved plain, TLS,
# plain, TLS ... for ROUNDS rounds. Method of CLAUDE.md "Measuring": server and client on separate
# cores. Output: out/idle-<plain|tls>-<round>.json. Binaries stay under ~/.cache/nilo-go-tls.
set -u
HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$HERE/../../.." && pwd)
ROUNDS=${ROUNDS:-3}
N1=${N1:-10000}
N2=${N2:-20000}
SETTLE=${SETTLE:-5s}
SRV_CPUS=${SRV_CPUS:-2,3,10,11}
CLI_CPUS=${CLI_CPUS:-4-7,12-15}
PORT=${PORT:-19011}
BIN=$HOME/.cache/nilo-go-tls/bin
OUT=${OUT:-$HERE/out}
ADDR=127.0.0.1:$PORT
mkdir -p "$BIN" "$OUT"
ulimit -n "$(ulimit -Hn)"
(cd "$ROOT/bench" && go build -trimpath -ldflags "-s -w" -o "$BIN/server" ./servers/nilogo && go build -trimpath -o "$BIN/client" ./client) || exit 1
# BASE_DIR: an export of the commit before the change (git archive HEAD | tar -x -C dir); its plain
# server is built from the same flags in the same session and run in the same rounds as "base".
MODES="plain tls"
if [[ -n ${BASE_DIR:-} ]]; then
  (cd "$BASE_DIR/bench" && go build -trimpath -ldflags "-s -w" -o "$BIN/server-base" ./servers/nilogo) || exit 1
  MODES="base plain tls"
fi
{
  echo "{\"date\":\"$(date -Is)\",\"commit\":\"$(git -C "$ROOT" rev-parse --short HEAD)\",\"dirty\":\"$(git -C "$ROOT" status --porcelain | wc -l) paths\","
  echo "\"go\":\"$(go version)\",\"kernel\":\"$(uname -r)\",\"cpu\":\"$(lscpu | sed -n 's/^Model name: *//p')\","
  echo "\"server_cpus\":\"$SRV_CPUS\",\"client_cpus\":\"$CLI_CPUS\",\"rounds\":$ROUNDS,\"n1\":$N1,\"n2\":$N2,\"settle\":\"$SETTLE\"}"
} > "$OUT/versions.json"
for r in $(seq "$ROUNDS"); do
  for mode in $MODES; do
    flag=(); cflag=()
    [[ $mode == tls ]] && { flag=(-tls); cflag=(-tls); }
    export STATS="$OUT/.stats.json"; rm -f "$STATS"
    srv=$BIN/server; [[ $mode == base ]] && srv=$BIN/server-base
    taskset -c "$SRV_CPUS" "$srv" -addr "$ADDR" "${flag[@]}" >"$OUT/.server.log" 2>&1 &
    pid=$!
    taskset -c "$CLI_CPUS" "$BIN/client" -mode wait -addr "$ADDR" || { echo "server did not start" >&2; kill $pid; continue; }
    sleep 0.5
    echo "round $r: $mode" >&2
    taskset -c "$CLI_CPUS" "$BIN/client" -mode idle -addr "$ADDR" -method GET -path /users/7 -n1 "$N1" -n2 "$N2" -settle "$SETTLE" -pid "$pid" -stats "$STATS" "${cflag[@]}" > "$OUT/idle-$mode-$r.json"
    kill $pid 2>/dev/null; wait $pid 2>/dev/null
    sleep 2
  done
done
rm -f "$OUT/.stats.json" "$OUT/.server.log"
