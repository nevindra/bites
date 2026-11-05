# usage: python3 analyze.py ; reads ~/.cache/nilo-go-ws-gate/raw/{msg,nomsg}-N.json
import json, glob, os, statistics as st
raw = os.path.expanduser("~/.cache/nilo-go-ws-gate/raw")
def rng(v):
    return f"{st.mean(v):.0f} [{min(v):.0f}-{max(v):.0f}]"
print("| kind | runs | fails | RSS per conn, 0 to 10k | RSS marginal 10k to 20k | heap+stack per conn after 2 GC at 20k | stacks per conn at 20k | goroutines at 20k |")
print("|---|---|---|---|---|---|---|---|")
for kind in ("nomsg", "msg"):
    R = [json.load(open(f)) for f in sorted(glob.glob(f"{raw}/{kind}-[0-9].json"))]
    a = [(r["rss_n1"] - r["rss0"]) / r["conns_n1"] for r in R]
    m = [(r["rss_n2"] - r["rss_n1"]) / (r["conns_n2"] - r["conns_n1"]) for r in R]
    h = [(r["stats_gc"]["m_objects"] + r["stats_gc"]["m_stacks"] - r["stats0"]["m_objects"] - r["stats0"]["m_stacks"]) / r["conns_n2"] for r in R]
    s = [(r["stats_gc"]["m_stacks"] - r["stats0"]["m_stacks"]) / r["conns_n2"] for r in R]
    g = [r["stats_n2"]["goroutines"] for r in R]
    print(f"| {kind} | {len(R)} | {sum(r['fails'] for r in R)} | {rng(a)} | {rng(m)} | {rng(h)} | {rng(s)} | {rng(g)} |")
