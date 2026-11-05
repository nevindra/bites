#!/bin/bash
# After run.sh: the other routes the roadmap named with a p99 past 10%, on the fixed tree, beside
# the plain route: Providers (prov), constraint types and Validate (valid), and RequestID alone
# (rid, the one middleware that still reserves room: 16 B). Three rounds interleaved, GET and POST.
# Usage: docs/research/middleware-p99/run-rest.sh   (needs the binaries run.sh built)
set -u
REPO=$(cd "$(dirname "$0")/../../.." && pwd)
CACHE=$HOME/.cache/bites-mwp99
RAW=${RAW:-$HOME/.cache/bites-mwp99-raw}
for r in 1 2 3; do
  for v in none prov valid rid; do
    var=$v; [[ $v == none ]] && var=
    echo "== round $r: $v" >&2
    (cd "$REPO/bench" && BITES_VARIANT=$var SERVERS=bites EXPS="get post allocs" ROUNDS=1 \
      SKIP_BUILD=1 BENCH_CACHE="$CACHE" OUT="$RAW/rest-$v-r$r" ./run.sh) >/dev/null 2>&1
  done
done
python3 -I - "$RAW" "$(dirname "$0")/raw-rest.jsonl" > "$(dirname "$0")/summary-rest.txt" <<'PY'
import json, os, statistics as st, sys
raw, out = sys.argv[1], open(sys.argv[2], "w")
for exp in ["get", "post", "allocs"]:
    print(exp)
    for v in ["none", "prov", "valid", "rid"]:
        rows = []
        for r in (1, 2, 3):
            f = f"{raw}/rest-{v}-r{r}/{exp}-bites-1.json"
            if os.path.exists(f):
                x = json.load(open(f)); x["round"] = r; rows.append(x)
                out.write(json.dumps({"variant": v, "exp": exp, **x}) + "\n")
        if exp == "allocs":
            print(f"  {v:6} allocs/req {[round(x['allocs_per_req'], 3) for x in rows]}  bytes/req {[round(x['bytes_per_req']) for x in rows]}")
        else:
            k = [x["rps"] / 1000 for x in rows]
            print(f"  {v:6} krps {[round(x, 1) for x in k]} mean {st.mean(k):.1f}  cpu ns/req {[round(x['cpu_ns_per_req']) for x in rows]}  p50 us {[x['p50_us'] for x in rows]}  p99 us {[x['p99_us'] for x in rows]}  mean p99 {st.mean(x['p99_us'] for x in rows):.0f}")
PY
cat "$(dirname "$0")/summary-rest.txt"
