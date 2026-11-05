# usage: python3 -I analyze.py [out dir]; reads <exp>-<variant>-<round>.json written by run.sh and
# prints one table per block: the mean of the rounds with the min to max in brackets.
import glob, json, os, statistics as st, sys

out = sys.argv[1] if len(sys.argv) > 1 else os.path.join(os.path.dirname(os.path.abspath(__file__)), "out")
VARIANTS = ["on", "off", "base"]


def runs(exp, v):
    R = []
    for f in sorted(glob.glob(f"{out}/{exp}-{v}-[0-9]*.json")):
        try:
            R.append(json.load(open(f)))
        except ValueError:
            pass
    return [r for r in R if "error" not in r]


def rng(vals, fmt="{:,.0f}"):
    if not vals:
        return "n/a"
    return f"{fmt.format(st.mean(vals))} [{fmt.format(min(vals))}-{fmt.format(max(vals))}]"


def hs(s):
    return s["heap_objects"] + s["stacks"]


print("## idle: bytes a connection, marginal from 10k to 20k\n")
print("| exp | variant | runs | fails | RSS 10k to 20k | RSS 0 to 20k | heap plus stack | stacks | goroutines at 20k |")
print("|---|---|---|---|---|---|---|---|---|")
for exp in ("idle-plain", "idle-tls", "idle-ws"):
    for v in VARIANTS:
        R = runs(exp, v)
        if not R:
            continue
        d = [r["conns_n2"] - r["conns_n1"] for r in R]
        rss = [(r["rss_n2"] - r["rss_n1"]) / k for r, k in zip(R, d)]
        rss0 = [(r["rss_n2"] - r["rss0"]) / r["conns_n2"] for r in R]
        h = [(hs(r["stats_n2"]) - hs(r["stats_n1"])) / k for r, k in zip(R, d)]
        s = [(r["stats_n2"]["stacks"] - r["stats_n1"]["stacks"]) / k for r, k in zip(R, d)]
        g = [r["stats_n2"]["goroutines"] for r in R]
        fails = sum(int(r.get("fails", 0)) for r in R)
        print(f"| {exp} | {v} | {len(R)} | {fails} | {rng(rss)} | {rng(rss0)} | {rng(h)} | {rng(s)} | {rng(g)} |")

print("\n## load: 256 connections, 10 s after 3 s of warm-up\n")
print("| exp | variant | runs | errors | req/s | p50 µs | p99 µs | p999 µs | server CPU ns/req |")
print("|---|---|---|---|---|---|---|---|---|")
for exp in ("get", "post", "get-tls", "wsload"):
    for v in VARIANTS:
        R = runs(exp, v)
        if not R:
            continue
        rate = [r.get("rps", r.get("mps", 0)) for r in R]
        cpu = [r.get("cpu_ns_per_req", r.get("cpu_ns_per_msg", 0)) for r in R]
        errs = sum(int(r.get("errors", 0)) for r in R)
        print(f"| {exp} | {v} | {len(R)} | {errs} | {rng(rate)} | {rng([r['p50_us'] for r in R])} | {rng([r['p99_us'] for r in R])} | {rng([r['p999_us'] for r in R])} | {rng(cpu)} |")

print("\n## allocations a POST (one connection, 50,000 requests)\n")
print("| variant | runs | allocations | bytes |")
print("|---|---|---|---|")
for v in VARIANTS:
    R = runs("allocs", v)
    if R:
        print(f"| {v} | {len(R)} | {rng([r['allocs_per_req'] for r in R], '{:.3f}')} | {rng([r['bytes_per_req'] for r in R])} |")

print("\n## wake: 2,000 connections, a request every 300 ms each (jittered 10%), 15 s\n")
print("| exp | variant | runs | errors | req/s | p50 µs | p90 µs | p99 µs | p999 µs | server CPU ns/req | allocations a request | goroutines at the end |")
print("|---|---|---|---|---|---|---|---|---|---|---|---|")
for exp in ("wake-plain", "wake-tls", "wake-ws"):
    for v in VARIANTS:
        R = runs(exp, v)
        if not R:
            continue
        errs = sum(int(r.get("errors", 0)) for r in R)
        print(f"| {exp} | {v} | {len(R)} | {errs} | {rng([r['rps'] for r in R])} | {rng([r['p50_us'] for r in R])} | {rng([r['p90_us'] for r in R])} | {rng([r['p99_us'] for r in R])} | {rng([r['p999_us'] for r in R])} | {rng([r['cpu_ns_per_req'] for r in R])} | {rng([r['allocs_per_req'] for r in R], '{:.2f}')} | {rng([r['goroutines_end'] for r in R])} |")
