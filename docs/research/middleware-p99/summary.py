# Summarise run.sh: python3 -I summary.py <raw dir> [<jsonl file to write the raw runs to>]
import json, os, statistics as st, sys
raw = sys.argv[1]
out = open(sys.argv[2], "w") if len(sys.argv) > 2 else None
vs = ["before-none", "before-mw1", "after-none", "after-mw1", "after-bare1", "after-mw3"]
for exp in ["get", "post", "allocs"]:
    print(exp)
    for v in vs:
        srv = "bites-before" if v.startswith("before") else "bites"
        rows = []
        for r in (1, 2, 3):
            f = f"{raw}/{v}-r{r}/{exp}-{srv}-1.json"
            if os.path.exists(f):
                x = json.load(open(f))
                x["round"] = r
                rows.append(x)
                if out:
                    out.write(json.dumps({"variant": v, "exp": exp, **x}) + "\n")
        if not rows:
            continue
        if exp == "allocs":
            print(f"  {v:12} allocs/req {[round(x['allocs_per_req'], 3) for x in rows]}  bytes/req {[round(x['bytes_per_req']) for x in rows]}")
        else:
            k = [x["rps"] / 1000 for x in rows]
            print(f"  {v:12} krps {[round(x, 1) for x in k]} mean {st.mean(k):.1f}  cpu ns/req {[round(x['cpu_ns_per_req']) for x in rows]}  p50 us {[x['p50_us'] for x in rows]}  p99 us {[x['p99_us'] for x in rows]}  mean p99 {st.mean(x['p99_us'] for x in rows):.0f}  errors {sum(x['errors'] for x in rows)}")
