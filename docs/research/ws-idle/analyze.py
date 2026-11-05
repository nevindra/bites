import json,glob,statistics as st
S=["hand-b","hand-b2","hand-park","gws","coder","gorilla","fib","nbio"]
def load(s,exp="idle"):
    out=[]
    for f in sorted(glob.glob(f"raw/{exp}-{s}-[0-9].json")):
        try: out.append(json.load(open(f)))
        except Exception: pass
    return out
def rng(v,fmt="{:.0f}"):
    if not v: return "n/a"
    return f"{fmt.format(st.mean(v))} [{fmt.format(min(v))}-{fmt.format(max(v))}]"
def hs(r,t): return r[t]["heap_inuse"]+r[t]["stack_inuse"]
print("| server | runs | fails | RSS/conn @10k | RSS marginal 10k->20k | heap+stack/conn @10k | heap+stack marginal | stack_inuse/conn @20k | goroutines @20k | scan_stack/conn (after GC) | RSS/conn marginal after 2 GC |")
print("|---|---|---|---|---|---|---|---|---|---|---|")
for s in S:
    R=load(s)
    if not R: continue
    a=[(r["rss_n1"]-r["rss0"])/r["conns_n1"] for r in R]
    m=[(r["rss_n2"]-r["rss_n1"])/(r["conns_n2"]-r["conns_n1"]) for r in R]
    ha=[(hs(r,"stats_n1")-hs(r,"stats0"))/r["conns_n1"] for r in R]
    hm=[(hs(r,"stats_n2")-hs(r,"stats_n1"))/(r["conns_n2"]-r["conns_n1"]) for r in R]
    sk=[(r["stats_n2"]["stack_inuse"]-r["stats0"]["stack_inuse"])/r["conns_n2"] for r in R]
    g=[r["stats_n2"]["goroutines"] for r in R]
    sc=[(r["stats_gc"]["scan_stack"])/r["conns_n2"] for r in R if r.get("stats_gc")]
    ga=[(r["rss_gc"]-r["rss0"])/r["conns_n2"] for r in R if r.get("rss_gc")]
    print(f"| {s} | {len(R)} | {sum(r['fails'] for r in R)} | {rng(a)} | {rng(m)} | {rng(ha)} | {rng(hm)} | {rng(sk)} | {rng(g)} | {rng(sc)} | {rng(ga)} (avg @20k) |")
