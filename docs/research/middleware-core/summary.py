# Summarise the runs of run.sh: python3 -I summary.py <raw dir> [<jsonl file to write the raw runs to>]
import json, statistics as st, sys
raw = sys.argv[1]
out = open(sys.argv[2], "w") if len(sys.argv) > 2 else None
vs = sys.argv[3].split() if len(sys.argv) > 3 else ["before", "after", "mw1", "mw3"]
for exp in ["get", "post", "allocs"]:
    print(exp)
    for v in vs:
        # Rounds 1 to 3, and a round 4 for the variants where a run was disturbed (the machine
        # stalled: a quarter of the throughput and a p99 of 5 ms or more). A run under 80% of
        # the variant's median throughput is dropped, and the first three that remain are used.
        import os
        allr = []
        for r in (1, 2, 3, 4):
            f = f"{raw}/{v}-r{r}/{exp}-nilogo-1.json"
            if os.path.exists(f):
                x = json.load(open(f))
                x["round"] = r
                allr.append(x)
        if exp != "allocs":
            med = st.median(x["rps"] for x in allr)
            for x in allr:
                x["dropped"] = x["rps"] < 0.8 * med
        rows = [x for x in allr if not x.get("dropped")][:3]
        for x in allr:
            if out:
                out.write(json.dumps({"variant": v, "exp": exp, **x}) + "\n")
        if exp == "allocs":
            print(f"  {v:7} allocs/req {[round(x['allocs_per_req'], 3) for x in rows]}  bytes/req {[round(x['bytes_per_req']) for x in rows]}")
        else:
            rps = [x["rps"] / 1000 for x in rows]
            print(f"  {v:7} krps {[round(x, 1) for x in rps]} mean {st.mean(rps):.1f}  cpu ns/req {[round(x['cpu_ns_per_req']) for x in rows]}  p99 us {[x['p99_us'] for x in rows]}  errors {sum(x['errors'] for x in rows)}  dropped {[x['round'] for x in allr if x.get('dropped')]}")
