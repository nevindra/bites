# Summarise run.sh: python3 -I summary.py <raw dir> [<jsonl file to write the raw runs to>]
# For each variant: the three rounds of GET and POST (req/s, CPU, p50, p99), the allocations and
# bytes a request, the stack bytes a goroutine under load (the median of the middle 60% of each
# run's samples, from $STACKS) and the collector's cycles a second; then the idle runs.
import glob, json, os, statistics as st, sys
raw = sys.argv[1]
out = open(sys.argv[2], "w") if len(sys.argv) > 2 else None
vs = ["before-none", "before-mw1", "before-mw3", "after-none", "after-mw1", "after-mw3"]

def stacks(f):
    rows = [list(map(int, l.split())) for l in open(f) if len(l.split()) == 4]
    rows = [r for r in rows if r[2] > 200]
    if len(rows) < 10:
        return None
    n = len(rows); rows = rows[n // 5: n - n // 5]
    secs = (rows[-1][0] - rows[0][0]) / 1e9
    return round(st.median(r[1] / r[2] for r in rows)), round((rows[-1][3] - rows[0][3]) / secs)

def srv(v):
    return "bites-before" if v.startswith("before") else "bites"

for exp in ["get", "post", "allocs"]:
    print(exp)
    for v in vs:
        rows = []
        for r in (1, 2, 3):
            f = f"{raw}/{v}-r{r}/{exp}-{srv(v)}-1.json"
            if os.path.exists(f):
                x = json.load(open(f)); x["round"] = r; rows.append(x)
                if out:
                    out.write(json.dumps({"variant": v, "exp": exp, **x}) + "\n")
        if not rows:
            continue
        if exp == "allocs":
            print(f"  {v:12} allocs/req {[round(x['allocs_per_req'], 3) for x in rows]}  bytes/req {[round(x['bytes_per_req']) for x in rows]}")
        else:
            k = [x["rps"] / 1000 for x in rows]
            print(f"  {v:12} krps {[round(x, 1) for x in k]} median {st.median(k):.1f}  cpu ns/req {[round(x['cpu_ns_per_req']) for x in rows]}  p50 us {[x['p50_us'] for x in rows]}  p99 us {[x['p99_us'] for x in rows]}  median p99 {st.median(x['p99_us'] for x in rows):.0f}  errors {sum(x['errors'] for x in rows)}")
print("stack bytes a goroutine under load and gc cycles a second, a pair a round")
for exp in ["get", "post"]:
    print(" ", exp)
    for v in vs:
        s = [stacks(f"{raw}/{v}-r{r}-{exp}.stacks") for r in (1, 2, 3) if os.path.exists(f"{raw}/{v}-r{r}-{exp}.stacks")]
        print(f"    {v:12} {s}")
print("idle")
for v in ["before-none", "after-none"]:
    for f in sorted(glob.glob(f"{raw}/{v}-ridle/idle-*.json")):
        x = json.load(open(f))
        if out:
            out.write(json.dumps({"variant": v, "exp": "idle", **x}) + "\n")
        print(f"  {v:12} {json.dumps(x)}")
